package main

import (
	"bytes"
	"log"
	"os"
	"sort"
	"testing"
)

// ---------- 1단계: 빈도 집계 ----------

func TestComputeNumberStats(t *testing.T) {
	// history[0]이 가장 최근 회차다. 1번은 가장 최근 회차(gap=0)에,
	// 2번은 그 전전 회차(gap=2)에 마지막으로 나오고, 45번은 history
	// 전체에 한 번도 안 나온다(gap=len(history)).
	history := []LottoDraw{
		{DrwNo: 3, Numbers: []int{1, 3, 5, 7, 9, 11}},
		{DrwNo: 2, Numbers: []int{3, 5, 7, 9, 11, 13}},
		{DrwNo: 1, Numbers: []int{2, 3, 5, 7, 9, 11}},
	}
	frequency := map[int]int{1: 1, 2: 1, 3: 3}

	stats := computeNumberStats(frequency, history)

	if got := stats[1].Gap; got != 0 {
		t.Errorf("stats[1].Gap = %d, want 0 (appears in most recent draw)", got)
	}
	if got := stats[2].Gap; got != 2 {
		t.Errorf("stats[2].Gap = %d, want 2 (last seen 2 draws ago)", got)
	}
	if got := stats[45].Gap; got != len(history) {
		t.Errorf("stats[45].Gap = %d, want %d (never appeared)", got, len(history))
	}
	if got := stats[3].Count; got != 3 {
		t.Errorf("stats[3].Count = %d, want 3", got)
	}
}

