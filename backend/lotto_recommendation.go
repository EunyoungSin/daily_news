package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

const (
	// 이번 회차 판매는 토요일 20:00 KST에 마감된다.
	lottoSalesCloseWeekday = time.Saturday
	lottoSalesCloseHour    = 20
	// 다음 회차의 추천번호는 일요일 06:00 KST에 공개된다 — lotto_draws가
	// 새로 추첨된 회차를 반영하는 시점과 정확히 같아서, "이번 주" 추천번호와
	// "이번 주" 빈도 데이터가 함께 바뀐다.
	lottoCycleStartWeekday = time.Sunday
	lottoCycleStartHour    = 6
)

// isLottoRecommendationBlackout은 `now`가 이번 회차 판매 마감 시점과 다음
// 회차 번호 공개 시점 사이에 있는지를 나타낸다. 이 구간 동안에는 더 이상
// 판매되지 않는 회차의 낡은 번호를 보여주는 대신, 추천번호 자체를 아예
// 숨긴다.
func isLottoRecommendationBlackout(now time.Time) bool {
	now = now.In(kst)
	if now.Weekday() == lottoSalesCloseWeekday && now.Hour() >= lottoSalesCloseHour {
		return true
	}
	if now.Weekday() == lottoCycleStartWeekday && now.Hour() < lottoCycleStartHour {
		return true
	}
	return false
}

// lottoCycleStartDate는 `now` 이전(또는 같은 시점)의 가장 최근 일요일
// 06:00 KST를 반환한다 — "이번 주" 추천 사이클을 식별하는 값이다.
// isLottoRecommendationBlackout(now)가 false일 때만 의미가 있으며, blackout
// 구간 중에는 아직 "현재" 사이클이 존재하지 않는다.
func lottoCycleStartDate(now time.Time) time.Time {
	now = now.In(kst)
	daysSinceSunday := int(now.Weekday())
	sunday := now.AddDate(0, 0, -daysSinceSunday)
	return time.Date(sunday.Year(), sunday.Month(), sunday.Day(), lottoCycleStartHour, 0, 0, 0, kst)
}

// nextLottoAvailableAt은 `now`보다 엄밀히 이후인 다음 일요일 06:00 KST를
// 반환한다 — blackout이 언제 끝나는지 프론트엔드에 알려주는 데 쓰인다.
func nextLottoAvailableAt(now time.Time) time.Time {
	now = now.In(kst)
	daysUntilSunday := (7 - int(now.Weekday())) % 7
	candidate := now.AddDate(0, 0, daysUntilSunday)
	candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), lottoCycleStartHour, 0, 0, 0, kst)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

// encodeRecommendationSet은 세트 하나를 두 개의 JSON 문자열로 나눠
// 인코딩한다 — numbers(번호 6개짜리 배열)와 stats(통계 객체)다. 별도
// 컬럼(numbers, stats_json)에 나눠 저장하는 이유는 DB 스키마 설계와
// 맞추기 위해서다: numbers는 원래부터 있던 컬럼이고, stats_json은 이번에
// 새로 추가한 컬럼이다.
func encodeRecommendationSet(set LottoRecommendationSet) (numbersJSON, statsJSON string, err error) {
	nb, err := json.Marshal(set.Numbers)
	if err != nil {
		return "", "", fmt.Errorf("encode numbers: %w", err)
	}
	sb, err := json.Marshal(set.Stats)
	if err != nil {
		return "", "", fmt.Errorf("encode stats: %w", err)
	}
	return string(nb), string(sb), nil
}

// decodeRecommendationSet은 encodeRecommendationSet의 역이다. 예전
// 형식(세트 여러 개를 담은 2차원 배열, 또는 그보다 더 예전의 CSV)으로
// 저장된 행이 들어오면 타입이 맞지 않아 이 단계에서 에러를 반환한다 —
// lookupLottoRecommendation이 이를 "낡은 캐시"로 취급해 재계산하므로,
// 형식이 여러 번 바뀌어도 캐시가 스스로 복구된다.
func decodeRecommendationSet(numbersJSON, statsJSON string) (LottoRecommendationSet, error) {
	var numbers []int
	if err := json.Unmarshal([]byte(numbersJSON), &numbers); err != nil {
		return LottoRecommendationSet{}, fmt.Errorf("decode numbers: %w", err)
	}
	var stats LottoRecommendationStats
	if err := json.Unmarshal([]byte(statsJSON), &stats); err != nil {
		return LottoRecommendationSet{}, fmt.Errorf("decode stats: %w", err)
	}
	return LottoRecommendationSet{Numbers: numbers, Stats: stats}, nil
}

