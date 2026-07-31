package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// adminKeyHeader는 관리자 전용 엔드포인트가 요구하는 인증 헤더다. 이
// 엔드포인트들은 프론트엔드 어디에도 노출되지 않는다 — dhlottery가 막혔을
// 때 개발자가 curl/스크립트로 직접 회차를 채워 넣기 위한 순수 API다.
const adminKeyHeader = "X-Admin-Key"

// requireAdminKey는 ADMIN_SECRET_KEY 환경변수와 X-Admin-Key 헤더를
// crypto/subtle.ConstantTimeCompare로 비교한다 — 문자열을 그대로 ==로
// 비교하면 타이밍 공격(응답 시간 차이로 몇 번째 글자까지 맞았는지 추측)에
// 노출될 수 있는데, 관리자 키처럼 무제한 시도가 가능한 엔드포인트에서는
// 이 정도의 방어를 들여도 손해 볼 게 없다. 통과하지 못하면 이 함수가 직접
// 에러 응답까지 쓰고 false를 반환하므로, 호출하는 쪽은 false를 받으면
// 그냥 즉시 return하면 된다.
func requireAdminKey(w http.ResponseWriter, r *http.Request) bool {
	expected := os.Getenv("ADMIN_SECRET_KEY")
	if expected == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "ADMIN_SECRET_KEY가 설정되지 않아 관리자 기능을 사용할 수 없습니다"})
		return false
	}

	got := r.Header.Get(adminKeyHeader)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "인증 실패 — X-Admin-Key 헤더를 확인하세요"})
		return false
	}
	return true
}

// lottoEntryInput은 회차 하나를 수동으로(관리자 API) 또는 정적으로(시드
// 파일) 채워 넣을 때 쓰는 공통 입력 형태다. json 태그를 사용자가 요청한
// POST 바디 형식 및 backend/data/lotto_seed.json의 필드명과 그대로
// 맞춘다.
type lottoEntryInput struct {
	DrwNo   int    `json:"drwNo"`
	DrwDate string `json:"drwDate"`
	Numbers []int  `json:"numbers"`
	Bonus   int    `json:"bonus"`
}

// validateLottoEntry는 관리자 수동 입력과 시드 파일 로딩이 공유하는 단일
// 검증 로직이다 — 사람이 손으로 입력하는 값(오타 위험이 실제 API 응답보다
// 훨씬 큼)이므로, dhlottery 실제 응답을 저장하는 insertLottoDraw보다 훨씬
// 엄격하게 검사한다.
func validateLottoEntry(e lottoEntryInput) error {
	if e.DrwNo < 1 {
		return fmt.Errorf("drwNo는 1 이상이어야 합니다 (받은 값: %d)", e.DrwNo)
	}
	if _, err := time.Parse("2006-01-02", e.DrwDate); err != nil {
		return fmt.Errorf("drwDate는 YYYY-MM-DD 형식이어야 합니다 (받은 값: %q)", e.DrwDate)
	}
	if len(e.Numbers) != 6 {
		return fmt.Errorf("numbers는 정확히 6개여야 합니다 (받은 개수: %d)", len(e.Numbers))
	}

	seen := make(map[int]bool, 6)
	for _, n := range e.Numbers {
		if n < 1 || n > 45 {
			return fmt.Errorf("numbers는 1~45 범위여야 합니다 (받은 값: %d)", n)
		}
		if seen[n] {
			return fmt.Errorf("numbers에 중복된 번호가 있습니다: %d", n)
		}
		seen[n] = true
	}

	if e.Bonus < 1 || e.Bonus > 45 {
		return fmt.Errorf("bonus는 1~45 범위여야 합니다 (받은 값: %d)", e.Bonus)
	}
	if seen[e.Bonus] {
		return fmt.Errorf("bonus는 numbers와 겹칠 수 없습니다 (받은 값: %d)", e.Bonus)
	}
	return nil
}