func TestTopNByCountAndGap(t *testing.T) {
	frequency := make(map[int]int, 45)
	for n := 1; n <= 45; n++ {
		frequency[n] = 45 - n // 1번이 가장 빈도 높고 45번이 가장 낮음
	}
	// history가 비어있으면 모든 번호의 gap이 동일(0)해지므로, gap 차이를
	// 보려면 실제 등장 이력이 있어야 한다 — 45번만 최근 회차에 나오게 해서
	// gap이 가장 작고(0), 1번은 전혀 안 나와 gap이 가장 크게(=len(history))
	// 만든다.
	history := []LottoDraw{{DrwNo: 1, Numbers: []int{45, 44, 43, 42, 41, 40}}}
	stats := computeNumberStats(frequency, history)

	hot := topNByCount(stats, 5)
	wantHot := []int{1, 2, 3, 4, 5}
	if !sameInts(hot, wantHot) {
		t.Errorf("topNByCount(5) = %v, want %v", hot, wantHot)
	}

	cold := topNByGap(stats, 5)
	// 45,44,43,42,41,40은 gap=0이라 후보에서 제외된다. 나머지 1~39는
	// 전부 gap=len(history)=1로 동점이라, 동점 처리 규칙(번호 오름차순)에
	// 따라 1~5가 뽑힌다.
	wantCold := []int{1, 2, 3, 4, 5}
	if !sameInts(cold, wantCold) {
		t.Errorf("topNByGap(5) = %v, want %v", cold, wantCold)
	}
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------- 2단계: 가중치 정책 ----------

func TestUniformWeightsAreAllEqual(t *testing.T) {
	weights := uniformWeights()
	if len(weights) != 45 {
		t.Fatalf("expected 45 weights, got %d", len(weights))
	}
	for n := 1; n <= 45; n++ {
		if weights[n] != 1 {
			t.Errorf("uniformWeights()[%d] = %v, want 1", n, weights[n])
		}
	}
}

func TestTrendWeightsBoostHotNumbers(t *testing.T) {
	frequency := make(map[int]int, 45)
	for n := 1; n <= 45; n++ {
		frequency[n] = 45 - n
	}
	stats := computeNumberStats(frequency, nil)
	weights := trendWeights(stats)

	// 1번(빈도 최상위)은 boost를 받아야 하고, 45번(빈도 최하위)은 기본값
	// 그대로여야 한다.
	if weights[1] != lottoHotColdWeightBoost {
		t.Errorf("weights[1] = %v, want boosted %v", weights[1], lottoHotColdWeightBoost)
	}
	if weights[45] != 1 {
		t.Errorf("weights[45] = %v, want baseline 1", weights[45])
	}
}

func TestRegressionWeightsBoostColdNumbers(t *testing.T) {
	// computeNumberStats를 거치지 않고 Gap을 직접 지정한다 — 실제 history로
	// gap을 만들면, "한 번도 안 나온" 번호들이 전부 len(history)로 동점이
	// 되어 어느 번호가 boosted 12개에 드는지가 동점 처리 규칙(번호
	// 오름차순)에 좌우된다. 여기서는 그 동점 문제와 무관하게 "gap이 큰
	// 번호가 boost를 받는다"는 정책 자체만 검증하기 위해 번호마다 서로
	// 다른 gap(=자기 자신의 값)을 직접 부여한다 — 45번이 가장 큰 gap을
	// 갖고, 1번이 가장 작은 gap을 갖는다.
	stats := make(map[int]*lottoNumberStat, 45)
	for n := 1; n <= 45; n++ {
		stats[n] = &lottoNumberStat{Number: n, Count: 1, Gap: n}
	}
	weights := regressionWeights(stats)

	if weights[1] != 1 {
		t.Errorf("weights[1] (smallest gap) = %v, want baseline 1", weights[1])
	}
	if weights[45] != lottoHotColdWeightBoost {
		t.Errorf("weights[45] (largest gap) = %v, want boosted %v", weights[45], lottoHotColdWeightBoost)
	}
}

func TestWeightedSampleWithoutReplacementRespectsZeroWeight(t *testing.T) {
	pool := make([]int, 45)
	for n := 1; n <= 45; n++ {
		pool[n-1] = n
	}
	weights := make(map[int]float64, 45)
	for n := 1; n <= 45; n++ {
		weights[n] = 0
	}
	allowed := []int{1, 2, 3, 4, 5, 6}
	for _, n := range allowed {
		weights[n] = 1
	}

	for trial := 0; trial < 50; trial++ {
		result := weightedSampleWithoutReplacement(pool, weights, 6)
		if len(result) != 6 {
			t.Fatalf("expected 6 numbers, got %d", len(result))
		}
		seen := make(map[int]bool, 6)
		for _, n := range result {
			if weights[n] == 0 {
				t.Fatalf("sampled a zero-weight number %d", n)
			}
			if seen[n] {
				t.Fatalf("duplicate number %d in sample", n)
			}
			seen[n] = true
		}
	}
}

// ---------- 3단계: 패턴 필터 검증 ----------

func TestCheckOddEvenRatio(t *testing.T) {
	cases := []struct {
		name    string
		numbers []int
		want    bool
	}{
		{"3 odd 3 even passes", []int{1, 2, 3, 4, 5, 6}, true},
		{"2 odd 4 even passes (boundary)", []int{1, 3, 2, 4, 6, 8}, true},
		{"4 odd 2 even passes (boundary)", []int{1, 3, 5, 7, 2, 4}, true},
		{"1 odd 5 even fails", []int{1, 2, 4, 6, 8, 10}, false},
		{"5 odd 1 even fails", []int{1, 3, 5, 7, 9, 2}, false},
		{"all even fails", []int{2, 4, 6, 8, 10, 12}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checkOddEvenRatio(c.numbers); got != c.want {
				t.Errorf("checkOddEvenRatio(%v) = %v, want %v", c.numbers, got, c.want)
			}
		})
	}
}

