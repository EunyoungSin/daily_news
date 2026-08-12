package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// openTempLottoTestDB는 briefing_section_test.go의 openTempBriefingTestDB와
// 같은 패턴이다 — 격리된 임시 SQLite/libSQL 파일 DB를 열고 전체
// 마이그레이션을 실행한다. 로또 사후 계산 기능은 실제 DB 왕복(여러
// 회차 삽입 -> 사후 계산 -> 조회)을 검증해야 순수 함수 테스트만으로는
// 잡을 수 없는 문제(SQL 오타, 컬럼 누락 등)를 잡아낼 수 있다.
func openTempLottoTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("libsql", "file:"+path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := migrate(conn); err != nil {
		t.Fatalf("migrate temp db: %v", err)
	}
	return conn
}

// seedLottoDrawForTest는 테스트에서 회차 하나를 직접 채워 넣는다 —
// upsertLottoDrawManual을 그대로 재사용해 실제 저장 경로와 동일한 SQL을
// 탄다.
func seedLottoDrawForTest(t *testing.T, conn *sql.DB, drwNo int, drwDate string, numbers []int, bonus int) {
	t.Helper()
	entry := lottoEntryInput{DrwNo: drwNo, DrwDate: drwDate, Numbers: numbers, Bonus: bonus}
	if err := upsertLottoDrawManual(context.Background(), conn, entry); err != nil {
		t.Fatalf("seed draw %d: %v", drwNo, err)
	}
}

func TestOverlapNumbers(t *testing.T) {
	cases := []struct {
		name string
		a, b []int
		want []int
	}{
		{"완전 무관", []int{1, 2, 3}, []int{4, 5, 6}, nil},
		{"일부 겹침, 순서 뒤섞임", []int{5, 1, 9, 3}, []int{3, 5, 20}, []int{3, 5}},
		{"전부 겹침", []int{1, 2, 3}, []int{3, 2, 1}, []int{1, 2, 3}},
		{"빈 슬라이스", []int{}, []int{1, 2, 3}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := overlapNumbers(tc.a, tc.b)
			if len(got) != len(tc.want) {
				t.Fatalf("overlapNumbers(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("overlapNumbers(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
					break
				}
			}
		})
	}
}

func TestFormatAndParseMatchedNumbersRoundTrip(t *testing.T) {
	cases := [][]int{
		{5, 17},
		{1, 2, 3, 4, 5, 6},
		nil,
		{9},
	}
	for _, nums := range cases {
		csv := formatMatchedNumbers(nums)
		got := parseMatchedNumbers(csv)
		if len(got) != len(nums) {
			t.Errorf("round trip %v -> %q -> %v, length mismatch", nums, csv, got)
			continue
		}
		for i := range got {
			if got[i] != nums[i] {
				t.Errorf("round trip %v -> %q -> %v", nums, csv, got)
				break
			}
		}
	}
	if got := formatMatchedNumbers(nil); got != "" {
		t.Errorf("formatMatchedNumbers(nil) = %q, want empty string", got)
	}
	if got := parseMatchedNumbers(""); got != nil {
		t.Errorf("parseMatchedNumbers(\"\") = %v, want nil", got)
	}
}

