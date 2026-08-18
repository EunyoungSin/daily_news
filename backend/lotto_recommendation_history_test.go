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

	// findLottoDrawClosingCycle이 날짜로 정확히 회차를 찾으므로(latestDrwNo를
	// 그대로 믿지 않는다 — 이 파일 상단의 버그 수정 참고), 회차 날짜를 실제
	// 로또처럼 토요일 7일 간격으로 정확히 맞춰서 심어야 한다. 2026-01-03은
	// 실제 토요일이다.
	drawDates := []string{"2026-01-03", "2026-01-10", "2026-01-17", "2026-01-24", "2026-01-31", "2026-02-07"}
	for i, date := range drawDates[:5] {
		n := i + 1
		seedLottoDrawForTest(t, conn, n, date, []int{n, n + 6, n + 12, n + 18, n + 24, n + 30}, 45)
	}
	actualNumbers := []int{2, 8, 14, 20, 26, 32}
	seedLottoDrawForTest(t, conn, 6, drawDates[5], actualNumbers, 45)

	// 회차 6(2026-02-07 토요일)이 마감하는 사이클은 2026-02-01(일요일)에
	// 시작한다 — now를 그 다음 주(2026-02-08~2026-02-15) 안으로 잡아야
	// now.AddDate(0,0,-7)이 2026-02-01 사이클로 계산된다.
	now := time.Date(2026, 2, 9, 12, 0, 0, 0, kst)
	results := getLottoPreviousRecommendationResult(ctx, conn, now, 6)

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

// TestGetLottoPreviousRecommendationResultUsesCorrectClosingRoundNotLatest는
// 실제 운영 DB에서 발견된 근본 버그를 재현한다: 예전 코드는
// "DB에 저장된 최신 회차 = previousCycleStart가 기다리던 바로 그 회차"라고
// 가정하고 latestNumbers를 그대로 대조에 썼는데, 자동 수집이 새 회차를
// 아직 못 가져온 지연 구간에는 이 가정이 깨진다. 이 테스트는 회차 7이
// 이미 DB에 있어도(즉 "최신 회차"가 7이어도), previousCycleStart가
// 실제로 기다리는 회차가 6이면 반드시 회차 6의 당첨번호와 대조해야
// 함을 확인한다 — 7과 잘못 대조되면 안 된다.
func TestGetLottoPreviousRecommendationResultUsesCorrectClosingRoundNotLatest(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	// 1~7회차를 실제 로또처럼 토요일 7일 간격으로 심는다.
	drawDates := []string{"2026-01-03", "2026-01-10", "2026-01-17", "2026-01-24", "2026-01-31", "2026-02-07", "2026-02-14"}
	round6Numbers := []int{2, 8, 14, 20, 26, 32}
	round7Numbers := []int{5, 11, 17, 23, 29, 35}
	for i, date := range drawDates {
		n := i + 1
		numbers := []int{n, n + 6, n + 12, n + 18, n + 24, n + 30}
		if n == 6 {
			numbers = round6Numbers
		}
		if n == 7 {
			numbers = round7Numbers
		}
		seedLottoDrawForTest(t, conn, n, date, numbers, 45)
	}

	// 회차 6(2026-02-07)이 마감하는 사이클은 2026-02-01 시작. now를 그
	// 사이클의 "지난주"로 잡으려면(now-7일이 2026-02-01 사이클에 속하려면)
	// now는 2026-02-08~2026-02-15 사이여야 한다 — 이때 DB에는 이미 회차
	// 7까지 저장되어 있다(최신 회차 = 7).
	now := time.Date(2026, 2, 9, 12, 0, 0, 0, kst)
	results := getLottoPreviousRecommendationResult(ctx, conn, now, 7)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.ActualDrwNo != 6 {
			t.Errorf("mode %s: ActualDrwNo = %d, want 6 (must match the specific round closing this cycle, not the latest round 7 in DB)", r.Mode, r.ActualDrwNo)
		}
		if !equalIntSlices(r.ActualNumbers, round6Numbers) {
			t.Errorf("mode %s: ActualNumbers = %v, want %v (round 6's own numbers, not round 7's %v)", r.Mode, r.ActualNumbers, round6Numbers, round7Numbers)
		}
	}
}