// lookupLottoRecommendation은 (cycle_start_date, mode) 복합키로 정확히
// 한 행을 조회한다 — 테이블의 PRIMARY KEY가 이 두 컬럼의 조합이므로
// (db.go의 createLottoRecommendationTable 참고), 모드가 다르면 애초에
// 서로 다른 행이라 캐시가 서로 덮어쓸 일이 없다. 사용자가 uniform ->
// trend -> uniform으로 오가도 각 모드의 행이 독립적으로 남아있다가 그대로
// 재사용된다.
//
// found는 "그 (cycle_start_date, mode) 행이 실제로 존재하는지"만으로
// 결정한다 — set 디코딩 실패(예: 이 컬럼이 다른 형식이었던 시절에 저장된
// 행)는 found=false가 아니라 found=true와 set=nil로 표현한다. 이 구분이
// 중요한 이유는 getLottoRecommendation이 found 값에 따라 저장 경로를
// 고르기 때문이다: found=false면 INSERT OR IGNORE를 쓰는데, 이미 존재하는
// 행에는 이 INSERT가 조용히 아무 효과가 없다(IGNORE) — 디코딩 실패를
// found=false로 잘못 표현하면, 깨진 옛 형식 행을 절대 새 형식으로 덮어쓰지
// 못하고 매 요청마다 디코딩에 계속 실패하는 채로 남는다. found=true(그리고
// set=nil)로 표현해야 getLottoRecommendation이 "찾았지만 낡았다" 분기
// (UPDATE 사용, 기존 based_on_data_hash를 WHERE 조건으로 덮어쓸 수 있다)를
// 타서 실제로 새 형식으로 갱신된다.
// isRetroactive는 이 행이 사용자가 그 사이클에 실제로 조회해서 생긴
// 정상 캐시가 아니라, 나중에(새 회차 저장 시점에) "그때 조회했다면
// 무엇이 나왔을지"를 사후 계산해 채운 행인지를 나타낸다 —
// lotto_recommendation_history.go 참고. getLottoRecommendation(현재
// 사이클 조회)은 이 값을 쓰지 않지만, 지난주 추천 결과 조회 경로는 이
// 값으로 안내 문구를 붙일지 결정한다.
func lookupLottoRecommendation(ctx context.Context, conn *sql.DB, cycleStartDate, mode string) (set *LottoRecommendationSet, basedOnDrwNo int, basedOnDataHash string, generatedAt time.Time, isRetroactive bool, found bool) {
	var numbersJSON, statsJSON string
	err := conn.QueryRowContext(ctx,
		`SELECT numbers, stats_json, based_on_drw_no, based_on_data_hash, generated_at, is_retroactive FROM lotto_recommendation WHERE cycle_start_date = ? AND mode = ?`,
		cycleStartDate, mode,
	).Scan(&numbersJSON, &statsJSON, &basedOnDrwNo, &basedOnDataHash, &generatedAt, &isRetroactive)
	if err != nil {
		return nil, 0, "", time.Time{}, false, false
	}

	decoded, decodeErr := decodeRecommendationSet(numbersJSON, statsJSON)
	if decodeErr != nil {
		log.Printf("로또: 추천번호 캐시(cycle=%s, mode=%s) 디코딩 실패, 낡은 캐시로 취급해 재계산합니다: %v", cycleStartDate, mode, decodeErr)
		return nil, basedOnDrwNo, basedOnDataHash, generatedAt, isRetroactive, true
	}
	return &decoded, basedOnDrwNo, basedOnDataHash, generatedAt, isRetroactive, true
}