// TestProcessRetroactivePreviousCycleRecommendationsGeneratesAllThreeModes는
// 이번 기능의 핵심 시나리오를 검증한다: 사용자가 지난주 uniform 모드
// 하나만 조회했어도(나머지 두 모드는 DB에 행 자체가 없어도), 새 회차가
// 저장되면 trend/regression/uniform 3개 모드 모두에 대해 일치 결과가
// 계산되어야 한다.
func TestProcessRetroactivePreviousCycleRecommendationsGeneratesAllThreeModes(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	// 최근 50회 집계에 쓰일 과거 회차 몇 개를 채워둔다(회차 1~5).
	for i := 1; i <= 5; i++ {
		seedLottoDrawForTest(t, conn, i, "2026-01-0"+string(rune('0'+i)), []int{i, i + 6, i + 12, i + 18, i + 24, i + 30}, 45)
	}

	// 사용자가 "지난주"(회차 6이 나오기 전 사이클) 동안 uniform 모드만
	// 실제로 조회했다고 가정하고, 그 세트를 직접 심어둔다 — 알려진 번호로
	// 심어서 matched_count를 정확히 예측할 수 있게 한다.
	previousCycleStart := "2026-01-04" // 회차 5(2026-01-05, 월요일)가 속한 사이클의 일요일
	knownUniformSet := LottoRecommendationSet{
		Numbers: []int{1, 2, 3, 20, 21, 22},
		Stats:   LottoRecommendationStats{OddEvenRatio: "3:3", Sum: 69, BandDistribution: map[string]int{}, OverlapWithPrevious: 0},
	}
	if err := insertLottoRecommendationIfAbsent(ctx, conn, previousCycleStart, lottoModeUniform, 5, "seed-hash", knownUniformSet, time.Now()); err != nil {
		t.Fatalf("seed uniform recommendation: %v", err)
	}

	// 실제 당첨번호(회차 6)가 새로 저장됐다 — uniform 추천(1,2,3,20,21,22)과
	// 정확히 {1,2,20}이 겹치도록 구성한다.
	actualNumbers := []int{1, 2, 20, 40, 41, 42}
	seedLottoDrawForTest(t, conn, 6, "2026-01-10", actualNumbers, 45)

	processRetroactivePreviousCycleRecommendations(ctx, conn, 6, "2026-01-10")

	for _, mode := range lottoRecommendationModes {
		row, found, err := queryLottoRecommendationRowForTest(ctx, conn, previousCycleStart, mode)
		if err != nil {
			t.Fatalf("query row for mode %s: %v", mode, err)
		}
		if !found {
			t.Fatalf("expected a lotto_recommendation row for mode %s, found none", mode)
		}

		if mode == lottoModeUniform {
			if row.isRetroactive {
				t.Errorf("uniform mode was pre-seeded by a real user view, expected is_retroactive=false, got true")
			}
			if row.matchedCount != 3 {
				t.Errorf("uniform matchedCount = %d, want 3 (numbers 1,2,20 overlap)", row.matchedCount)
			}
			wantNums := []int{1, 2, 20}
			if !equalIntSlices(parseMatchedNumbers(row.matchedNumbers), wantNums) {
				t.Errorf("uniform matchedNumbers = %q, want %v", row.matchedNumbers, wantNums)
			}
		} else {
			if !row.isRetroactive {
				t.Errorf("mode %s was never viewed by a user, expected is_retroactive=true, got false", mode)
			}
			if row.matchedCount < 0 || row.matchedCount > 6 {
				t.Errorf("mode %s matchedCount = %d, out of valid range [0,6]", mode, row.matchedCount)
			}
			matched := parseMatchedNumbers(row.matchedNumbers)
			if len(matched) != row.matchedCount {
				t.Errorf("mode %s: len(matchedNumbers)=%d does not match matchedCount=%d", mode, len(matched), row.matchedCount)
			}
			// 정확성 불변식: matched로 보고된 각 번호는 실제로 추천 세트와
			// 당첨번호 양쪽 모두에 들어있어야 한다.
			recommended := parseNumbersJSONForTest(t, row.numbersJSON)
			for _, n := range matched {
				if !containsInt(recommended, n) {
					t.Errorf("mode %s: matched number %d is not in the recommended set %v", mode, n, recommended)
				}
				if !containsInt(actualNumbers, n) {
					t.Errorf("mode %s: matched number %d is not in the actual draw numbers %v", mode, n, actualNumbers)
				}
			}
		}
	}
}

// TestGetLottoPreviousRecommendationResultReturnsFixedOrder는 반환되는
// 배열이 항상 trend -> regression -> uniform 고정 순서이며, 일치 개수로
// 재정렬되지 않는지 확인한다.
func TestGetLottoPreviousRecommendationResultReturnsFixedOrder(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		seedLottoDrawForTest(t, conn, i, "2026-01-0"+string(rune('0'+i)), []int{i, i + 6, i + 12, i + 18, i + 24, i + 30}, 45)
	}
	actualNumbers := []int{2, 8, 14, 20, 26, 32}
	seedLottoDrawForTest(t, conn, 6, "2026-01-10", actualNumbers, 45)

	now := time.Date(2026, 1, 11, 12, 0, 0, 0, kst) // 회차 6이 나온 뒤의 "지금"
	results := getLottoPreviousRecommendationResult(ctx, conn, now, 6, actualNumbers)

	if len(results) != 3 {
		t.Fatalf("expected 3 results (trend, regression, uniform), got %d", len(results))
	}
	wantOrder := []string{lottoModeTrend, lottoModeRegression, lottoModeUniform}
	for i, want := range wantOrder {
		if results[i].Mode != want {
			t.Errorf("results[%d].Mode = %q, want %q (order must always be trend -> regression -> uniform, never sorted by matchedCount)", i, results[i].Mode, want)
		}
		if results[i].ActualDrwNo != 6 {
			t.Errorf("results[%d].ActualDrwNo = %d, want 6", i, results[i].ActualDrwNo)
		}
		if !equalIntSlices(results[i].ActualNumbers, actualNumbers) {
			t.Errorf("results[%d].ActualNumbers = %v, want %v", i, results[i].ActualNumbers, actualNumbers)
		}
	}
}

