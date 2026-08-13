package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// numericUnitPattern은 숫자 바로 뒤에 K/M/B가 붙는 축약 표기("9B",
// "$6.6B", "500K")뿐 아니라, 국제 뉴스 헤드라인에서 흔한 소문자·철자
// 표기("£25bn", "$6.6bn", "25 million", "£16m")까지 매치한다. 실제 보고된
// 사례: "£25bn"은 이 패턴이 예전에 잡아내지 못해(통화 기호가 £이고 단위가
// 소문자 2글자 "bn"이라 K/M/B 한 글자·대문자·$ 전용이던 옛 패턴에 안
// 걸렸다) 원문 그대로 모델에 전달됐고, 모델이 직접 계산하다가
// "2500억"(10배 과다)으로 틀렸다. 그 뒤 "£16m"도 같은 이유(단위가 소문자
// 단일 글자 "m")로 재발했다.
//
// 대소문자를 통째로 구분하지 않고(`(?i)`) k/m/b 단일 글자까지 매치한 뒤,
// 실제로 값을 계산하는 parseNumericUnitMatch에서 "통화 기호 없이 소문자
// 단일 글자만 있는" 경우만 별도로 걸러낸다 — "100m"(100미터), "3m"처럼
// 흔한 단위·약어와 충돌하는 조합은 이 경우뿐이기 때문이다. "£16m"처럼
// 통화 기호가 함께 있으면 소문자 단일 글자라도 안전하게 금액으로 판단할
// 수 있고, "bn"/"mn"/"thousand"/"million"/"billion" 철자 표기는 두 글자
// 이상이라 통화 기호 유무와 무관하게 다른 의미로 오인될 여지가 없다.
var numericUnitPattern = regexp.MustCompile(`(?i)([$£€]?)(\d+(?:\.\d+)?)\s?(k|m|b|bn|mn|thousand|million|billion)\b`)

// numericUnitMultipliers는 numericUnitPattern이 잡아낸 단위 텍스트(소문자로
// 정규화됨)를 실제 배수로 변환한다. k(천)/m(백만)/b(십억) 세 단위 모두
// 여기 한 곳에만 등록하면 되므로, "새 단위 하나를 깜빡 빠뜨려 재발하는"
// 문제(예: bn은 예외 처리했는데 m은 빠뜨린 사례)가 구조적으로 줄어든다.
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

// parseNumericUnitMatch는 numericUnitPattern.FindStringSubmatch가 반환한
// 그룹(전체매치, 통화기호, 숫자, 단위)을 받아 실제 배수를 적용한 값과
// 통화 접미사를 계산한다. annotateNumericUnits(사전 변환)와
// briefing.go의 extractEnglishUnitNumbers(사후 검증)가 이 함수 하나를
// 공유한다 — 두 곳이 "어떤 단위 표기를 인정할지"에 대해 서로 다른
// 판단을 내리면, 한쪽만 아는 표기가 있을 때 정상 변환을 다른 쪽이
// 근거 없는 숫자로 오탐하는 문제가 재발하기 때문이다.
//
// ok가 false를 반환하는 경우는 두 가지다: (1) 단위가
// numericUnitMultipliers에 없거나 숫자 파싱에 실패한 경우, (2) 통화
// 기호 없이 소문자 단일 글자 단위만 있는 경우 — "100m"(100미터),
// "3m"처럼 통화와 무관한 단위·약어와 구분할 방법이 없어서 안전하게
// 거부한다. 대문자 단일 글자(예: "9B")나 통화 기호가 붙은 소문자
// 단일 글자(예: "£16m"), 그리고 두 글자 이상인 철자 표기(bn/million
// 등)는 통화 기호 유무와 무관하게 항상 허용한다.
func parseNumericUnitMatch(groups []string) (value float64, currencySuffix string, ok bool) {
	if len(groups) < 4 {
		return 0, "", false
	}
	currencySymbol, numStr, unitTextRaw := groups[1], groups[2], groups[3]
	unitTextLower := strings.ToLower(unitTextRaw)

	if currencySymbol == "" && len(unitTextRaw) == 1 && unitTextRaw == unitTextLower {
		return 0, "", false
	}

	multiplier, found := numericUnitMultipliers[unitTextLower]
	if !found {
		return 0, "", false
	}

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, "", false
	}

	return num * multiplier, numericUnitCurrencySuffixes[currencySymbol], true
}

