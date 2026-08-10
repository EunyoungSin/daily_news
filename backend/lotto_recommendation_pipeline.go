package main

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
)

// 이 파일은 "이번 주 추천 번호"의 4단계 번호 생성 파이프라인이다:
//   1단계(빈도 집계)      -> computeNumberStats, topNByCount, topNByGap
//   2단계(가중치 정책)    -> trendWeights/regressionWeights/uniformWeights
//   3단계(패턴 필터 검증) -> checkXxx 함수들 + generateRecommendationSet의 재추출 루프
//   4단계(결과 출력)      -> computeRecommendationStats
//
// lotto_recommendation.go(주기별 캐싱, 판매 마감 블랙아웃 처리)는 이 중
// generateRecommendationSet 하나만 호출한다 — 캐싱/블랙아웃 로직 자체는
// 이 교체와 무관하게 그대로다. 세트는 (사이클, 모드)당 정확히 1개만
// 생성·캐싱한다.
//
// 아래 모든 가중치는 과거 출현 패턴을 재미로 반영한 것일 뿐, 실제 당첨
// 확률은 매 회차 독립적으로 균등하다 — 추첨 기계는 지난 회차를 기억하지
// 못한다. trend/regression 모드는 그 사실과 무관하게 "그럴듯해 보이는"
// 조합을 만들어주는 오락적 장치일 뿐이다.

// ---------- 1단계: 빈도 집계 ----------

// lottoHotColdCount는 빈도 상위/하위 몇 개를 "핫넘버"/"콜드넘버"로 볼지
// 정하는 N이다. 너무 작으면(예: 3) 2단계의 가중 추출이 항상 똑같은
// 소수의 번호만 우대해 세트 5개가 서로 비슷해지고, 너무 크면(예: 30)
// 핫/콜드 구분 자체가 무의미해진다 — 45개 중 10~15개 정도가 "상위권
// 다양성"과 "구분력"의 합리적인 균형점이다.
const lottoHotColdCount = 12

// lottoNumberStat은 1단계에서 계산하는 번호 1개의 통계다.
type lottoNumberStat struct {
	Number int
	Count  int // 최근 lottoHistoryWindow 회차 안에서의 출현 횟수
	Gap    int // 마지막 출현 이후 지난 회차 수. 0이면 가장 최근 회차에도 나옴
}

// computeNumberStats는 frequency(번호별 출현 횟수)와 history(최신순으로
// 정렬된 최근 회차 목록)로부터 1~45 전체의 통계를 계산한다. Gap은
// history를 최근 회차부터 순서대로 훑어, 각 번호가 처음(=가장 최근)
// 등장하는 인덱스를 찾는 방식으로 구한다 — history 전체에 한 번도 안
// 나온 번호는 len(history)를 그대로 Gap으로 쓴다("관측 범위 안에서는
// 계속 안 나왔다"는 뜻이며, 동시에 그 어떤 실제 인덱스보다 큰 값이라
// topNByGap 정렬에서 자연스럽게 가장 "오래 안 나온" 취급을 받는다).
func computeNumberStats(frequency map[int]int, history []LottoDraw) map[int]*lottoNumberStat {
	stats := make(map[int]*lottoNumberStat, 45)
	for n := 1; n <= 45; n++ {
		stats[n] = &lottoNumberStat{Number: n, Count: frequency[n], Gap: len(history)}
	}
	for idx, draw := range history {
		for _, n := range draw.Numbers {
			if s, ok := stats[n]; ok && s.Gap == len(history) {
				s.Gap = idx
			}
		}
	}
	return stats
}

func statsSlice(stats map[int]*lottoNumberStat) []*lottoNumberStat {
	all := make([]*lottoNumberStat, 0, len(stats))
	for _, s := range stats {
		all = append(all, s)
	}
	return all
}

func numbersOf(stats []*lottoNumberStat) []int {
	nums := make([]int, len(stats))
	for i, s := range stats {
		nums[i] = s.Number
	}
	return nums
}