// upsertLottoDrawManual은 insertLottoDraw(ON CONFLICT DO NOTHING — 자동
// 수집 전용, dhlottery의 실제 응답은 절대 바뀌지 않으므로 이미 있으면
// 무시)와 달리 ON CONFLICT DO UPDATE를 쓴다 — 손으로 입력하다 보면 오타로
// 잘못 넣었을 수 있고, 관리자가 같은 회차를 다시 제출하면 그건 "정정"
// 의도이지 "이미 있으니 무시"가 아니기 때문이다. 시드 파일 로딩도 이
// 함수를 그대로 재사용한다.
func upsertLottoDrawManual(ctx context.Context, conn *sql.DB, e lottoEntryInput) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO lotto_draws (drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(drw_no) DO UPDATE SET
			drw_date = excluded.drw_date,
			num1 = excluded.num1, num2 = excluded.num2, num3 = excluded.num3,
			num4 = excluded.num4, num5 = excluded.num5, num6 = excluded.num6,
			bonus_no = excluded.bonus_no`,
		e.DrwNo, e.DrwDate, e.Numbers[0], e.Numbers[1], e.Numbers[2], e.Numbers[3], e.Numbers[4], e.Numbers[5], e.Bonus,
	)
	return err
}

// lottoManualEntryHandler는 POST /api/admin/lotto/manual-entry를 서빙한다
// — dhlottery가 막혔을 때 회차 하나를 수동으로 채워 넣는 유일한 대체
// 수단이다. 프론트엔드 어디에도 이 엔드포인트로 연결되는 버튼/메뉴가
// 없다 — 순수하게 curl이나 scripts/manual_lotto_entry.sh로만 호출한다.
func lottoManualEntryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST 요청만 허용됩니다"})
		return
	}
	if !requireAdminKey(w, r) {
		return
	}
	if db == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "데이터베이스에 연결할 수 없습니다"})
		return
	}

	var input lottoEntryInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "요청 본문이 올바른 JSON이 아닙니다: " + err.Error()})
		return
	}
	if err := validateLottoEntry(input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lottoInsertTimeout)
	defer cancel()
	if err := upsertLottoDrawManual(ctx, db, input); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "저장 실패: " + err.Error()})
		return
	}

	var savedCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lotto_draws`).Scan(&savedCount); err != nil {
		savedCount = -1
	}

	log.Printf("로또(관리자 수동 입력): %d회차 저장/갱신됨(현재 총 %d회차)", input.DrwNo, savedCount)
	json.NewEncoder(w).Encode(map[string]any{
		"success":    true,
		"drwNo":      input.DrwNo,
		"savedCount": savedCount,
	})
}

// lottoAdminStatusHandler는 GET /api/admin/lotto/status를 서빙한다 — 현재
// DB에 있는 회차의 최소/최대값과, 그 범위 안에서 비어있는(아직 채워지지
// 않은) 회차 목록을 보여준다. dhlottery가 며칠~몇 주씩 막혀서 자동 수집이
// 계속 실패했을 때, 어느 회차부터 manual-entry로 채워야 하는지 알아내는
// 용도다.
func lottoAdminStatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !requireAdminKey(w, r) {
		return
	}
	if db == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "데이터베이스에 연결할 수 없습니다"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lottoTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, `SELECT drw_no FROM lotto_draws ORDER BY drw_no`)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	existing := make(map[int]bool)
	minDrwNo, maxDrwNo := 0, 0
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		existing[n] = true
		if minDrwNo == 0 || n < minDrwNo {
			minDrwNo = n
		}
		if n > maxDrwNo {
			maxDrwNo = n
		}
	}
	if err := rows.Err(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	theoretical := theoreticalLatestDrwNo(time.Now())

	// 빈 DB라면 비교할 하한이 없으니, missing은 의미가 없다(전체가
	// "아직 안 채워짐"일 뿐이다) — 그냥 빈 목록으로 둔다.
	missing := []int{}
	if len(existing) > 0 {
		for n := minDrwNo; n <= theoretical; n++ {
			if !existing[n] {
				missing = append(missing, n)
			}
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"savedCount":        len(existing),
		"minDrwNo":          minDrwNo,
		"maxDrwNo":          maxDrwNo,
		"theoreticalLatest": theoretical,
		"missing":           missing,
	})
}