// insertLottoRecommendationIfAbsent는 `INSERT OR IGNORE`를 사용한다
// ((cycle_start_date, mode)가 복합 primary key다) — MySQL의 `INSERT
// IGNORE`에 해당하는 SQLite/libSQL 문법이다. 같은 사이클의 같은 모드에
// 대해 동시에 생성 요청이 들어와도 둘 다 쓰기에 성공하는 일이 없도록
// 하기 위해서다 — 경쟁에서 진 쪽의 insert는 조용히 무시되고,
// getLottoRecommendation이 이후에 다시 읽어서 실제로 이긴 쪽의 row를
// 가져온다. 한 사이클의 한 모드 안에서 모든 요청이 동일한 번호를 보게
// 되는 것은 이 insert가 아니라 그 재조회(re-read) 덕분이다.
func insertLottoRecommendationIfAbsent(ctx context.Context, conn *sql.DB, cycleStartDate, mode string, basedOnDrwNo int, basedOnDataHash string, set LottoRecommendationSet, generatedAt time.Time) error {
	numbersJSON, statsJSON, err := encodeRecommendationSet(set)
	if err != nil {
		return err
	}
	// number_groups를 명시적으로 빈 문자열로 채운다 — 이미 배포된 DB의
	// 이 컬럼은 (db.go의 createLottoRecommendationTable 문서 주석 참고)
	// DEFAULT 절 없이 NOT NULL로 생성되어 있어서, INSERT 문에서 아예
	// 빼버리면 그 DB에서는 제약 위반으로 실패한다.
	_, err = conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO lotto_recommendation (cycle_start_date, mode, based_on_drw_no, based_on_data_hash, numbers, number_groups, stats_json, generated_at)
		VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
		cycleStartDate, mode, basedOnDrwNo, basedOnDataHash, numbersJSON, statsJSON, generatedAt,
	)
	return err
}

// updateLottoRecommendationForNewData는 이미 존재하는 (cycle_start_date,
// mode) 행을 새 통계 입력 기준으로 덮어쓴다. 이는 두 가지 상황을 포함한다:
// (1) 자동 수집이 막혔다가 관리자가 나중에 새 회차를 수동으로 넣는 경우,
// (2) 새 회차 없이 이미 저장된 회차의 오타를 나중에 정정하는 경우
// (latest_drw_no는 그대로지만 frequency 자체가 바뀐다). WHERE 절에 mode를
// 반드시 포함해야 한다 — 포함하지 않으면 cycle_start_date가 같은 다른
// 모드의 행까지 함께 걸려, 이 갱신이 의도한 모드가 아닌 엉뚱한 모드의
// 행을 덮어쓸 수 있다(모드가 달라도 based_on_data_hash는 서로 같을 수
// 있으므로 — 이 해시는 이제 모드와 무관하게 frequency만으로 계산된다).
// staleDataHash도 함께 거는 것은, 동시에 여러 요청이 "낡았다"고 판단해
// 각자 새로 계산한(서로 다른 무작위) 번호로 서로를 계속 덮어쓰는 경쟁을
// 막기 위해서다 — 이미 누군가 newDataHash로 갱신해뒀다면 이 UPDATE는
// 0행에 적용되고 조용히 넘어가며, getLottoRecommendation이 재조회로 그
// 결과를 가져온다.
func updateLottoRecommendationForNewData(ctx context.Context, conn *sql.DB, cycleStartDate, mode, staleDataHash string, newDrwNo int, newDataHash string, set LottoRecommendationSet, generatedAt time.Time) error {
	numbersJSON, statsJSON, err := encodeRecommendationSet(set)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `
		UPDATE lotto_recommendation
		SET based_on_drw_no = ?, based_on_data_hash = ?, numbers = ?, stats_json = ?, generated_at = ?
		WHERE cycle_start_date = ? AND mode = ? AND based_on_data_hash = ?`,
		newDrwNo, newDataHash, numbersJSON, statsJSON, generatedAt, cycleStartDate, mode, staleDataHash,
	)
	return err
}

func toLottoRecommendation(cycleStartDate, mode string, set LottoRecommendationSet, generatedAt time.Time) LottoRecommendation {
	return LottoRecommendation{
		Mode:           mode,
		Set:            &set,
		CycleStartDate: cycleStartDate,
		GeneratedAt:    generatedAt.Format(time.RFC3339),
	}
}

// latestDrawNumbers는 history(최신순 정렬)의 가장 최근 회차 당첨번호를
// 반환한다 — 3단계 "직전 회차 중복" 필터에 쓰인다. history가 비어있으면
// (호출하는 쪽에서 이미 채워져 있음을 보장하지만, 방어적으로) nil을
// 반환하며, 이 경우 checkPreviousDrawOverlap은 항상 통과한다(겹칠 대상
// 자체가 없으므로).
func latestDrawNumbers(history []LottoDraw) []int {
	if len(history) == 0 {
		return nil
	}
	return history[0].Numbers
}