// topNByCount는 출현 횟수(count) 내림차순 상위 n개 번호("핫넘버")를
// 반환한다. 동점일 때는 번호 오름차순으로 정렬해 결과를 결정적으로
// 만든다 — 이 동점 처리 자체에는 통계적 의미가 없다.
func topNByCount(stats map[int]*lottoNumberStat, n int) []int {
	all := statsSlice(stats)
	sort.Slice(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return all[i].Number < all[j].Number
	})
	if n > len(all) {
		n = len(all)
	}
	return numbersOf(all[:n])
}

// topNByGap은 미출현 기간(gap) 내림차순 상위 n개 번호("콜드넘버")를
// 반환한다.
func topNByGap(stats map[int]*lottoNumberStat, n int) []int {
	all := statsSlice(stats)
	sort.Slice(all, func(i, j int) bool {
		if all[i].Gap != all[j].Gap {
			return all[i].Gap > all[j].Gap
		}
		return all[i].Number < all[j].Number
	})
	if n > len(all) {
		n = len(all)
	}
	return numbersOf(all[:n])
}

// ---------- 2단계: 가중치 정책 선택 ----------

const (
	lottoModeTrend      = "trend"
	lottoModeRegression = "regression"
	lottoModeUniform    = "uniform"
)

func isValidLottoMode(mode string) bool {
	return mode == lottoModeTrend || mode == lottoModeRegression || mode == lottoModeUniform
}

// lottoHotColdWeightBoost는 trend/regression 모드에서 핫/콜드 넘버가
// 그 외 번호보다 몇 배 더 뽑히기 쉬운지를 정한다. 3배는 "눈에 띄게
// 우대하지만 그 외 번호도 여전히 자주 뽑히는" 정도의 균형값이다 — 너무
// 크면(예: 20배) 사실상 핫/콜드 12개 중에서만 뽑는 것과 다름없어져
// "가중치를 준다"보다 "후보를 제한한다"에 가까워진다.
const lottoHotColdWeightBoost = 3.0

// trendWeights: "빈도 높은 번호에 가중치 부여(핫넘버 우선)" 정책.
func trendWeights(stats map[int]*lottoNumberStat) map[int]float64 {
	return boostedWeights(topNByCount(stats, lottoHotColdCount))
}

// regressionWeights: "미출현 기간이 긴 번호에 가중치 부여(콜드넘버
// 우선)" 정책. "그동안 안 나왔으니 나올 때가 됐다"는 속설(도박사의
// 오류)을 재미로 반영한 것일 뿐, trendWeights와 마찬가지로 실제 확률에
// 영향을 주는 근거는 전혀 아니다.
func regressionWeights(stats map[int]*lottoNumberStat) map[int]float64 {
	return boostedWeights(topNByGap(stats, lottoHotColdCount))
}

// uniformWeights: 가중치를 전혀 두지 않는 기본값 — 모든 번호가 완전히
// 동일한 확률로 뽑힌다.
func uniformWeights() map[int]float64 {
	weights := make(map[int]float64, 45)
	for n := 1; n <= 45; n++ {
		weights[n] = 1
	}
	return weights
}

// boostedWeights는 trendWeights/regressionWeights가 공유하는 아주 작은
// 조립 로직이다("boosted 집합에 들면 배율 적용, 아니면 기본값 1") —
// 어떤 번호를 boosted 집합에 넣을지(빈도 상위 vs gap 상위)를 정하는
// 실제 가중치 "정책"은 각자 분리되어 있고, 여기서는 그 정책의 결과를
// 숫자 가중치 맵으로 바꾸는 형식적인 부분만 공유한다.
func boostedWeights(boosted []int) map[int]float64 {
	boostedSet := make(map[int]bool, len(boosted))
	for _, n := range boosted {
		boostedSet[n] = true
	}
	weights := make(map[int]float64, 45)
	for n := 1; n <= 45; n++ {
		if boostedSet[n] {
			weights[n] = lottoHotColdWeightBoost
		} else {
			weights[n] = 1
		}
	}
	return weights
}

func weightsForMode(mode string, stats map[int]*lottoNumberStat) map[int]float64 {
	switch mode {
	case lottoModeTrend:
		return trendWeights(stats)
	case lottoModeRegression:
		return regressionWeights(stats)
	default:
		return uniformWeights()
	}
}