func TestCheckBandDistribution(t *testing.T) {
	cases := []struct {
		name    string
		numbers []int
		want    bool
	}{
		{"spread across bands passes", []int{5, 15, 25, 35, 42, 44}, true},
		{"exactly 3 in one band passes", []int{1, 2, 3, 15, 25, 35}, true},
		{"4 in one band fails", []int{1, 2, 3, 4, 25, 35}, false},
		{"all 6 in one band fails", []int{40, 41, 42, 43, 44, 45}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checkBandDistribution(c.numbers); got != c.want {
				t.Errorf("checkBandDistribution(%v) = %v, want %v", c.numbers, got, c.want)
			}
		})
	}
}

func TestCheckSumRange(t *testing.T) {
	if !checkSumRange([]int{5, 10, 15, 20, 25, 25}) { // sum=100
		t.Error("expected sum=100 to pass (lower boundary)")
	}
	if !checkSumRange([]int{20, 25, 30, 30, 30, 35}) { // sum=170
		t.Error("expected sum=170 to pass (upper boundary)")
	}
	if checkSumRange([]int{1, 2, 3, 4, 5, 6}) { // sum=21
		t.Error("expected a very low sum to fail")
	}
	if checkSumRange([]int{40, 41, 42, 43, 44, 45}) { // sum=255
		t.Error("expected a very high sum to fail")
	}
}

func TestCheckNoTripleConsecutive(t *testing.T) {
	cases := []struct {
		name    string
		numbers []int
		want    bool
	}{
		{"no consecutive passes", []int{1, 5, 10, 20, 30, 40}, true},
		{"exactly 2 consecutive passes", []int{1, 2, 10, 20, 30, 40}, true},
		{"two separate pairs passes", []int{1, 2, 10, 20, 30, 31}, true},
		{"3 consecutive fails", []int{1, 2, 3, 20, 30, 40}, false},
		{"consecutive run in the middle fails", []int{1, 10, 20, 21, 22, 40}, false},
		{"5 consecutive fails", []int{1, 2, 3, 4, 5, 40}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sorted := append([]int(nil), c.numbers...)
			sort.Ints(sorted)
			if got := checkNoTripleConsecutive(sorted); got != c.want {
				t.Errorf("checkNoTripleConsecutive(%v) = %v, want %v", sorted, got, c.want)
			}
		})
	}
}

func TestCheckPreviousDrawOverlap(t *testing.T) {
	previous := []int{1, 2, 3, 4, 5, 6}
	cases := []struct {
		name    string
		numbers []int
		want    bool
	}{
		{"no overlap passes", []int{7, 8, 9, 10, 11, 12}, true},
		{"1 overlap passes (boundary)", []int{1, 8, 9, 10, 11, 12}, true},
		{"2 overlap fails", []int{1, 2, 9, 10, 11, 12}, false},
		{"identical set fails", []int{1, 2, 3, 4, 5, 6}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checkPreviousDrawOverlap(c.numbers, previous); got != c.want {
				t.Errorf("checkPreviousDrawOverlap(%v, %v) = %v, want %v", c.numbers, previous, got, c.want)
			}
		})
	}
}

// ---------- 4단계: 결과 출력 / 전체 파이프라인 ----------

func TestComputeRecommendationStats(t *testing.T) {
	stats := computeRecommendationStats([]int{1, 2, 15, 22, 30, 44}, []int{1, 9, 19, 29, 39, 45})

	if stats.OddEvenRatio != "2:4" {
		t.Errorf("OddEvenRatio = %q, want %q", stats.OddEvenRatio, "2:4")
	}
	if stats.Sum != 114 {
		t.Errorf("Sum = %d, want 114", stats.Sum)
	}
	wantBands := map[string]int{"1-9": 2, "10-19": 1, "20-29": 1, "30-39": 1, "40-45": 1}
	for band, want := range wantBands {
		if stats.BandDistribution[band] != want {
			t.Errorf("BandDistribution[%q] = %d, want %d", band, stats.BandDistribution[band], want)
		}
	}
	if stats.OverlapWithPrevious != 1 {
		t.Errorf("OverlapWithPrevious = %d, want 1 (only 1 overlaps)", stats.OverlapWithPrevious)
	}
}

