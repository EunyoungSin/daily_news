package main

import "testing"

func TestStripLeakedDisclaimer(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no disclaimer — text passes through unchanged",
			in:   "최근 50회 로또 데이터를 분석한 결과, 15번과 27번이 각각 11회로 가장 많이 출현했습니다. 반면, 14번은 3회로 가장 적게 출현했습니다. 또한, 최근 10회 동안에는 43번과 44번이 두드러지는 출현 빈도를 보였습니다.",
			want: "최근 50회 로또 데이터를 분석한 결과, 15번과 27번이 각각 11회로 가장 많이 출현했습니다. 반면, 14번은 3회로 가장 적게 출현했습니다. 또한, 최근 10회 동안에는 43번과 44번이 두드러지는 출현 빈도를 보였습니다.",
		},
		{
			name: "trailing disclaimer sentence on its own line is removed",
			in:   "15번과 27번이 각각 11회로 가장 많이 출현했습니다. 14번은 3회로 가장 적게 출현했습니다. 43번과 44번이 두드러졌습니다.\n※ 통계적 재미를 위한 분석입니다.",
			want: "15번과 27번이 각각 11회로 가장 많이 출현했습니다. 14번은 3회로 가장 적게 출현했습니다. 43번과 44번이 두드러졌습니다.",
		},
		{
			name: "disclaimer glued onto the same line as the last stat sentence",
			in:   "15번과 27번이 각각 11회로 가장 많이 출현했습니다. 14번은 3회로 가장 적게 출현했습니다. 43번과 44번이 두드러졌습니다. ※ 통계적 재미를 위한 분석입니다.",
			want: "15번과 27번이 각각 11회로 가장 많이 출현했습니다. 14번은 3회로 가장 적게 출현했습니다. 43번과 44번이 두드러졌습니다.",
		},
		{
			name: "paraphrased disclaimer without the ※ mark is still caught via '통계적 재미'",
			in:   "15번과 27번이 가장 많이 출현했습니다. 이 분석은 통계적 재미를 위한 것입니다.",
			want: "15번과 27번이 가장 많이 출현했습니다.",
		},
		{
			name: "decimal points in a stat sentence must not be treated as sentence boundaries",
			in:   "환율은 1 USD당 1320.55 KRW입니다. ※ 통계적 재미를 위한 분석입니다.",
			want: "환율은 1 USD당 1320.55 KRW입니다.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripLeakedDisclaimer(tc.in); got != tc.want {
				t.Errorf("stripLeakedDisclaimer(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitLottoInsightSentences(t *testing.T) {
	in := "15번과 27번이 각각 11회로 가장 많이 출현했습니다. 14번은 3회로 가장 적게 출현했습니다. 환율은 1320.55 KRW입니다."
	want := []string{
		"15번과 27번이 각각 11회로 가장 많이 출현했습니다.",
		"14번은 3회로 가장 적게 출현했습니다.",
		"환율은 1320.55 KRW입니다.",
	}

	got := splitLottoInsightSentences(in)
	if len(got) != len(want) {
		t.Fatalf("splitLottoInsightSentences(%q) = %v (len %d), want len %d", in, got, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}
