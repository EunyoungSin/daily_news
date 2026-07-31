package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
)

// lottoSeedJSON은 backend/data/lotto_seed.json을 빌드 시점에 바이너리에
// 포함시킨다(go:embed) — 배포 환경의 작업 디렉터리가 무엇이든, 상대
// 경로로 파일을 찾다 실패하는 일 없이 항상 이 데이터를 쓸 수 있다.
// 이 파일은 개발자가 신뢰할 수 있는 출처에서 직접 채워 넣는 정적
// 데이터로 취급한다 — 지어낸 값을 넣어두면 실제 당첨번호인 것처럼
// 화면에 노출될 위험이 있으므로, 기본값은 빈 배열이다.
//
//go:embed data/lotto_seed.json
var lottoSeedJSON []byte

// seedLottoDrawsIfEmpty는 서버가 처음 뜰 때(lotto_draws가 비어있을 때만)
// 딱 한 번 시드 데이터를 채운다 — dhlottery를 전혀 호출하지 않는다.
// 이후로는(테이블에 행이 하나라도 있으면) 아무 것도 하지 않으므로 매
// 재시작마다 반복 실행해도 안전하다.
//
// 검증(validateLottoEntry)과 저장(upsertLottoDrawManual)은 관리자 수동
// 입력(lotto_admin.go)과 완전히 동일한 경로를 그대로 재사용한다 — 시드
// 파일도 결국 "사람이 채워 넣는 정적 데이터"라는 점에서 관리자 수동
// 입력과 성격이 같고, 오타 방지를 위한 엄격한 검증도 똑같이 필요하기
// 때문이다.
func seedLottoDrawsIfEmpty(ctx context.Context, conn *sql.DB) error {
	if conn == nil {
		return nil
	}

	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM lotto_draws`).Scan(&count); err != nil {
		return fmt.Errorf("count lotto_draws: %w", err)
	}
	if count > 0 {
		return nil
	}

	var seed []lottoEntryInput
	if err := json.Unmarshal(lottoSeedJSON, &seed); err != nil {
		return fmt.Errorf("parse lotto_seed.json: %w", err)
	}
	if len(seed) == 0 {
		log.Println("로또: 시드 파일(backend/data/lotto_seed.json)이 비어 있어 건너뜁니다 — " +
			"관리자 수동 입력(POST /api/admin/lotto/manual-entry)이나 자동 주간 수집으로 채워야 합니다")
		return nil
	}

	inserted := 0
	for _, e := range seed {
		if err := validateLottoEntry(e); err != nil {
			log.Printf("로또: 시드 항목(%d회차) 검증 실패, 건너뜀: %v", e.DrwNo, err)
			continue
		}
		if err := upsertLottoDrawManual(ctx, conn, e); err != nil {
			log.Printf("로또: 시드 항목(%d회차) 저장 실패: %v", e.DrwNo, err)
			continue
		}
		inserted++
	}
	log.Printf("로또: 시드 파일에서 %d/%d개 회차를 DB에 삽입했습니다(외부 API 호출 없음)", inserted, len(seed))
	return nil
}