// TestComputeLottoRecommendationMatchForCycleReusesStoredResult는 이미
// matched_count가 계산되어 있으면 재계산(및 재저장) 없이 그 값을 그대로
// 재사용하는지 확인한다 — GET /api/lotto가 반복 폴링될 때마다 매번 같은
// UPDATE를 되풀이하지 않기 위한 핵심 동작이다.
func TestComputeLottoRecommendationMatchForCycleReusesStoredResult(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		seedLottoDrawForTest(t, conn, i, "2026-01-0"+string(rune('0'+i)), []int{i, i + 10, i + 20, i + 24, i + 28, i + 32}, 45)
	}

	cycleStart := "2026-01-04"
	set := LottoRecommendationSet{Numbers: []int{1, 2, 3, 4, 5, 6}, Stats: LottoRecommendationStats{BandDistribution: map[string]int{}}}
	if err := insertLottoRecommendationIfAbsent(ctx, conn, cycleStart, lottoModeUniform, 3, "hash", set, time.Now()); err != nil {
		t.Fatalf("seed recommendation: %v", err)
	}

	first, err := computeLottoRecommendationMatchForCycle(ctx, conn, cycleStart, lottoModeUniform, 3, 4, []int{1, 2, 100, 101, 102, 103})
	if err != nil {
		t.Fatalf("first compute: %v", err)
	}
	if first.MatchedCount != 2 {
		t.Fatalf("first MatchedCount = %d, want 2", first.MatchedCount)
	}

	// 두 번째 호출은 이제 다른(가짜) 실제 번호를 넘겨도, 이미 저장된 첫
	// 번째 계산 결과를 그대로 반환해야 한다 — 매번 다시 계산한다면 여기서
	// 값이 바뀌어버릴 것이다.
	second, err := computeLottoRecommendationMatchForCycle(ctx, conn, cycleStart, lottoModeUniform, 3, 4, []int{1, 2, 3, 4, 5, 6})
	if err != nil {
		t.Fatalf("second compute: %v", err)
	}
	if second.MatchedCount != 2 {
		t.Errorf("second MatchedCount = %d, want 2 (should reuse the stored result, not recompute with the new actualNumbers)", second.MatchedCount)
	}
}

