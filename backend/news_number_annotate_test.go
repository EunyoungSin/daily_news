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

		// 실제 보고된 사례: "£25bn"이 예전 패턴(대문자 K/M/B, $ 전용)에
		// 안 걸려 원문 그대로 모델에 전달됐고, 모델이 직접 계산하다
		// "2500억"(10배 과다)으로 틀렸다.
		{"UK unveils £25bn infrastructure plan", "UK unveils 250억 파운드 infrastructure plan"},
		{"a £2.5bn deal", "a 25억 파운드 deal"},
		{"raises $6.6bn in new funding", "raises 66억 달러 in new funding"},
		{"a €1.2bn acquisition", "a 12억 유로 acquisition"},
		{"invests $25 million in the project", "invests 2500만 달러 in the project"},
		{"donated 500 thousand meals", "donated 50만 meals"},
		// 소문자 단일 글자는 통화 기호가 없으면 여전히 매치되면 안 된다 —
		// "100m"이 100미터를 뜻할 수도 있는데 무조건 1억으로 바꾸면 안
		// 되기 때문이다.
		{"ran the 100m sprint in 9.8 seconds", "ran the 100m sprint in 9.8 seconds"},

		// 실제 보고된 재발 사례: "£25bn"(bn)은 예외 처리했지만 "£16m"(소문자
		// 단일 글자 m)은 빠뜨려서 근거 없는 숫자로 오탐이 재발했다. £/$/€
		// 같은 통화 기호가 앞에 붙어 있으면 소문자 단일 글자라도 "100m"
		// 같은 단위와 헷갈릴 여지가 없으므로 안전하게 변환해야 한다.
		{"UK invests £16m in the scheme", "UK invests 1600만 파운드 in the scheme"},
		{"a $2.5m deal", "a 250만 달러 deal"},
		{"a €900k grant", "a 90만 유로 grant"},
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