// TestGetLottoPreviousRecommendationResultReturnsNilWhenClosingRoundNotYetCollected
// 는 findLottoDrawClosingCycle이 찾는 회차가 아직 수집되지 않은 경우(자동
// 수집 지연 등) nil을 반환하고 절대 다른(더 오래된) 회차로 대체해 잘못된
// 결과를 캐싱하지 않는지 확인한다.
func TestGetLottoPreviousRecommendationResultReturnsNilWhenClosingRoundNotYetCollected(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	// 회차 1~5만 심는다(2026-01-03 ~ 2026-01-31) — 회차 6(2026-02-07)은
	// 아직 수집되지 않은 상태를 흉내낸다.
	drawDates := []string{"2026-01-03", "2026-01-10", "2026-01-17", "2026-01-24", "2026-01-31"}
	for i, date := range drawDates {
		n := i + 1
		seedLottoDrawForTest(t, conn, n, date, []int{n, n + 6, n + 12, n + 18, n + 24, n + 30}, 45)
	}

	// 회차 6이 마감할 사이클(2026-02-01 시작)의 "지난주"에 해당하는 now.
	now := time.Date(2026, 2, 9, 12, 0, 0, 0, kst)
	results := getLottoPreviousRecommendationResult(ctx, conn, now, 5)

	if results != nil {
		t.Errorf("expected nil (closing round not yet collected), got %d results", len(results))
	}
}

// TestComputeLottoRecommendationMatchForCycleMatchesReportedRealWorldCases는
// 실제로 잘못 계산되어 사용자가 보고한 두 사례를 정확한 값으로 재검증한다:
//  1. 추천 [1,3,9,20,38,39] vs 실제 [10,20,23,34,37,40] → 정답은 20 하나만
//     일치(1개)인데, 예전에는 matched_numbers가 "38"로 잘못 저장되어 있었다
//     (38은 실제로는 직전 회차 1236의 당첨번호였다 — 잘못된 회차와 대조된
//     결과가 캐싱된 것).
//  2. 추천 [10,11,16,31,32,40] vs 실제 [10,20,23,34,37,40] → 정답은 10과
//     40, 2개가 일치하는데, 예전에는 matched_count가 0으로 저장되어 있었다.
func TestComputeLottoRecommendationMatchForCycleMatchesReportedRealWorldCases(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		seedLottoDrawForTest(t, conn, i, "2026-01-0"+string(rune('0'+i)), []int{i, i + 10, i + 20, i + 24, i + 28, i + 32}, 45)
	}
	actualDrwNo := 6
	actualNumbers := []int{10, 20, 23, 34, 37, 40}
	seedLottoDrawForTest(t, conn, actualDrwNo, "2026-01-10", actualNumbers, 45)

	cases := []struct {
		name           string
		cycleStart     string
		mode           string
		recommended    []int
		wantCount      int
		wantMatchedSet []int
	}{
		{"case1_trend", "2026-01-01", lottoModeTrend, []int{1, 3, 9, 20, 38, 39}, 1, []int{20}},
		{"case2_uniform", "2026-01-02", lottoModeUniform, []int{10, 11, 16, 31, 32, 40}, 2, []int{10, 40}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cycleStart := tc.cycleStart
			set := LottoRecommendationSet{Numbers: tc.recommended, Stats: LottoRecommendationStats{BandDistribution: map[string]int{}}}
			if err := insertLottoRecommendationIfAbsent(ctx, conn, cycleStart, tc.mode, 5, "hash-"+tc.name, set, time.Now()); err != nil {
				t.Fatalf("seed recommendation: %v", err)
			}

			match, err := computeLottoRecommendationMatchForCycle(ctx, conn, cycleStart, tc.mode, 5, actualDrwNo, actualNumbers)
			if err != nil {
				t.Fatalf("compute match: %v", err)
			}
			if match.MatchedCount != tc.wantCount {
				t.Errorf("MatchedCount = %d, want %d", match.MatchedCount, tc.wantCount)
			}
			if !equalIntSlices(match.MatchedNumbers, tc.wantMatchedSet) {
				t.Errorf("MatchedNumbers = %v, want %v", match.MatchedNumbers, tc.wantMatchedSet)
			}
		})
	}
}

