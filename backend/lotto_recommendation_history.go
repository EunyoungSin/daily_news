package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lottoRecommendationModes는 "지난주 추천 결과"를 항상 이 고정 순서로
// 계산·노출한다 — 사용자가 실제로 지난주에 조회한 모드가 무엇이었든
// 무관하게 trend -> regression -> uniform 3개를 전부 보여주고, 절대 일치
// 개수 등으로 재정렬하지 않는다. 이 순서 고정 자체가 "어떤 방식이 더
// 우수하다"는 인상을 주지 않기 위한 설계의 일부다 — 순위처럼 보이는
// 정렬(예: 일치 개수 내림차순)은 실제 당첨 확률과 무관한 우연의 결과를
// "성적표"처럼 보이게 만든다.
var lottoRecommendationModes = []string{lottoModeTrend, lottoModeRegression, lottoModeUniform}

// overlapNumbers는 a와 b에 공통으로 있는 번호를 오름차순으로 반환한다 —
// countOverlap(개수만 반환)과 달리 실제 겹친 번호 목록 자체가 필요할 때
// 쓴다.
func overlapNumbers(a, b []int) []int {
	bSet := make(map[int]bool, len(b))
	for _, n := range b {
		bSet[n] = true
	}
	var result []int
	for _, n := range a {
		if bSet[n] {
			result = append(result, n)
		}
	}
	sort.Ints(result)
	return result
}

// formatMatchedNumbers/parseMatchedNumbers는 matched_numbers 컬럼의 "5,17"
// 형태 왕복 인코딩이다. JSON이 아니라 쉼표 구분 문자열을 쓰는 이유는 이미
// numbers/stats_json 컬럼이 JSON을 쓰고 있어, 이 값 하나만 별도 컬럼으로
// 사람이 SQL로 직접 들여다볼 때도 바로 읽히게 하기 위해서다(디버깅 시
// `SELECT matched_numbers FROM ...`만으로 바로 확인 가능).
func formatMatchedNumbers(nums []int) string {
	if len(nums) == 0 {
		return ""
	}
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

func parseMatchedNumbers(csv string) []int {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err == nil {
			nums = append(nums, n)
		}
	}
	return nums
}

// LottoRecommendationMatch는 "지난주 추천 결과" API 응답의 항목 하나다 —
// models.go 참고.