// getLottoRecommendation은 이번 요청의 추천 상태를 결정한다: blackout 안내,
// 캐시된 (사이클, 모드)의 세트, 또는 새로 계산한 세트 중 하나다. (cycle_start_date,
// mode) 복합키 덕분에 사용자가 uniform -> trend -> uniform으로 오가도 각
// 모드의 캐시가 서로 독립적으로 남아있다가 그대로 재사용된다 — 이 함수는
// 딱 하나의 (cycle_start_date, mode) 행만 보고 판단하므로 "다른 모드가
// 방금 이 사이클을 덮어썼는지" 같은 걱정을 할 필요가 없다.
//
// 캐시 유효성은 그 계산에 실제로 쓰인 frequency 입력의 해시
// (based_on_data_hash)가 지금의 frequency와 같은지로만 판단한다 — 정상적인
// 주간 흐름(매주 새 사이클 시작 시점에 최신 회차도 함께 바뀜)에서는 사실상
// 항상 같지만, 다음 두 예외 상황에서는 어긋날 수 있어 이 컬럼이 필요하다:
// (1) 자동 수집이 막혀 있다가 같은 사이클 도중에 관리자가 새 회차를 수동
// 입력하는 경우, (2) 새 회차 없이 이미 저장된 회차의 오타를 나중에 정정하는
// 경우. 이 두 경우 모두 "회차가 갱신"된 것으로 취급되어, 캐시된 모든 모드가
// (각자 다음에 조회될 때) 독립적으로 무효화되고 다시 계산된다.
func getLottoRecommendation(ctx context.Context, conn *sql.DB, history []LottoDraw, frequency map[int]int, latestDrwNo int, mode string, now time.Time) LottoRecommendation {
	if isLottoRecommendationBlackout(now) {
		return LottoRecommendation{
			IsBlackout:      true,
			NextAvailableAt: nextLottoAvailableAt(now).Format(time.RFC3339),
		}
	}
	if !isValidLottoMode(mode) {
		mode = lottoModeUniform
	}

	cycleStart := lottoCycleStartDate(now).Format("2006-01-02")
	dataHash := hashJSON(frequency)

	// set != nil 조건이 필요하다: found=true인데 set이 nil인 경우는
	// "행은 있지만 디코딩에 실패한(예: 예전 형식) 낡은 캐시"를 뜻하며
	// (lookupLottoRecommendation 문서 주석 참고), 이 경우도 데이터 해시
	// 불일치와 똑같이 재계산 대상이다.
	cached, basedOnDrwNo, basedOnDataHash, generatedAt, _, found := lookupLottoRecommendation(ctx, conn, cycleStart, mode)
	if found && cached != nil && basedOnDataHash == dataHash {
		return toLottoRecommendation(cycleStart, mode, *cached, generatedAt)
	}

	stats := computeNumberStats(frequency, history)
	previousDrawNumbers := latestDrawNumbers(history)
	set := generateRecommendationSet(mode, stats, previousDrawNumbers)
	generatedAt = time.Now()

	if found {
		log.Printf("로또: 추천번호 캐시가 낡음(cycle=%s, mode=%s, 캐시 기준 회차=%d, 최신 회차=%d, 디코딩 성공=%v) — 재계산",
			cycleStart, mode, basedOnDrwNo, latestDrwNo, cached != nil)
		if err := updateLottoRecommendationForNewData(ctx, conn, cycleStart, mode, basedOnDataHash, latestDrwNo, dataHash, set, generatedAt); err != nil {
			log.Printf("로또: 추천번호 갱신 실패(cycle=%s, mode=%s): %v", cycleStart, mode, err)
		}
	} else {
		if err := insertLottoRecommendationIfAbsent(ctx, conn, cycleStart, mode, latestDrwNo, dataHash, set, generatedAt); err != nil {
			// 치명적이지 않다 — 최악의 경우 이번 요청은 저장되지 않을 방금 뽑은
			// 세트를 보여주게 되고, 다음 요청이 insert를 다시 시도한다.
			log.Printf("로또: 추천번호 저장 실패(cycle=%s, mode=%s): %v", cycleStart, mode, err)
		}
	}

	// 우리의 write가 경쟁에서 이겼는지 여부와 무관하게 다시 읽는다. 그래야
	// 경쟁에서 진 동시 요청도 자신이 로컬에서 우연히 뽑은 세트가 아니라
	// 실제로 저장된 세트를 반환하게 된다.
	if cachedSet, _, _, cachedGeneratedAt, _, found := lookupLottoRecommendation(ctx, conn, cycleStart, mode); found && cachedSet != nil {
		return toLottoRecommendation(cycleStart, mode, *cachedSet, cachedGeneratedAt)
	}

	// (단순한 경쟁 문제가 아니라) DB 왕복 자체가 완전히 실패한 경우 — 이번
	// 요청에서라도 섹션이 계속 동작하도록 이미 생성해둔 세트로 대체한다.
	return toLottoRecommendation(cycleStart, mode, set, generatedAt)
}