// TestEnsureLottoRecommendationForPastCycleSelfHealsLegacyCSVFormat는 실제
// 운영 DB에서 발견된 버그를 재현한다: JSON 인코딩이 도입되기 전 저장된
// 레거시 행(numbers가 "1,5,8,20,21,30" 같은 순수 CSV)은
// decodeRecommendationSet이 실패해 lookupLottoRecommendation이
// found=true, set=nil을 반환한다. 이전 코드는 이 경우를 "행이 없음"과
// 똑같이 취급해 insertRetroactiveLottoRecommendation(INSERT OR IGNORE)을
// 호출했는데, 행이 이미 있으니 PK 충돌로 조용히 무시되어 아무것도 고쳐지지
// 않고, 매 호출마다 새로 계산한 세트를 반환해 uniform 모드처럼 무작위성이
// 있는 모드는 호출할 때마다 다른 번호가 나오는 버그로 이어졌다. 또한
// is_retroactive를 항상 true로 덮어써 실제로는 사용자가 조회했던
// (is_retroactive=false) 행의 출처 정보를 잃어버렸다.
//
// 이 테스트는 (1) 반복 호출해도 결과가 안정적인지(reencode로 실제 UPDATE가
// 일어나는지), (2) 원래 is_retroactive=false였다면 그대로 보존되는지,
// (3) numbers가 바뀌었으니 matched_count/matched_numbers도 새 numbers
// 기준으로 다시 계산되는지를 확인한다.
func TestEnsureLottoRecommendationForPastCycleSelfHealsLegacyCSVFormat(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		seedLottoDrawForTest(t, conn, i, "2026-01-0"+string(rune('0'+i)), []int{i, i + 6, i + 12, i + 18, i + 24, i + 30}, 45)
	}

	cycleStart := "2026-01-04"
	// JSON 인코딩 이전 포맷을 그대로 재현: numbers가 순수 CSV, 이 행은
	// 실제 사용자가 조회해서 생긴 것이라 is_retroactive=0이다. 인코딩되지
	// 않은 stale matched_count/matched_numbers도 함께 심어서, reencode 후
	// 이 값들이 NULL로 리셋되고 새 numbers 기준으로 재계산되는지 확인한다.
	_, err := conn.ExecContext(ctx, `
		INSERT INTO lotto_recommendation
			(cycle_start_date, mode, based_on_drw_no, based_on_data_hash, numbers, number_groups, stats_json, generated_at, matched_count, matched_numbers, is_retroactive)
		VALUES (?, ?, 5, 'legacy-hash', '1,5,8,20,21,30', '', '{}', ?, 99, '1,2,3', 0)`,
		cycleStart, lottoModeUniform, time.Now(),
	)
	if err != nil {
		t.Fatalf("seed legacy CSV row: %v", err)
	}

	set1, isRetroactive1, err := ensureLottoRecommendationForPastCycle(ctx, conn, cycleStart, lottoModeUniform, 5)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if set1 == nil {
		t.Fatal("first ensure returned nil set")
	}
	if isRetroactive1 {
		t.Error("legacy row was a real user view (is_retroactive=0), expected self-heal to preserve isRetroactive=false, got true")
	}

	set2, isRetroactive2, err := ensureLottoRecommendationForPastCycle(ctx, conn, cycleStart, lottoModeUniform, 5)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if !equalIntSlices(set1.Numbers, set2.Numbers) {
		t.Errorf("numbers changed between calls: %v vs %v — self-heal (UPDATE) did not persist, still regenerating randomly each call", set1.Numbers, set2.Numbers)
	}
	if isRetroactive2 {
		t.Error("second ensure: expected isRetroactive=false to remain preserved, got true")
	}

	// matched_count/matched_numbers는 numbers가 바뀌었으므로 리셋되어
	// 새 numbers 기준으로 재계산되어야 한다 — 이전 값(99, "1,2,3")이 그대로
	// 남아 있으면 안 된다.
	actualNumbers := []int{2, 8, 14, 20, 26, 32}
	seedLottoDrawForTest(t, conn, 6, "2026-01-10", actualNumbers, 45)
	match, err := computeLottoRecommendationMatchForCycle(ctx, conn, cycleStart, lottoModeUniform, 5, 6, actualNumbers)
	if err != nil {
		t.Fatalf("compute match: %v", err)
	}
	if match.MatchedCount == 99 {
		t.Fatal("stale matched_count (99) from before self-heal was reused instead of being reset and recomputed")
	}
	wantMatched := overlapNumbers(set1.Numbers, actualNumbers)
	if !equalIntSlices(match.MatchedNumbers, wantMatched) {
		t.Errorf("matchedNumbers = %v, want %v (recomputed against the self-healed numbers, not the stale 1,2,3)", match.MatchedNumbers, wantMatched)
	}
	if match.MatchedCount != len(wantMatched) {
		t.Errorf("MatchedCount = %d, want %d", match.MatchedCount, len(wantMatched))
	}
}

// --- 테스트 전용 헬퍼: lotto_recommendation 행을 직접 조회 ---

type lottoRecommendationRowForTest struct {
	numbersJSON    string
	matchedCount   int
	matchedNumbers string
	isRetroactive  bool
}

func queryLottoRecommendationRowForTest(ctx context.Context, conn *sql.DB, cycleStartDate, mode string) (lottoRecommendationRowForTest, bool, error) {
	var row lottoRecommendationRowForTest
	var matchedCount sql.NullInt64
	var matchedNumbers sql.NullString
	err := conn.QueryRowContext(ctx,
		`SELECT numbers, matched_count, matched_numbers, is_retroactive FROM lotto_recommendation WHERE cycle_start_date = ? AND mode = ?`,
		cycleStartDate, mode,
	).Scan(&row.numbersJSON, &matchedCount, &matchedNumbers, &row.isRetroactive)
	if err == sql.ErrNoRows {
		return lottoRecommendationRowForTest{}, false, nil
	}
	if err != nil {
		return lottoRecommendationRowForTest{}, false, err
	}
	if matchedCount.Valid {
		row.matchedCount = int(matchedCount.Int64)
	}
	row.matchedNumbers = matchedNumbers.String
	return row, true, nil
}

func parseNumbersJSONForTest(t *testing.T, numbersJSON string) []int {
	t.Helper()
	decoded, err := decodeRecommendationSet(numbersJSON, "{}")
	if err != nil {
		t.Fatalf("decode numbers JSON %q: %v", numbersJSON, err)
	}
	return decoded.Numbers
}

func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	sortedA := append([]int{}, a...)
	sortedB := append([]int{}, b...)
	sort.Ints(sortedA)
	sort.Ints(sortedB)
	for i := range sortedA {
		if sortedA[i] != sortedB[i] {
			return false
		}
	}
	return true
}

func containsInt(nums []int, target int) bool {
	for _, n := range nums {
		if n == target {
			return true
		}
	}
	return false
}
