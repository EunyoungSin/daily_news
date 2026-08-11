package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// numericUnitPattern은 숫자 바로 뒤에 K/M/B가 붙는 축약 표기("9B",
// "$6.6B", "500K")뿐 아니라, 국제 뉴스 헤드라인에서 흔한 소문자·철자
// 표기("£25bn", "$6.6bn", "25 million")까지 매치한다. 실제 보고된 사례:
// "£25bn"은 이 패턴이 예전에 잡아내지 못해(통화 기호가 £이고 단위가
// 소문자 2글자 "bn"이라 K/M/B 한 글자·대문자·$ 전용이던 옛 패턴에 안
// 걸렸다) 원문 그대로 모델에 전달됐고, 모델이 직접 계산하다가
// "2500억"(10배 과다)으로 틀렸다.
//
// 단일 글자 축약(K/M/B)은 대소문자를 구분한다 — 소문자까지 허용하면
// "100m"(100미터), "3m"처럼 흔한 단위·약어와 충돌해 false positive가
// 생긴다. 반면 "bn"/"mn"과 "thousand"/"million"/"billion" 철자 표기는
// 두 글자 이상이라 다른 의미로 오인될 여지가 사실상 없으므로
// `(?i:...)`로 대소문자를 구분하지 않는다.
var numericUnitPattern = regexp.MustCompile(`([$£€]?)(\d+(?:\.\d+)?)\s?([KMB]|(?i:bn|mn|thousand|million|billion))\b`)

// numericUnitMultipliers는 numericUnitPattern이 잡아낸 단위 텍스트(소문자로
// 정규화됨)를 실제 배수로 변환한다. briefing.go의 extractEnglishUnitNumbers도
// 검증 단계에서 같은 매핑을 재사용한다 — annotateNumericUnits가 사전에
// 변환하는 규칙과 findUngroundedNumber가 사후 검증하는 규칙이 서로
// 어긋나면 한쪽만 고친 단위 표기가 있을 때 정상 변환까지 오탐할 수
// 있으므로, 두 곳이 항상 같은 표를 봐야 한다.
var numericUnitMultipliers = map[string]float64{
	"k": 1e3, "thousand": 1e3,
	"m": 1e6, "mn": 1e6, "million": 1e6,
	"b": 1e9, "bn": 1e9, "billion": 1e9,
}

// numericUnitCurrencySuffixes는 numericUnitPattern이 잡아낸 통화 기호를
// 변환된 한글 숫자 뒤에 붙일 통화명으로 매핑한다.
var numericUnitCurrencySuffixes = map[string]string{
	"$": "달러",
	"£": "파운드",
	"€": "유로",
}

// annotateNumericUnits는 제목에 나오는 K/M/B(및 bn/mn/thousand/million/
// billion) 축약 표기를 미리 계산해 둔 한글 숫자 표현으로 통째로 치환한다
// — 원래 축약 표기의 흔적은 전혀 남기지 않는다. 예: "9B" -> "90억",
// "$6.6B" -> "66억 달러", "£25bn" -> "250억 파운드".
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
// 과다)으로, "70B"를 "70억"(10배 과소)으로, "£25bn"을 "2500억"(10배
// 과다)으로 잘못 번역했다.
func annotateNumericUnits(title string) string {
	return numericUnitPattern.ReplaceAllStringFunc(title, func(match string) string {
		groups := numericUnitPattern.FindStringSubmatch(match)
		if groups == nil {
			return match
		}
		currencySymbol, numStr, unitText := groups[1], groups[2], strings.ToLower(groups[3])

		num, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return match
		}

		multiplier, ok := numericUnitMultipliers[unitText]
		if !ok {
			return match
		}

		korean := koreanUnitAmount(num * multiplier)
		if suffix, ok := numericUnitCurrencySuffixes[currencySymbol]; ok {
			korean += " " + suffix
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