// weightedSampleWithoutReplacement는 pool에서 서로 다른 번호 k개를
// weights 비율에 따라 중복 없이 뽑는다. 뽑을 때마다 이미 뽑힌 번호를
// 후보에서 제거하고 남은 후보들로 가중치 합을 다시 계산한다 — 그래야
// 6개를 뽑는 동안 같은 번호가 여러 번 뽑히는 일이 없다.
func weightedSampleWithoutReplacement(pool []int, weights map[int]float64, k int) []int {
	remaining := make([]int, len(pool))
	copy(remaining, pool)

	result := make([]int, 0, k)
	for len(result) < k && len(remaining) > 0 {
		total := 0.0
		for _, n := range remaining {
			total += weights[n]
		}

		pick := rand.Float64() * total
		acc := 0.0
		chosen := len(remaining) - 1 // 부동소수점 누적 오차로 못 찾을 때의 안전한 폴백
		for i, n := range remaining {
			acc += weights[n]
			if pick < acc {
				chosen = i
				break
			}
		}

		result = append(result, remaining[chosen])
		remaining = append(remaining[:chosen], remaining[chosen+1:]...)
	}
	return result
}

// ---------- 3단계: 패턴 필터 검증 ----------

func countOddEven(numbers []int) (odd, even int) {
	for _, n := range numbers {
		if n%2 == 1 {
			odd++
		} else {
			even++
		}
	}
	return odd, even
}

// checkOddEvenRatio: 홀수 개수가 2~4개 범위(=짝수도 자동으로 2~4개)여야
// 한다.
func checkOddEvenRatio(numbers []int) bool {
	odd, _ := countOddEven(numbers)
	return odd >= 2 && odd <= 4
}

var lottoBandLabels = [5]string{"1-9", "10-19", "20-29", "30-39", "40-45"}

// lottoBandIndex는 1~45 번호를 5개 구간(1-9, 10-19, 20-29, 30-39, 40-45)
// 중 하나로 분류한다.
func lottoBandIndex(n int) int {
	switch {
	case n <= 9:
		return 0
	case n <= 19:
		return 1
	case n <= 29:
		return 2
	case n <= 39:
		return 3
	default:
		return 4
	}
}

func bandCounts(numbers []int) [5]int {
	var counts [5]int
	for _, n := range numbers {
		counts[lottoBandIndex(n)]++
	}
	return counts
}

// checkBandDistribution: 5개 구간 중 어느 하나에도 4개 이상 몰리지
// 않아야 한다.
func checkBandDistribution(numbers []int) bool {
	for _, c := range bandCounts(numbers) {
		if c >= 4 {
			return false
		}
	}
	return true
}

func sumNumbers(numbers []int) int {
	sum := 0
	for _, n := range numbers {
		sum += n
	}
	return sum
}

// checkSumRange: 6개 번호의 합이 100~170 사이여야 한다.
func checkSumRange(numbers []int) bool {
	sum := sumNumbers(numbers)
	return sum >= 100 && sum <= 170
}

// checkNoTripleConsecutive: 3개 이상 연속되는 숫자가 없어야 한다.
// numbers는 오름차순 정렬되어 있다고 가정한다.
func checkNoTripleConsecutive(sortedNumbers []int) bool {
	run := 1
	for i := 1; i < len(sortedNumbers); i++ {
		if sortedNumbers[i] == sortedNumbers[i-1]+1 {
			run++
			if run >= 3 {
				return false
			}
		} else {
			run = 1
		}
	}
	return true
}

func countOverlap(numbers, other []int) int {
	otherSet := make(map[int]bool, len(other))
	for _, n := range other {
		otherSet[n] = true
	}
	overlap := 0
	for _, n := range numbers {
		if otherSet[n] {
			overlap++
		}
	}
	return overlap
}

// checkPreviousDrawOverlap: 직전 회차 당첨번호(보너스 제외)와 겹치는
// 번호가 2개 이상이면 안 된다(교집합 최대 1개까지 허용).
func checkPreviousDrawOverlap(numbers, previousDrawNumbers []int) bool {
	return countOverlap(numbers, previousDrawNumbers) <= 1
}