// insertRetroactiveLottoRecommendation은 사용자가 그 사이클에 실제로
// 조회하지 않아 DB에 없던 (cycleStartDate, mode) 행을, asOfDrwNo 시점까지의
// 데이터로 사후 계산한 set과 함께 is_retroactive=true로 저장한다.
// INSERT OR IGNORE를 쓰는 이유는 insertLottoRecommendationIfAbsent와
// 같다 — 동시에 같은 사이클/모드에 대해 사후 계산이 경쟁적으로 여러 번
// 시도돼도(예: 자동 수집 훅과 어떤 요청이 동시에 지난주 결과를 확인하는
// 경우) 안전하게 하나만 저장되게 하기 위해서다.
func insertRetroactiveLottoRecommendation(ctx context.Context, conn *sql.DB, cycleStartDate, mode string, basedOnDrwNo int, basedOnDataHash string, set LottoRecommendationSet, generatedAt time.Time) error {
	numbersJSON, statsJSON, err := encodeRecommendationSet(set)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO lotto_recommendation
			(cycle_start_date, mode, based_on_drw_no, based_on_data_hash, numbers, number_groups, stats_json, generated_at, is_retroactive)
		VALUES (?, ?, ?, ?, ?, '', ?, ?, 1)`,
		cycleStartDate, mode, basedOnDrwNo, basedOnDataHash, numbersJSON, statsJSON, generatedAt,
	)
	return err
}

// reencodeLottoRecommendation은 이미 (cycleStartDate, mode) 행이 존재하지만
// numbers가 레거시 포맷(JSON 도입 이전의 순수 CSV, 예: "1,5,8,20,21,30")이라
// decodeRecommendationSet이 실패하는 경우, 새로 계산한 set으로 그 행을
// UPDATE해 자가 치유한다. getLottoRecommendation/updateLottoRecommendationForNewData가
// 쓰는 것과 같은 이유다 — 이런 행은 INSERT OR IGNORE로는 절대 고쳐지지
// 않는다(PK 충돌로 조용히 무시됨). is_retroactive는 건드리지 않는다 —
// 이 행이 원래 실제로 사용자가 조회해서 생긴 것인지(is_retroactive=false)
// 사후 계산으로 생긴 것인지(is_retroactive=true)는 포맷 문제와 무관한
// 별개의 사실이므로 호출자가 원래 값을 그대로 유지해야 한다.
//
// matched_count/matched_numbers를 NULL로 되돌리는 것이 핵심이다 — numbers가
// 바뀌므로 기존에 계산해 둔 일치 결과는 새 numbers와 더 이상 대응하지
// 않는 stale 값이 된다. NULL로 리셋해두면 다음 computeLottoRecommendationMatchForCycle
// 호출이 lookupLottoRecommendationMatch에서 hasMatch=false를 보고 새
// numbers 기준으로 다시 계산해준다.
func reencodeLottoRecommendation(ctx context.Context, conn *sql.DB, cycleStartDate, mode string, basedOnDrwNo int, basedOnDataHash string, set LottoRecommendationSet, generatedAt time.Time) error {
	numbersJSON, statsJSON, err := encodeRecommendationSet(set)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `
		UPDATE lotto_recommendation
		SET based_on_drw_no = ?, based_on_data_hash = ?, numbers = ?, stats_json = ?, generated_at = ?,
			matched_count = NULL, matched_numbers = NULL
		WHERE cycle_start_date = ? AND mode = ?`,
		basedOnDrwNo, basedOnDataHash, numbersJSON, statsJSON, generatedAt, cycleStartDate, mode,
	)
	return err
}

// updateLottoRecommendationMatchResult는 이미 존재하는 (cycleStartDate,
// mode) 행의 matched_count/matched_numbers를 채운다. 이 함수를 부르기
// 전에 그 행이 확보되어 있어야 한다(ensureLottoRecommendationForPastCycle
// 참고).
func updateLottoRecommendationMatchResult(ctx context.Context, conn *sql.DB, cycleStartDate, mode string, matchedCount int, matchedNumbersCSV string) error {
	_, err := conn.ExecContext(ctx, `
		UPDATE lotto_recommendation SET matched_count = ?, matched_numbers = ?
		WHERE cycle_start_date = ? AND mode = ?`,
		matchedCount, matchedNumbersCSV, cycleStartDate, mode,
	)
	return err
}

// lookupLottoRecommendationMatch는 (cycleStartDate, mode) 행에 이미 계산된
// matched_count/matched_numbers가 있으면 그대로 디코딩해 반환한다.
// matched_count가 NULL이면(아직 실제 당첨번호와 대조해본 적이 없음)
// hasMatch=false를 반환한다 — computeLottoRecommendationMatchForCycle이
// 이 값으로 "이미 계산되어 있으니 다시 계산할 필요 없음"을 판단해,
// GET /api/lotto가 반복 폴링될 때마다 매번 같은 UPDATE를 되풀이하지
// 않게 한다.
func lookupLottoRecommendationMatch(ctx context.Context, conn *sql.DB, cycleStartDate, mode string) (matchedCount int, matchedNumbers []int, hasMatch bool, err error) {
	var count sql.NullInt64
	var numbersCSV sql.NullString
	err = conn.QueryRowContext(ctx,
		`SELECT matched_count, matched_numbers FROM lotto_recommendation WHERE cycle_start_date = ? AND mode = ?`,
		cycleStartDate, mode,
	).Scan(&count, &numbersCSV)
	if err != nil {
		return 0, nil, false, err
	}
	if !count.Valid {
		return 0, nil, false, nil
	}
	return int(count.Int64), parseMatchedNumbers(numbersCSV.String), true, nil
}

// ensureLottoRecommendationForPastCycle은 (cycleStart, mode) 행이 이미
// 있으면 그 세트와 is_retroactive 값을 그대로 반환하고, 없으면
// asOfDrwNo까지의 데이터(최근 lottoHistoryWindow회, asOfDrwNo 포함)로
// 4단계 파이프라인을 그대로 재사용해 사후 계산한 뒤 is_retroactive=true로
// 저장하고 반환한다.
//
// 사후 계산에 쓰는 통계 함수(computeNumberStats)와 생성 함수
// (generateRecommendationSet)는 "현재 시점"이 하드코딩되어 있지 않은
// 순수 함수라 — frequency/history를 파라미터로 그대로 받는다 — 이 시점
// 재현이 별도 로직 없이 그대로 가능하다. 유일하게 필요했던 추가 작업은
// frequency/history를 "asOfDrwNo 시점 기준"으로 조회하는
// queryLottoHistoryAsOf/queryFrequencyAsOf였다(lotto.go 참고).
//
// asOfDrwNo 이하 회차가 DB에 하나도 없으면(예: 아주 이른 초기 회차거나
// 데이터 공백 구간) 사후 계산 자체가 무의미하므로 set=nil을 반환하고
// 조용히 건너뛴다 — 이 경우 해당 모드는 "지난주 추천 결과"에서 빠진다.
//
// found=true인데 cached=nil인 경우(레거시 CSV 포맷이라 decodeRecommendationSet
// 실패)를 반드시 별도로 처리해야 한다 — 이 경우 행 자체는 이미 존재하므로
// insertRetroactiveLottoRecommendation의 INSERT OR IGNORE는 PK 충돌로 조용히
// 무시되어 아무것도 고치지 못하고, 매 호출마다 새로 계산한 (uniform 모드의
// 경우 매번 다른) set을 반환하게 되는 버그가 있었다 — reencodeLottoRecommendation로
// 실제 UPDATE해서 자가 치유해야 한다. 이때 is_retroactive는 원래 값
// (cachedIsRetroactive)을 그대로 보존한다 — 포맷 문제 때문에 사후 계산을
// 다시 했다고 해서, 원래 사용자가 실제로 조회해서 생겼던 행(false)이
// 사후 계산 행(true)으로 둔갑해서는 안 된다.
func ensureLottoRecommendationForPastCycle(ctx context.Context, conn *sql.DB, cycleStart, mode string, asOfDrwNo int) (set *LottoRecommendationSet, isRetroactive bool, err error) {
	cached, _, _, _, cachedIsRetroactive, found := lookupLottoRecommendation(ctx, conn, cycleStart, mode)
	if found && cached != nil {
		return cached, cachedIsRetroactive, nil
	}

	history, err := queryLottoHistoryAsOf(ctx, conn, lottoHistoryWindow, asOfDrwNo)
	if err != nil {
		return nil, false, err
	}
	if len(history) == 0 {
		return nil, false, nil
	}
	frequency, err := queryFrequencyAsOf(ctx, conn, lottoHistoryWindow, asOfDrwNo)
	if err != nil {
		return nil, false, err
	}

	stats := computeNumberStats(frequency, history)
	previousDrawNumbers := latestDrawNumbers(history)
	newSet := generateRecommendationSet(mode, stats, previousDrawNumbers)
	dataHash := hashJSON(frequency)
	generatedAt := time.Now()

	if found {
		if err := reencodeLottoRecommendation(ctx, conn, cycleStart, mode, asOfDrwNo, dataHash, newSet, generatedAt); err != nil {
			return nil, false, err
		}
	} else {
		if err := insertRetroactiveLottoRecommendation(ctx, conn, cycleStart, mode, asOfDrwNo, dataHash, newSet, generatedAt); err != nil {
			return nil, false, err
		}
	}

	// insertLottoRecommendationIfAbsent와 같은 이유로, 우리의 write가
	// 경쟁에서 이겼는지와 무관하게 다시 읽는다.
	if cachedSet, _, _, _, cachedIsRetroactive, found := lookupLottoRecommendation(ctx, conn, cycleStart, mode); found && cachedSet != nil {
		return cachedSet, cachedIsRetroactive, nil
	}
	if found {
		return &newSet, cachedIsRetroactive, nil
	}
	return &newSet, true, nil
}

// computeLottoRecommendationMatchForCycle은 (cycleStart, mode)의 추천
// 세트를 확보(ensureLottoRecommendationForPastCycle)한 뒤, 이미 일치
// 결과가 계산되어 있으면 그 값을 그대로 쓰고 없으면 actualNumbers와
// 대조해 계산·저장한다. set을 구할 수 없으면(과거 데이터 자체가 없음)
// nil을 반환한다. 이 함수는 새 회차 저장 훅(처음 계산)과 GET /api/lotto
// 읽기 경로(그 뒤로는 반복 조회) 양쪽에서 호출되므로, 이미 계산된 값을
// 재사용하는 것이 매 요청/폴링마다 불필요한 UPDATE를 반복하지 않는
// 핵심이다.
func computeLottoRecommendationMatchForCycle(ctx context.Context, conn *sql.DB, cycleStart, mode string, asOfDrwNo, actualDrwNo int, actualNumbers []int) (*LottoRecommendationMatch, error) {
	set, isRetroactive, err := ensureLottoRecommendationForPastCycle(ctx, conn, cycleStart, mode, asOfDrwNo)
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, nil
	}

	matchedCount, matchedNumbers, hasMatch, err := lookupLottoRecommendationMatch(ctx, conn, cycleStart, mode)
	if err != nil {
		return nil, err
	}
	if !hasMatch {
		matchedNumbers = overlapNumbers(set.Numbers, actualNumbers)
		matchedCount = len(matchedNumbers)
		if err := updateLottoRecommendationMatchResult(ctx, conn, cycleStart, mode, matchedCount, formatMatchedNumbers(matchedNumbers)); err != nil {
			return nil, err
		}
	}

	return &LottoRecommendationMatch{
		Mode:           mode,
		Numbers:        set.Numbers,
		MatchedCount:   matchedCount,
		MatchedNumbers: matchedNumbers,
		IsRetroactive:  isRetroactive,
		ActualDrwNo:    actualDrwNo,
		ActualNumbers:  actualNumbers,
	}, nil
}

// processRetroactivePreviousCycleRecommendations는 회차 drwNo(추첨일
// drwDate)가 lotto_draws에 새로 저장된 직후 호출된다. 이 회차가 속한
// "직전 주기"(drwDate가 낀 주의 일요일 06:00 KST — 추첨은 토요일이고
// 그 다음 주 사이클이 아니라 그 전주 일요일에 시작된 사이클이 이
// 회차를 "기다리고" 있었다는 뜻이다)에 대해 trend/regression/uniform
// 3개 모드 모두의 일치 결과를 확보한다.
//
// 이 함수는 자동 수집 성공(checkForNewLottoRound, lotto.go)과 관리자
// 수동 입력(lottoManualEntryHandler, lotto_admin.go) 양쪽에서 호출된다.
// 시드 파일 로딩(seedLottoDrawsIfEmpty)에서는 의도적으로 호출하지 않는다
// — 시드는 한꺼번에 과거 회차 수십 개를 채워 넣는 것이라, 매 행마다 이
// 로직을 태우면 아무도 다시 보지 않을 수십 개의 오래된 사이클에 대한
// 사후 계산만 쌓일 뿐 의미가 없다. GET /api/lotto의 읽기 경로
// (getLottoPreviousRecommendationResult, lotto_handler.go)가 최근
// 사이클에 대해서는 어차피 같은 로직을 온디맨드로 다시 시도하므로, 이
// 훅이 어떤 이유로든 못 탔거나(예: 이 기능이 배포되기 전에 이미 있던
// 과거 회차) 실패해도 다음 조회에서 스스로 복구된다.
func processRetroactivePreviousCycleRecommendations(ctx context.Context, conn *sql.DB, drwNo int, drwDate string) {
	if drwNo <= 1 {
		return // 1회차에는 "그 이전 회차"가 없으므로 대조 대상 자체가 없다.
	}

	drawTime, err := time.ParseInLocation("2006-01-02", drwDate, kst)
	if err != nil {
		log.Printf("로또: 지난주 추천 결과 계산 건너뜀 — drwDate 파싱 실패(%q): %v", drwDate, err)
		return
	}
	cycleStart := lottoCycleStartDate(drawTime).Format("2006-01-02")

	actualNumbers, err := queryLottoDrawNumbers(ctx, conn, drwNo)
	if err != nil {
		log.Printf("로또: 지난주 추천 결과 계산 건너뜀 — %d회차 조회 실패: %v", drwNo, err)
		return
	}

	asOfDrwNo := drwNo - 1
	for _, mode := range lottoRecommendationModes {
		match, err := computeLottoRecommendationMatchForCycle(ctx, conn, cycleStart, mode, asOfDrwNo, drwNo, actualNumbers)
		if err != nil {
			log.Printf("로또: 지난주 추천(cycle=%s, mode=%s) 일치 결과 계산 실패: %v", cycleStart, mode, err)
			continue
		}
		if match == nil {
			continue
		}
		if match.IsRetroactive {
			log.Printf("로또: 지난주(cycle=%s) %s 모드를 사후 계산 — %d개 일치", cycleStart, mode, match.MatchedCount)
		} else {
			log.Printf("로또: 지난주(cycle=%s) %s 모드 일치 결과 계산 — %d개 일치", cycleStart, mode, match.MatchedCount)
		}
	}
}

// findLottoDrawClosingCycle은 cycleStart("YYYY-MM-DD", 일요일 06:00 KST가
// 시작하는 사이클)의 추천을 "마감하는" 실제 회차 — 그 사이클의 6일 뒤인
// 토요일에 추첨된 회차 — 를 찾는다. lotto_draws의 drw_date가 항상
// "YYYY-MM-DD" 형태로(insertLottoDraw 문서 주석 참고) 정확히 그 토요일
// 날짜로 저장되므로, "가장 최신 회차가 곧 이 사이클의 결과"라고 가정하지
// 않고 날짜로 정확히 짚어서 찾는다. found=false면 그 회차가 아직
// 수집되지 않았다는 뜻이다(자동 수집이 지연되는 등) — 이 경우 호출자는
// "아직 결과 없음"으로 조용히 처리해야 하며, 그 대신 다른(더 오래된)
// 회차를 결과로 써서는 절대 안 된다.
//
// 이 함수가 반드시 필요했던 이유(실제 버그): 예전
// getLottoPreviousRecommendationResult는 "지금 DB에 저장된 최신 회차가
// previousCycleStart가 기다리던 바로 그 회차"라고 그냥 가정하고
// latestDrwNo/latestNumbers를 그대로 넘겼다. 하지만 자동 수집이 새 회차를
// 아직 못 가져온 짧은 지연 구간에는 "최신 회차"가 실제로는 그 이전
// 사이클의 결과였는데도, 계산된 matched_count/matched_numbers가
// (틀린 값인 채로) DB에 영구 캐싱되어(lookupLottoRecommendationMatch가
// "이미 계산됨"으로 판단해 다시는 재계산하지 않음) 나중에 진짜 회차가
// 수집된 뒤에도 절대 스스로 고쳐지지 않는 사고로 이어졌다(실측: 회차
// 1236 수집 지연 중 GET /api/lotto가 회차 1237 사이클의 결과를 1236의
// 당첨번호와 잘못 대조해 캐싱함).
func findLottoDrawClosingCycle(ctx context.Context, conn *sql.DB, cycleStart string) (drwNo int, numbers []int, found bool, err error) {
	cycleStartDate, err := time.ParseInLocation("2006-01-02", cycleStart, kst)
	if err != nil {
		return 0, nil, false, fmt.Errorf("parse cycleStart %q: %w", cycleStart, err)
	}
	closeDate := cycleStartDate.AddDate(0, 0, 6).Format("2006-01-02")

	var n1, n2, n3, n4, n5, n6 int
	err = conn.QueryRowContext(ctx,
		`SELECT drw_no, num1, num2, num3, num4, num5, num6 FROM lotto_draws WHERE drw_date = ?`, closeDate,
	).Scan(&drwNo, &n1, &n2, &n3, &n4, &n5, &n6)
	if err == sql.ErrNoRows {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	return drwNo, []int{n1, n2, n3, n4, n5, n6}, true, nil
}

// getLottoPreviousRecommendationResult는 GET /api/lotto가 매 요청 호출하는
// 읽기 경로다. "지금" 사이클보다 정확히 한 주기 전(now.AddDate(0,0,-7)
// 기준으로 계산한 사이클)의 3개 모드 결과를 항상 trend -> regression ->
// uniform 고정 순서로 반환한다. processRetroactivePreviousCycleRecommendations와
// 똑같은 computeLottoRecommendationMatchForCycle을 그대로 재사용하므로
// (이미 계산되어 있으면 DB 조회만으로 즉시 끝난다), 그 훅이 어떤 이유로든
// 아직 못 탄 사이클(예: 이 기능이 배포되기 전의 과거 데이터)에 대해서도
// 이 요청 시점에 스스로 복구(사후 계산)한다.
//
// latestDrwNo는 오직 "아직 회차가 하나도 없다"는 초기 상태를 걸러내는
// 저렴한 가드로만 쓴다 — 실제 대조 대상 회차는 findLottoDrawClosingCycle로
// previousCycleStart에서 독립적으로 다시 찾는다(latestNumbers는 더 이상
// 쓰지 않는다). findLottoDrawClosingCycle 문서 주석 참고 — "최신 회차 =
// 이 사이클의 결과"라는 가정이 실제 버그의 원인이었다.
func getLottoPreviousRecommendationResult(ctx context.Context, conn *sql.DB, now time.Time, latestDrwNo int) []LottoRecommendationMatch {
	if latestDrwNo <= 1 {
		return nil
	}

	previousCycleStart := lottoCycleStartDate(now.AddDate(0, 0, -7)).Format("2006-01-02")

	actualDrwNo, actualNumbers, found, err := findLottoDrawClosingCycle(ctx, conn, previousCycleStart)
	if err != nil {
		log.Printf("로또: 지난주 추천 결과 조회 실패(cycle=%s) — 실제 결과 회차 조회 오류: %v", previousCycleStart, err)
		return nil
	}
	if !found {
		// 이 사이클을 마감하는 회차가 아직 수집되지 않았다 — 조용히
		// 생략한다(다음 요청 때 그 회차가 수집된 뒤 다시 시도하면 스스로
		// 채워진다).
		return nil
	}
	asOfDrwNo := actualDrwNo - 1

	results := make([]LottoRecommendationMatch, 0, len(lottoRecommendationModes))
	for _, mode := range lottoRecommendationModes {
		match, err := computeLottoRecommendationMatchForCycle(ctx, conn, previousCycleStart, mode, asOfDrwNo, actualDrwNo, actualNumbers)
		if err != nil {
			log.Printf("로또: 지난주 추천 결과 조회 실패(cycle=%s, mode=%s): %v", previousCycleStart, mode, err)
			continue
		}
		if match != nil {
			results = append(results, *match)
		}
	}
	if len(results) == 0 {
		return nil
	}
	return results
}