// annotateNumericUnits는 제목에 나오는 K/M/B(및 bn/mn/thousand/million/
// billion) 축약 표기를 미리 계산해 둔 한글 숫자 표현으로 통째로 치환한다
// — 원래 축약 표기의 흔적은 전혀 남기지 않는다. 예: "9B" -> "90억",
// "$6.6B" -> "66억 달러", "£25bn" -> "250억 파운드", "£16m" -> "1600만 파운드".
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
		value, currencySuffix, ok := parseNumericUnitMatch(groups)
		if !ok {
			return match
		}

		korean := koreanUnitAmount(value)
		if currencySuffix != "" {
			korean += " " + currencySuffix
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

// bareCurrencyAmountPattern은 numericUnitPattern과 달리 단위 축약형이 없는
// 통화 금액("$1,204,000", "£25.50")을 매칭한다 — numericUnitPattern은
// million/billion/bn 같은 단위 축약형이 반드시 있어야 매칭되므로, 이
// 패턴은 그 보완이다. truncateForPrompt(briefing.go)가 잘라내는 위치가
// 이런 금액 한가운데를 가로지르지 않게 하는 용도로만 쓰인다 — 통화
// 기호를 필수로 요구해, 연도나 퍼센트처럼 무관한 숫자 때문에 잘라내는
// 위치가 불필요하게 뒤로 밀리지 않게 한다.
var bareCurrencyAmountPattern = regexp.MustCompile(`[$£€]\s?\d[\d,]*(?:\.\d+)?`)

// numericTruncationGuardPatterns는 extendCutToPreserveNumericToken이
// "이 지점에서 자르면 숫자 표현이 반토막 나는가"를 확인할 때 훑는
// 패턴 목록이다. numericUnitPattern(통화 기호 없이도 million/billion 같은
// 단위 축약형이 있으면 매칭)과 bareCurrencyAmountPattern(단위 축약형 없이
// 통화 기호만 있는 금액)을 함께 써야, "3 million people"처럼 통화가 아닌
// 표현과 "$1,204,000"처럼 축약형이 없는 금액을 모두 커버한다.
var numericTruncationGuardPatterns = []*regexp.Regexp{numericUnitPattern, bareCurrencyAmountPattern}

// extendCutToPreserveNumericToken은 cutIdx(원본 문자열 s의 rune 인덱스)가
// numericTruncationGuardPatterns 중 하나의 매치 중간을 가로지르면, 그
// 매치 전체가 포함되도록 cutIdx를 매치의 끝까지 뒤로 미룬다. 가로지르는
// 매치가 없으면 cutIdx를 그대로 반환한다.
//
// 실제 보고된 사례: "$100 million" 표현이 하드컷 지점 바로 뒤에서 시작되는
// " million" 부분만 잘려나가 "$100…"만 남으면, annotateNumericUnits가
// 매칭할 단위를 찾지 못해 원래 값이 완전히 소실되고, 모델이 "1억"과
// "10억" 사이를 오가며 추측하다 검증에 반복 실패하는 사고로 이어졌다.
// 완전히 안 자르는 대신 이 표현 하나만큼만 살짝 더 자르는 것이,
// title/description 전체 상한을 없애는 것보다 토큰 비용 대비 효율적이다.
//
// 최대 3회 반복하는 이유는, cutIdx를 한 매치의 끝까지 늘렸더니 공교롭게도
// 그 늘어난 지점이 또 다른 매치 중간에 걸리는(예: 숫자 표현 두 개가
// 바로 붙어 있는) 드문 경우까지 안전하게 처리하기 위해서다 — 실제로는
// 거의 항상 1회 안에 안정된다.
func extendCutToPreserveNumericToken(s string, cutIdx int) int {
	for pass := 0; pass < 3; pass++ {
		moved := false
		for _, pattern := range numericTruncationGuardPatterns {
			for _, loc := range pattern.FindAllStringIndex(s, -1) {
				start := utf8.RuneCountInString(s[:loc[0]])
				end := utf8.RuneCountInString(s[:loc[1]])
				if start < cutIdx && end > cutIdx {
					cutIdx = end
					moved = true
				}
			}
		}
		if !moved {
			break
		}
	}
	return cutIdx
}