// passesAllRecommendationFilters는 3단계의 다섯 조건을 모두 만족하는지
// 확인한다. numbers는 오름차순 정렬되어 있어야 한다(checkNoTripleConsecutive
// 요구사항).
func passesAllRecommendationFilters(numbers, previousDrawNumbers []int) bool {
	return checkOddEvenRatio(numbers) &&
		checkBandDistribution(numbers) &&
		checkSumRange(numbers) &&
		checkNoTripleConsecutive(numbers) &&
		checkPreviousDrawOverlap(numbers, previousDrawNumbers)
}

// ---------- 4단계: 결과 출력 ----------

// lottoRecommendationMaxAttempts는 3단계 필터를 만족하는 조합이 나올
// 때까지 재추출을 시도하는 최대 횟수다. 다섯 필터를 동시에 만족하는
// 조합은 실제로 상당히 흔해서(무작위 6개 조합 대부분이 이미 통과한다)
// 정상적인 상황에서는 몇 번 안에 끝나지만, 혹시 극단적인 가중치
// 조합에서 계속 실패하더라도 무한 루프에 빠지지 않도록 상한을 둔다.
const lottoRecommendationMaxAttempts = 1000

func computeRecommendationStats(numbers, previousDrawNumbers []int) LottoRecommendationStats {
	odd, even := countOddEven(numbers)
	counts := bandCounts(numbers)
	bands := make(map[string]int, len(lottoBandLabels))
	for i, label := range lottoBandLabels {
		bands[label] = counts[i]
	}

	return LottoRecommendationStats{
		OddEvenRatio:        fmt.Sprintf("%d:%d", odd, even),
		Sum:                 sumNumbers(numbers),
		BandDistribution:    bands,
		OverlapWithPrevious: countOverlap(numbers, previousDrawNumbers),
	}
}

// generateRecommendationSet은 2단계(가중 추출)와 3단계(패턴 필터)를
// 묶어서, mode에 맞는 가중치를 계산한 뒤 generateRecommendationSetWithWeights에
// 넘긴다.
func generateRecommendationSet(mode string, stats map[int]*lottoNumberStat, previousDrawNumbers []int) LottoRecommendationSet {
	return generateRecommendationSetWithWeights(mode, weightsForMode(mode, stats), previousDrawNumbers)
}

// generateRecommendationSetWithWeights는 필터를 통과하는 조합이 나올
// 때까지 최대 lottoRecommendationMaxAttempts번 재추출한다. 그래도 통과하는
// 조합을 못 찾으면(이론상 극히 드물다) 마지막 후보를 그대로 쓰고 경고
// 로그를 남긴다 — 추천 섹션 자체가 사라지는 것보다는, 필터를 살짝
// 벗어난 조합이라도 보여주는 편이 낫다. weights를 별도 인자로 받도록
// generateRecommendationSet과 분리해둔 것은, 테스트에서 "특정 6개
// 번호만 뽑히도록" 가중치를 직접 조작해 최대 재시도 경로를 결정적으로
// 재현할 수 있게 하기 위해서다(mode/stats를 거치면 항상 45개 전체에
// 어느 정도 가중치가 남아 있어 이런 결정적 재현이 불가능하다).
func generateRecommendationSetWithWeights(mode string, weights map[int]float64, previousDrawNumbers []int) LottoRecommendationSet {
	pool := make([]int, 45)
	for n := 1; n <= 45; n++ {
		pool[n-1] = n
	}

	var candidate []int
	for attempt := 1; attempt <= lottoRecommendationMaxAttempts; attempt++ {
		candidate = weightedSampleWithoutReplacement(pool, weights, 6)
		sort.Ints(candidate)
		if passesAllRecommendationFilters(candidate, previousDrawNumbers) {
			break
		}
		if attempt == lottoRecommendationMaxAttempts {
			log.Printf("로또: 추천번호 패턴 필터를 %d회 재추출해도 만족하는 조합을 찾지 못해 마지막 후보를 그대로 사용합니다 (mode=%s, numbers=%v)",
				lottoRecommendationMaxAttempts, mode, candidate)
		}
	}

	return LottoRecommendationSet{
		Numbers: candidate,
		Stats:   computeRecommendationStats(candidate, previousDrawNumbers),
	}
}
