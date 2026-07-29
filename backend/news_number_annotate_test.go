package main

import "testing"

func TestAnnotateNumericUnits(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"A $500 RL fine-tune of a 9B open model", "A $500 RL fine-tune of a 90억 open model"},
		{"OpenAI raises $6.6B in funding", "OpenAI raises 66억 달러 in funding"},
		{"New 70B parameter model", "New 700억 parameter model"},
		{"trained on 500K examples", "trained on 50만 examples"},
		{"128K token context window", "12.8만 token context window"},
		{"raised $500M in Series B", "raised 5억 달러 in Series B"},
		{"no numbers here", "no numbers here"},
		{"a 3D model viewer", "a 3D model viewer"}, // "3D"는 단위로 매치되면 안 됨
	}

	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := annotateNumericUnits(tc.title)
			if got != tc.want {
				t.Errorf("annotateNumericUnits(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestKoreanUnitAmount(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{9e9, "90억"},     // 9B
		{6.6e9, "66억"},   // 6.6B — 70B 모델이 실제로 틀렸던 케이스(660억이라고 답함)
		{70e9, "700억"},   // 70B — 70B 모델이 실제로 틀렸던 케이스(70억이라고 답함)
		{500e6, "5억"},    // 500M
		{500e3, "50만"},   // 500K
		{128e3, "12.8만"}, // 128K
	}

	for _, tc := range cases {
		got := koreanUnitAmount(tc.value)
		if got != tc.want {
			t.Errorf("koreanUnitAmount(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