// TestOverlapNumbersRandomCases는 overlapNumbers가 완전히 새로운 임의의
// 추천/실제 번호 조합에서도 일반적으로 정확한지(순서·중복과 무관하게
// 값 기반 Set 교집합으로만 판단하는지) 폭넓게 검증한다.
func TestOverlapNumbersRandomCases(t *testing.T) {
	cases := []struct {
		name string
		rec  []int
		act  []int
		want []int
	}{
		{"no overlap", []int{1, 2, 3, 4, 5, 6}, []int{7, 8, 9, 10, 11, 12}, nil},
		{"all six match, different order", []int{5, 3, 45, 1, 20, 12}, []int{45, 1, 3, 12, 5, 20}, []int{1, 3, 5, 12, 20, 45}},
		{"single overlap at the boundary values", []int{1, 10, 20, 30, 40, 45}, []int{1, 11, 21, 31, 41, 44}, []int{1}},
		{"three overlaps scattered", []int{2, 9, 17, 23, 31, 44}, []int{2, 8, 17, 24, 31, 45}, []int{2, 17, 31}},
		{"overlap only via the smallest and largest numbers", []int{1, 45, 22, 23, 24, 25}, []int{1, 45, 2, 3, 4, 5}, []int{1, 45}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := overlapNumbers(tc.rec, tc.act)
			if !equalIntSlices(got, tc.want) {
				t.Errorf("overlapNumbers(%v, %v) = %v, want %v", tc.rec, tc.act, got, tc.want)
			}
		})
	}
}

// TestComputeLottoRecommendationMatchForCycleModesAreIndependent는 세
// 모드(trend/regression/uniform)가 같은 사이클 안에서도 서로 다른 추천
// 번호를 가질 때 각자 독립적으로 정확히 계산되는지 확인한다 — 한 모드의
// 계산이 다른 모드의 matched_count/matched_numbers에 영향을 주는 변수
// 재사용 버그가 없는지가 핵심이다.
func TestComputeLottoRecommendationMatchForCycleModesAreIndependent(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		seedLottoDrawForTest(t, conn, i, "2026-03-0"+string(rune('0'+i)), []int{i, i + 10, i + 20, i + 24, i + 28, i + 32}, 45)
	}
	actualNumbers := []int{3, 14, 25, 33, 40, 45}
	seedLottoDrawForTest(t, conn, 6, "2026-03-10", actualNumbers, 45)

	cycleStart := "2026-03-04"
	sets := map[string][]int{
		lottoModeTrend:      {3, 7, 11, 15, 19, 23}, // 겹침: {3} -> 1개
		lottoModeRegression: {14, 25, 33, 1, 2, 4},  // 겹침: {14,25,33} -> 3개
		lottoModeUniform:    {5, 6, 8, 9, 10, 12},   // 겹침: 없음 -> 0개
	}
	wantCounts := map[string]int{lottoModeTrend: 1, lottoModeRegression: 3, lottoModeUniform: 0}
	wantMatched := map[string][]int{
		lottoModeTrend:      {3},
		lottoModeRegression: {14, 25, 33},
		lottoModeUniform:    nil,
	}

	for mode, numbers := range sets {
		set := LottoRecommendationSet{Numbers: numbers, Stats: LottoRecommendationStats{BandDistribution: map[string]int{}}}
		if err := insertLottoRecommendationIfAbsent(ctx, conn, cycleStart, mode, 5, "hash-"+mode, set, time.Now()); err != nil {
			t.Fatalf("seed recommendation for mode %s: %v", mode, err)
		}
	}

	// 세 모드를 일부러 뒤섞인 순서로 계산해, 앞선 모드의 계산 결과가 뒤
	// 모드에 잘못 넘어가지 않는지도 함께 확인한다.
	callOrder := []string{lottoModeRegression, lottoModeUniform, lottoModeTrend}
	for _, mode := range callOrder {
		match, err := computeLottoRecommendationMatchForCycle(ctx, conn, cycleStart, mode, 5, 6, actualNumbers)
		if err != nil {
			t.Fatalf("compute match for mode %s: %v", mode, err)
		}
		if match.MatchedCount != wantCounts[mode] {
			t.Errorf("mode %s: MatchedCount = %d, want %d", mode, match.MatchedCount, wantCounts[mode])
		}
		if !equalIntSlices(match.MatchedNumbers, wantMatched[mode]) {
			t.Errorf("mode %s: MatchedNumbers = %v, want %v", mode, match.MatchedNumbers, wantMatched[mode])
		}
		if !equalIntSlices(match.Numbers, sets[mode]) {
			t.Errorf("mode %s: Numbers = %v, want %v (leaked another mode's recommended set)", mode, match.Numbers, sets[mode])
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
