package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// numericUnitPattern은 "9B", "$6.6B", "500K", "128K"처럼 숫자 바로 뒤에
// K/M/B가 붙는 축약 표기와 매치한다 (대소문자를 구분하는 이유: 소문자까지
// 허용하면 일반 단어에 false positive가 생길 위험이 있다).
var numericUnitPattern = regexp.MustCompile(`\$?\d+(?:\.\d+)?[KMB]\b`)

// annotateNumericUnits는 제목에 나오는 K/M/B 축약 표기를 미리 계산해 둔
// 한글 숫자 표현으로 통째로 치환한다 — 원래 축약 표기의 흔적은 전혀 남기지
// 않는다. 예: "9B" -> "90억", "$6.6B" -> "66억 달러".
//
// 지금 방식으로 정착하기 전에 더 약한 방식 두 가지를 시도했는데, 둘 다 AI
// 브리핑에 쓰는 작은 모델(llama-3.1-8b-instant)로 테스트하다가 실패했다:
//   - 원본 뒤에 한글 값을 덧붙이는 방식("$12M(1200만 달러)")도 결국
//     "12만 달러"로 나왔다 — 모델이 주어진 답을 쓰지 않고 원본을 다시
//     계산해버렸다.
//   - 한글 값을 앞에 두고 원본을 뒤 괄호에 남기는 방식("1200만
//     달러($12M)")도 *여전히* "12만 달러"로 나왔다 — 바로 앞에 정확한
//     값이 있는데도 모델이 괄호 속 "M"을 한글 단위 "만"과 잘못 매칭해
//     혼동했다.
//
// 결국 축약 표기를 아예 지워버리는 방식이 먹혔다: "M"/"B" 문자가 어디에도
// 남지 않으니 모델이 단위로 잘못 읽을 거리 자체가 없어진다.
//
// 이 함수가 애초에 필요한 이유는, 70B 파라미터급 모델도 K(천/만) 변환은
// 안정적으로 맞히지만 B(십억) 변환은 한 자리 숫자의 딱 떨어지는 값이
// 아니면 자주 틀리기 때문이다 — 예를 들어 "$6.6B"를 "660억"(10배
// 과다)으로, "70B"를 "70억"(10배 과소)으로 잘못 번역했다.
func annotateNumericUnits(title string) string {
	return numericUnitPattern.ReplaceAllStringFunc(title, func(match string) string {
		hasDollar := strings.HasPrefix(match, "$")
		numPart := match
		if hasDollar {
			numPart = strings.TrimPrefix(numPart, "$")
		}

		unit := numPart[len(numPart)-1]
		numStr := numPart[:len(numPart)-1]

		num, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return match
		}

		var multiplier float64
		switch unit {
		case 'K':
			multiplier = 1e3
		case 'M':
			multiplier = 1e6
		case 'B':
			multiplier = 1e9
		default:
			return match
		}

		korean := koreanUnitAmount(num * multiplier)
		if hasDollar {
			korean += " 달러"
		}
		return korean
	})
}

// koreanUnitAmount는 한국 뉴스 헤드라인 표기 방식대로 절댓값을
// 억(10^8)/만(10^4) 단위로 표현한다. 예: 9e9 -> "90억", 6.6e9 -> "66억",
// 128000 -> "12.8만".
func koreanUnitAmount(value float64) string {
	abs := math.Abs(value)
	switch {
	case abs >= 1e8:
		return trimTrailingZero(value/1e8) + "억"
	case abs >= 1e4:
		return trimTrailingZero(value/1e4) + "만"
	default:
		return trimTrailingZero(value)
	}
}

func trimTrailingZero(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}
