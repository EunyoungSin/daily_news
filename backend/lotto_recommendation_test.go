package main

import (
	"testing"
	"time"
)

func TestIsLottoRecommendationBlackout(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"Saturday 19:59 — sales still open", time.Date(2026, 8, 1, 19, 59, 0, 0, kst), false},
		{"Saturday 20:00 — sales just closed", time.Date(2026, 8, 1, 20, 0, 0, 0, kst), true},
		{"Saturday 20:01 — blackout", time.Date(2026, 8, 1, 20, 1, 0, 0, kst), true},
		{"Saturday 23:59 — still blackout", time.Date(2026, 8, 1, 23, 59, 0, 0, kst), true},
		{"Sunday 00:00 — still blackout", time.Date(2026, 8, 2, 0, 0, 0, 0, kst), true},
		{"Sunday 05:59 — still blackout", time.Date(2026, 8, 2, 5, 59, 0, 0, kst), true},
		{"Sunday 06:00 — blackout just ended", time.Date(2026, 8, 2, 6, 0, 0, 0, kst), false},
		{"Sunday 06:01 — available", time.Date(2026, 8, 2, 6, 1, 0, 0, kst), false},
		{"Wednesday noon — ordinary weekday", time.Date(2026, 8, 5, 12, 0, 0, 0, kst), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLottoRecommendationBlackout(tc.when); got != tc.want {
				t.Errorf("isLottoRecommendationBlackout(%v) = %v, want %v", tc.when, got, tc.want)
			}
		})
	}
}

func TestLottoCycleStartDate(t *testing.T) {
	// 2026-08-02는 일요일이다.
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"Sunday 06:00 itself", time.Date(2026, 8, 2, 6, 0, 0, 0, kst), "2026-08-02"},
		{"Sunday evening, same cycle", time.Date(2026, 8, 2, 22, 0, 0, 0, kst), "2026-08-02"},
		{"midweek Wednesday, same cycle", time.Date(2026, 8, 5, 12, 0, 0, 0, kst), "2026-08-02"},
		{"Saturday 19:59, still same cycle", time.Date(2026, 8, 8, 19, 59, 0, 0, kst), "2026-08-02"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lottoCycleStartDate(tc.when).Format("2006-01-02")
			if got != tc.want {
				t.Errorf("lottoCycleStartDate(%v) = %s, want %s", tc.when, got, tc.want)
			}
		})
	}
}

func TestNextLottoAvailableAt(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want time.Time
	}{
		{
			"Saturday 20:01 blackout -> next day 06:00",
			time.Date(2026, 8, 1, 20, 1, 0, 0, kst),
			time.Date(2026, 8, 2, 6, 0, 0, 0, kst),
		},
		{
			"Sunday 05:59 blackout -> later today 06:00",
			time.Date(2026, 8, 2, 5, 59, 0, 0, kst),
			time.Date(2026, 8, 2, 6, 0, 0, 0, kst),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextLottoAvailableAt(tc.when)
			if !got.Equal(tc.want) {
				t.Errorf("nextLottoAvailableAt(%v) = %v, want %v", tc.when, got, tc.want)
			}
		})
	}
}

func TestEncodeDecodeRecommendationSetRoundTrip(t *testing.T) {
	original := LottoRecommendationSet{
		Numbers: []int{3, 12, 19, 27, 33, 41},
		Stats: LottoRecommendationStats{
			OddEvenRatio:        "3:3",
			Sum:                 135,
			BandDistribution:    map[string]int{"1-9": 1, "10-19": 1, "20-29": 1, "30-39": 1, "40-45": 2},
			OverlapWithPrevious: 1,
		},
	}

	numbersJSON, statsJSON, err := encodeRecommendationSet(original)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}

	decoded, err := decodeRecommendationSet(numbersJSON, statsJSON)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(decoded.Numbers) != len(original.Numbers) {
		t.Fatalf("decoded.Numbers length mismatch")
	}
	for j := range original.Numbers {
		if decoded.Numbers[j] != original.Numbers[j] {
			t.Errorf("decoded.Numbers[%d] = %d, want %d", j, decoded.Numbers[j], original.Numbers[j])
		}
	}
	// LottoRecommendationStats는 map 필드(BandDistribution)를 담고
	// 있어 == 비교가 불가능하므로 필드별로 확인한다.
	if decoded.Stats.OddEvenRatio != original.Stats.OddEvenRatio ||
		decoded.Stats.Sum != original.Stats.Sum ||
		decoded.Stats.OverlapWithPrevious != original.Stats.OverlapWithPrevious {
		t.Errorf("decoded.Stats = %+v, want %+v", decoded.Stats, original.Stats)
	}
	for band, count := range original.Stats.BandDistribution {
		if decoded.Stats.BandDistribution[band] != count {
			t.Errorf("decoded.Stats.BandDistribution[%q] = %d, want %d", band, decoded.Stats.BandDistribution[band], count)
		}
	}
}

func TestDecodeRecommendationSetRejectsOldArrayFormat(t *testing.T) {
	// 예전(다중 세트) 형식 — 지금의 단일 세트 형식으로는 타입이 맞지
	// 않아 디코딩에 실패해야 한다(lookupLottoRecommendation이 이를 낡은
	// 캐시로 취급해 재계산하도록 만드는 안전장치).
	numbersJSON := `[[1,2,3,4,5,6],[7,8,9,10,11,12]]`
	statsJSON := `[{"oddEvenRatio":"3:3","sum":21,"bandDistribution":{},"overlapWithPrevious":0}]`
	if _, err := decodeRecommendationSet(numbersJSON, statsJSON); err == nil {
		t.Error("expected an error when decoding the old multi-set array format, got nil")
	}
}