// TestGenerateRecommendationSetPassesAllFilters는 세 모드 각각에서 실제로
// 생성된 세트가 3단계의 다섯 필터를 모두 만족하는지 여러 번 반복해
// 확인한다 — generateRecommendationSet 자체가 필터를 통과할 때까지
// 재추출하므로, 반환된 세트는 (최대 재시도 소진이라는 예외적 상황이
// 아닌 한) 항상 필터를 통과해야 한다.
func TestGenerateRecommendationSetPassesAllFilters(t *testing.T) {
	frequency := make(map[int]int, 45)
	for n := 1; n <= 45; n++ {
		frequency[n] = n % 7
	}
	history := []LottoDraw{
		{DrwNo: 10, Numbers: []int{2, 9, 16, 23, 30, 37}},
		{DrwNo: 9, Numbers: []int{1, 8, 15, 22, 29, 36}},
	}
	stats := computeNumberStats(frequency, history)
	previousDrawNumbers := latestDrawNumbers(history)

	for _, mode := range []string{lottoModeTrend, lottoModeRegression, lottoModeUniform} {
		t.Run(mode, func(t *testing.T) {
			for trial := 0; trial < 30; trial++ {
				set := generateRecommendationSet(mode, stats, previousDrawNumbers)

				if len(set.Numbers) != 6 {
					t.Fatalf("mode=%s: expected 6 numbers, got %d (%v)", mode, len(set.Numbers), set.Numbers)
				}
				seen := make(map[int]bool, 6)
				for _, n := range set.Numbers {
					if n < 1 || n > 45 {
						t.Errorf("mode=%s: number %d out of range 1-45", mode, n)
					}
					if seen[n] {
						t.Errorf("mode=%s: duplicate number %d in %v", mode, n, set.Numbers)
					}
					seen[n] = true
				}
				if !sort.IntsAreSorted(set.Numbers) {
					t.Errorf("mode=%s: numbers %v not sorted ascending", mode, set.Numbers)
				}
				if !passesAllRecommendationFilters(set.Numbers, previousDrawNumbers) {
					t.Errorf("mode=%s: generated set %v fails pattern filters (stats=%+v)", mode, set.Numbers, set.Stats)
				}
			}
		})
	}
}

// TestGenerateRecommendationSetExhaustsRetriesAndLogs는 3단계 필터를 영원히
// 통과할 수 없는 상황(가중치를 조작해 후보를 단 6개 — 서로 연속된
// 1~6 — 로 고정)을 강제로 만들어, 최대 재시도(lottoRecommendationMaxAttempts)를
// 다 쓰고도 실패해 마지막 후보를 그대로 반환하면서 경고 로그를 남기는지
// 확인한다.
func TestGenerateRecommendationSetExhaustsRetriesAndLogs(t *testing.T) {
	weights := make(map[int]float64, 45)
	for n := 1; n <= 45; n++ {
		weights[n] = 0
	}
	// 1~6은 세 개 이상 연속되고(checkNoTripleConsecutive 위반) 합도
	// 21로 100 미만이라(checkSumRange 위반) 어떤 순서로 뽑히든 항상
	// 필터를 통과하지 못한다. 가중치 0인 다른 39개 번호는 실질적으로
	// 뽑힐 수 없으므로, 이 6개만 계속 뽑히는 상황이 결정적으로 재현된다.
	for n := 1; n <= 6; n++ {
		weights[n] = 1
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	set := generateRecommendationSetWithWeights("test-mode", weights, nil)

	wantNumbers := []int{1, 2, 3, 4, 5, 6}
	if !sameInts(set.Numbers, wantNumbers) {
		t.Errorf("Numbers = %v, want %v", set.Numbers, wantNumbers)
	}
	if !bytes.Contains(buf.Bytes(), []byte("재추출해도 만족하는 조합을 찾지 못해")) {
		t.Errorf("expected a max-retries warning to be logged, got log output: %q", buf.String())
	}
}
