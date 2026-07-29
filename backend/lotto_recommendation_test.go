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

func TestComputeRecommendationNumbers(t *testing.T) {
	frequency := make(map[int]int, 45)
	for n := 1; n <= 45; n++ {
		frequency[n] = 45 - n // 1이 가장 많이 나오고 45가 가장 적게 나옴
	}

	result := computeRecommendationNumbers(frequency)

	if len(result) != 6 {
		t.Fatalf("expected 6 numbers, got %d", len(result))
	}

	seen := make(map[int]bool)
	groupCounts := map[string]int{}
	for _, n := range result {
		if n.Number < 1 || n.Number > 45 {
			t.Errorf("number %d out of range 1-45", n.Number)
		}
		if seen[n.Number] {
			t.Errorf("duplicate number %d", n.Number)
		}
		seen[n.Number] = true
		groupCounts[n.Group]++
	}

	for _, group := range []string{recommendationGroupHot, recommendationGroupMid, recommendationGroupCold} {
		if groupCounts[group] != 2 {
			t.Errorf("expected exactly 2 numbers in group %q, got %d", group, groupCounts[group])
		}
	}

	// frequency[n] = 45-n이므로, 구간 1(hot, 빈도 상위 15개)은 1-15번,
	// 구간 2(mid)는 16-30번, 구간 3(cold)은 31-45번이 된다.
	for _, n := range result {
		switch n.Group {
		case recommendationGroupHot:
			if n.Number < 1 || n.Number > 15 {
				t.Errorf("hot number %d not in expected band 1-15", n.Number)
			}
		case recommendationGroupMid:
			if n.Number < 16 || n.Number > 30 {
				t.Errorf("mid number %d not in expected band 16-30", n.Number)
			}
		case recommendationGroupCold:
			if n.Number < 31 || n.Number > 45 {
				t.Errorf("cold number %d not in expected band 31-45", n.Number)
			}
		}
	}
}

func TestEncodeDecodeRecommendationNumbersRoundTrip(t *testing.T) {
	original := []LottoRecommendationNumber{
		{Number: 3, Group: "hot"},
		{Number: 12, Group: "hot"},
		{Number: 19, Group: "mid"},
		{Number: 27, Group: "mid"},
		{Number: 33, Group: "cold"},
		{Number: 41, Group: "cold"},
	}

	numbersCSV, groupsCSV := encodeRecommendationNumbers(original)
	if numbersCSV != "3,12,19,27,33,41" {
		t.Errorf("numbersCSV = %q, want %q", numbersCSV, "3,12,19,27,33,41")
	}

	decoded, err := decodeRecommendationNumbers(numbersCSV, groupsCSV)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded length = %d, want %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("decoded[%d] = %+v, want %+v", i, decoded[i], original[i])
		}
	}
}
