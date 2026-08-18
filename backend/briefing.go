package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// umbrellaPrecipThreshold는 프런트엔드의 HIGH_PRECIP_THRESHOLD(WeatherCard.tsx)와
// 값을 맞춰서, AI가 생성하는 문장과 UI의 우산 강조 표시가 "비가 올 것 같다"는
// 기준에 대해 서로 다르게 판단하지 않도록 합니다.
const umbrellaPrecipThreshold = 40

// umbrellaAdvice는 LLM이 직접 판단하게 두지 않고 Go 코드에서 미리 계산합니다.
// 8B 모델은 precipProbability를 기준값과 스스로 비교하는 작업을 매번 틀리기
// 때문입니다(테스트 중 실제로 강수확률 22%에서도 비가 온다고 지어낸 사례가
// 있었습니다). 계산된 boolean 값을 그대로 넘겨주면 이런 실패 유형 자체가
// 사라집니다.
type umbrellaAdvice struct {
	Needed bool   `json:"needed"`
	Period string `json:"period,omitempty"`
}

func computeUmbrellaAdvice(today DayForecast) umbrellaAdvice {
	morningHigh := today.Morning.Available && today.Morning.PrecipProbability >= umbrellaPrecipThreshold
	afternoonHigh := today.Afternoon.Available && today.Afternoon.PrecipProbability >= umbrellaPrecipThreshold

	switch {
	case morningHigh && afternoonHigh:
		return umbrellaAdvice{Needed: true, Period: "오전 8시와 오후 2시 모두"}
	case morningHigh:
		return umbrellaAdvice{Needed: true, Period: "오전 8시"}
	case afternoonHigh:
		return umbrellaAdvice{Needed: true, Period: "오후 2시"}
	default:
		return umbrellaAdvice{Needed: false}
	}
}

// briefingCurrentWeather는 CurrentWeather에서 브리핑 문장 생성에 실제로
// 필요한 값만 남깁니다 — DetailURL은 프런트엔드의 "자세히 보기" 링크용일
// 뿐이므로, Groq 프롬프트에 넣으면 쓸모없는 토큰 낭비만 되고(캐시 키에도
// 무관하게 영향을 줄 뿐이라) 제외했습니다.
type briefingCurrentWeather struct {
	City         string  `json:"city"`
	CityLabel    string  `json:"cityLabel"`
	TemperatureC float64 `json:"temperatureC"`
	WindSpeedKph float64 `json:"windSpeedKph"`
	WeatherCode  int     `json:"weatherCode"`
	Description  string  `json:"description"`
	ObservedAt   string  `json:"observedAt"`
}

// briefingPeriodForecast는 PeriodForecast에서 문장 생성에 실제로 필요한
// 값만 남깁니다 — 특히 Source(models.go 참고: 그 슬롯이 제때 확보된
// 값인지, 이미 지난 뒤 소급 복구한 값인지)는 일부러 제외했습니다. 값의
// 신뢰도 자체는 실측과 다르지 않으므로 문장에서 "예보값으로 대체됨"을
// 언급할 필요가 없고(프론트엔드 배지로만 안내), 여기 그대로 포함시키면
// 같은 기온이라도 Source가 나중에 바뀔 때마다(예: 정각 직후엔 apiValue가
// 그대로 available이었다가, 다시 조회할 즈음엔 소급 복구를 거치는 경우)
// hashJSON 캐시 키가 실질적으로 아무 의미 없는 이유로 무효화됩니다.
type briefingPeriodForecast struct {
	TemperatureC      float64 `json:"temperatureC"`
	WeatherCode       int     `json:"weatherCode"`
	Description       string  `json:"description"`
	PrecipProbability int     `json:"precipProbability"`
}

// briefingDayForecast는 DayForecast와 구조는 같지만 포인터 필드를 사용해서,
// 해당 시간대가 Available하지 않으면 Groq로 보내는 JSON에서 그 필드 자체를
// 아예 생략합니다 — DayForecast의 제로값 PeriodForecast를 그대로 쓰면 실제
// 데이터가 없는 시간대에도 모델에게 "temperatureC": 0이라는 값을 그대로
// 넘겨주게 되기 때문입니다(PeriodForecast의 문서 주석 참고).
type briefingDayForecast struct {
	Morning   *briefingPeriodForecast `json:"morning,omitempty"`
	Afternoon *briefingPeriodForecast `json:"afternoon,omitempty"`
}

func toBriefingPeriodForecast(p PeriodForecast) *briefingPeriodForecast {
	return &briefingPeriodForecast{
		TemperatureC:      p.TemperatureC,
		WeatherCode:       p.WeatherCode,
		Description:       p.Description,
		PrecipProbability: p.PrecipProbability,
	}
}

func toBriefingDayForecast(day DayForecast) briefingDayForecast {
	var out briefingDayForecast
	if day.Morning.Available {
		out.Morning = toBriefingPeriodForecast(day.Morning)
	}
	if day.Afternoon.Available {
		out.Afternoon = toBriefingPeriodForecast(day.Afternoon)
	}
	return out
}

// briefingWeatherInput은 날씨 섹션이 실제로 보게 되는 WeatherData의 유일한
// 부분집합입니다 — 현재 날씨, 오늘 오전/오후 예보, 그리고 계산된 우산 필요
// 여부뿐입니다. Forecast.Tomorrow는 프롬프트 텍스트에서만 빼는 게 아니라
// 이 구조체 자체에서 의도적으로 제외했습니다. 그래야 내일 예보가 바뀌어도
// 캐시가 무효화되거나 브리핑에 내일 예보 내용이 섞여 들어가는 일이 절대
// 생기지 않습니다.
type briefingWeatherInput struct {
	Current        briefingCurrentWeather `json:"current"`
	Today          briefingDayForecast    `json:"today"`
	UmbrellaAdvice umbrellaAdvice         `json:"umbrellaAdvice"`
}

func toBriefingWeatherInput(weather *WeatherData) *briefingWeatherInput {
	if weather == nil {
		return nil
	}
	return &briefingWeatherInput{
		Current: briefingCurrentWeather{
			City:         weather.Current.City,
			CityLabel:    weather.Current.CityLabel,
			TemperatureC: weather.Current.TemperatureC,
			WindSpeedKph: weather.Current.WindSpeedKph,
			WeatherCode:  weather.Current.WeatherCode,
			Description:  weather.Current.Description,
			ObservedAt:   weather.Current.ObservedAt,
		},
		Today:          toBriefingDayForecast(weather.Forecast.Today),
		UmbrellaAdvice: computeUmbrellaAdvice(weather.Forecast.Today),
	}
}

// exchangeTrend도 LLM이 ChangePercent의 부호만 보고 직접 판단하게 두지 않고
// Go에서 미리 계산합니다 — 테스트 중 changePercent=-0.6이 주어졌을 때 모델은
// "하락"이라고는 맞게 말했지만, 이를 "원화 약세"와 짝지어버렸습니다. 이는
// 정반대입니다(원/달러 환율이 하락한다는 건 원화가 강세라는 뜻입니다). 우산
// 기준값이나 B/M/K 단위 환산과 같은 부류의 실패입니다 — 소형 모델의
// 산술에 가까운 추론은 믿을 수 없으니, 답을 미리 계산해서 넘겨줘야 합니다.
type exchangeTrend struct {
	Direction   string `json:"direction"`   // "상승" 또는 "하락"
	Implication string `json:"implication"` // 예: "KRW 약세" 또는 "KRW 강세"
}

// computeExchangeTrend는 이미 계산되어 있는 changePercent 값을 받아서(참고:
// exchange.go의 computeChangePercent — Yesterday 값과 이번 주간 변동값이
// 모두 같은 함수를 거치므로 두 값이 서로 어긋날 일이 없습니다) 방향/의미
// 문구만 결정합니다.
func computeExchangeTrend(changePercent, weekAgoRate float64, to string) *exchangeTrend {
	if weekAgoRate == 0 || changePercent == 0 {
		return nil
	}
	if changePercent > 0 {
		return &exchangeTrend{Direction: "상승", Implication: to + " 약세"}
	}
	return &exchangeTrend{Direction: "하락", Implication: to + " 강세"}
}

// briefingExchangeInput은 미리 계산해둔 Trend를, Groq로 보낼 화면 표시용
// ExchangeData 필드들과 함께 담습니다. Rate/SevenDaysAgoRate는 항상 화면
// 표시용 값이며(1보다 작은 원본 rate가 그대로 들어가는 일은 없습니다 —
// exchangeIsInverted 참고), BaseCurrency/QuoteCurrency/BaseUnits는 그 값이
// 어떤 단위로 고시되는지("100 JPY" vs "1 USD" 등)를 알려줍니다. 덕분에
// 모델은 "환율은 {baseUnits} {baseCurrency}당 {rate} {quoteCurrency}입니다"라고
// 말하기만 하면 되고, 0.00069 같은 아주 작은 소수를 직접 언급하거나
// JPY-per-100 같은 자체 표기 관례를 스스로 만들어낼 필요가 없습니다.
// SevenDaysAgoRate/Date는 ExchangeData.Weekly의 가장 오래된 항목입니다(단순
// 달력 계산이 아니라 Frankfurter가 실제로 반환한 약 7일 전 영업일 데이터).
// 이 값은 Yesterday를 계산할 때도 쓰이는 단일 진실 공급원(single source of
// truth)이므로, 브리핑의 "지난 7일간" 수치가 환율 카드의 차트와 어긋나는
// 일은 있을 수 없습니다.
type briefingExchangeInput struct {
	BaseCurrency     string         `json:"baseCurrency"`
	QuoteCurrency    string         `json:"quoteCurrency"`
	BaseUnits        float64        `json:"baseUnits"`
	Rate             float64        `json:"rate"`
	Date             string         `json:"date"`
	SevenDaysAgoRate float64        `json:"sevenDaysAgoRate,omitempty"`
	SevenDaysAgoDate string         `json:"sevenDaysAgoDate,omitempty"`
	ChangePercent    float64        `json:"changePercent"`
	Trend            *exchangeTrend `json:"trend"`
}

func toBriefingExchangeInput(exchange *ExchangeData) *briefingExchangeInput {
	if exchange == nil {
		return nil
	}

	// fetchExchange가 함께 설정해주는 최상위 RawRate/DisplayRate/DisplayLabel
	// 같은 편의 필드를 그대로 믿지 않고, exchange.Current.Rate/From/To에서
	// 직접 값을 도출합니다 — 이렇게 하면 (테스트에서처럼) Current/Weekly만
	// 채워서 직접 만든 ExchangeData가 들어와도 toBriefingExchangeInput이 올바른
	// 결과를 내며, planExchangeDisplay 자신의 단일 진실 공급원과도 일치합니다.
	// exchange.Current.Rate는 이 시점에 이미 보정/정밀화가 끝난 rate입니다 —
	// exchange.go의 exchangeRateCorrection 참고.
	plan := planExchangeDisplay(exchange.Current.Rate, exchange.From, exchange.To)

	input := &briefingExchangeInput{
		BaseCurrency:  plan.BaseCurrency,
		QuoteCurrency: plan.QuoteCurrency,
		BaseUnits:     plan.BaseUnits,
		Rate:          plan.displayRateFor(exchange.Current.Rate, exchange.From),
		Date:          exchange.Current.Date,
	}

	// weekAgoRawRate는 아래에서 computeExchangeTrend 자체의 0 여부 검사에
	// 쓰입니다 — 역수가 아니라 반드시 원본 rate 값이어야 합니다. 역수 변환은
	// 0이 아닌 입력에 대해 절대 정확히 0을 만들어내지 않으므로, 역수를 쓰면
	// 이 검사가 조용히 무력화되기 때문입니다.
	var weekAgoRawRate float64
	if len(exchange.Weekly) > 0 {
		weekAgo := exchange.Weekly[0]
		weekAgoRawRate = weekAgo.Rate
		if weekAgo.Rate != 0 {
			input.SevenDaysAgoRate = plan.displayRateFor(weekAgo.Rate, exchange.From)
			input.SevenDaysAgoDate = weekAgo.Date
			input.ChangePercent = computeChangePercent(weekAgo.Rate, exchange.Current.Rate)
		}
	}
	// 여기서 plan.QuoteCurrency가 아니라 exchange.To를 쓰는 것은 의도적입니다:
	// trend의 의미("KRW 약세"/"KRW 강세")는 화면 표시상 어느 쪽이 "기준
	// 통화"로 뒤집히든 상관없이 원래 통화쌍의 To 통화를 기준으로 설명하는
	// 것이기 때문입니다.
	input.Trend = computeExchangeTrend(input.ChangePercent, weekAgoRawRate, exchange.To)

	return input
}

// briefingNewsItem/briefingNewsInput은 뉴스 항목에서 브리핑에 필요한 값만
// 남깁니다(URL 없음, translatedTitle 없음 — 모델은 원문 영어 제목을 그대로
// 사용합니다). 그리고 무엇보다 중요한 점은 Title을 annotateNumericUnits에
// 통과시킨다는 것입니다(news_number_annotate.go 참고) — 이걸 하지 않으면
// 여기서도 모델이 K/M/B 단위 축약을 잘못 계산하는 것이 테스트로
// 확인되었습니다("$12M"이 1200만 달러(12,000,000)가 아니라 12만
// 달러(120,000)로 돌아온 사례가 있었습니다). 헤드라인은 상위 5개만
// 유지하는데, 이는 뉴스 카드 자체가 보여주는 개수이자 캐시 해시가 추적하려는
// 범위이기 때문입니다.
//
// Score는 (한때 포함했다가 캐싱 버그가 드러나서) 의도적으로 제외했습니다:
// 프롬프트는 Score를 전혀 참조하지 않는데도, HN 점수는 실시간으로 계속
// 올라가기 때문에 해시 대상 입력에 포함시키면 실제로 생성 텍스트에 영향을
// 주는 유일한 요소인 헤드라인 자체는 전혀 바뀌지 않았는데도 거의 모든
// 요청마다 캐시가 무효화되는 결과가 됐습니다.
//
// 이 값은 news.go의 newsItemCount(뉴스 카드가 화면에 보여주는 개수, 5개)와는
// 의도적으로 분리되어 있습니다. 브리핑은 어차피 한 문장짜리 요약이라
// 헤드라인 하나만 골라 쓰면 충분한데도, 이전에는 5개를 전부 프롬프트에
// 넣고 있었습니다 — TPM 초과 문제를 겪은 뒤 브리핑용 입력만 3개로 줄였고
// (뉴스 카드 자체는 여전히 5개를 모두 보여줍니다), 이는 hashJSON의 캐시
// 키와 newsGroundingText/allowedNewsNumbers의 범위에도 함께 반영됩니다.
const briefingNewsHeadlineCount = 3

// Description을 Title과 함께 포함시킨 것은 실제로 보고된 hallucination을
// 고치기 위한 조치입니다: 헤드라인 하나만 주어지면 모델은 "detailed"
// 문장이 요구하는 추가 구체적 사실을 채우려고 두 번째 "사실"(지어낸
// 계약 상대방 회사명)을 스스로 만들어낼 수밖에 없었습니다. 기사의 실제
// description을 함께 주면 대개 그 자리를 채울 진짜 두 번째 사실을 얻을 수
// 있습니다.
type briefingNewsItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type briefingNewsInput struct {
	Items []briefingNewsItem `json:"items"`
}

// briefingNewsDescriptionMaxRunes는 기사 description 중 실제로 프롬프트에
// 들어가는 분량을 제한합니다. NewsData.io의 description은 길면 수백 자에
// 달하는데, 한 요청에 헤드라인 여러 개 분량이 들어가면 금방 누적됩니다 —
// 240자였을 때 실측 결과 뉴스 브리핑 요청 하나가 6,148토큰까지 늘어나
// (당시 기본 모델이던) llama-3.1-8b-instant의 분당 한도(6,000 TPM)를 단일
// 요청만으로 초과하는 것이 확인되어 100자로 줄였는데, 그 뒤 존댓말 강화·
// 환각 방지 지침이 newsSectionSystemPrompt에 추가되며 프롬프트 총합이
// 다시 2,464토큰까지 늘어난 것이 재확인되어(TestNewsBriefingPromptFitsWithinTokenBudget
// 참고) 80자로 한 번 더 줄였습니다. 요약에는 구체적 사실 하나를 더 뽑아낼
// 만큼의 description만 있으면 충분하지 전체가 필요한 게 아니므로, 문맥을
// 약간 포기하는 대신 요청당 토큰 비용을 실질적으로 낮춥니다.
//
// (2026-08 추가) llama-3.1-8b-instant가 Groq에서 완전히 지원 종료되어
// 기본 모델이 openai/gpt-oss-20b로 바뀌었는데, 이 모델의 실제 분당 한도는
// 8,000 TPM으로(콘솔 문서 기준, 6,000 TPM보다 오히려 33% 여유롭다) 위
// 80자 제한은 여전히 안전하게 여유 있는 값이다 — 새 모델 자체는 이 값을
// 더 줄일 이유를 주지 않으므로 그대로 유지한다.
const briefingNewsDescriptionMaxRunes = 80

// briefingNewsTitleMaxRunes는 title에 대한 상한입니다. description(80자)과
// 달리 title은 "정상적으로 자주 잘리는" 대상이 아니라 방어용 안전판에
// 가깝습니다 — 실제 NewsData.io 헤드라인은 매우 장황한 것도 대개 120자
// 안팎이라(예: "Ontario woman who went missing from Shambhala Music
// Festival in B.C. posts thank you video to rescuers, shares
// details" — 약 118자) 이 상한에 사실상 걸리지 않지만, 극히 드문
// 비정상적으로 긴 제목 하나가 토큰 예산을 조용히 넘겨버리는 사고만
// 막으면 됩니다.
//
// 예전에는 이 값도 80이라 description과 똑같이 취급됐는데, 그 결과 실제
// 헤드라인 제목("...announces $100 million...")이 "$100…"으로 잘려나가는
// 사고가 실제로 보고됐다 — description은 원래 정보가 일부 소실되는 것을
// 감수하는 설계지만, title은 그 자체로 기사 전체의 핵심 사실을 담고
// 있어서 잘리면 요약할 재료 자체가 왜곡된다("belly size" 문제와 달리
// 이번엔 잘못 번역한 게 아니라 원문 입력 자체가 이미 불완전했다). 120으로
// 올려서 위 예시 같은 실측 최장 헤드라인까지는 사실상 전혀 잘리지 않게
// 했다 — 240처럼 더 크게 잡으면(TestNewsBriefingPromptFitsWithinTokenBudget의
// 인위적 최악 시나리오 기준 실측 2,097토큰) 예산 여유가 지나치게 줄어드는데
// 반해, 실제 헤드라인은 어차피 그 정도로 길지 않아 더 올려서 얻는 실익이
// 없다. truncateForPrompt의 extendCutToPreserveNumericToken
// (news_number_annotate.go)이 혹시 이 상한에 걸리는 극단적인 경우에도
// 숫자 표현만큼은 마지막까지 보존한다.
const briefingNewsTitleMaxRunes = 120

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// truncateForPrompt는 truncateRunes와 같은 rune 안전한 하드컷을 하되,
// 실제로 잘라낸 경우에는 마지막 단어 경계(공백)까지 되돌아가서 자르고
// 말줄임표(…)를 붙인다. title/description을 그냥 truncateRunes로 자르면
// 단어나 절 한가운데서 뚝 끊긴 조각이 남는데, 모델이 이 조각을 완결된
// 문장처럼 취급해 억지로 문법을 짜맞추려다 실제로는 무관한 사실을 하나로
// 뒤섞어 붙이는 사례가 보고됐다(예: "…총으로 쏘려고 시도하여 17년에서
// 무기징역을" 같은 비문). 잘렸다는 표시를 명시적으로 남기면, 적어도 그
// 조각이 온전한 문장이 아니라는 신호는 모델에게 전달된다.
func truncateForPrompt(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	// 말줄임표(…) 한 글자가 들어갈 자리를 남겨둬서, 잘라낸 결과가 전체적으로
	// maxRunes를 넘지 않게 한다.
	limit := maxRunes - 1
	if limit < 1 {
		limit = maxRunes
	}
	cutIdx := limit
	// 하드컷 지점이 실제로 단어 중간이었을 때만(잘린 지점 바로 다음 문자가
	// 공백이 아닐 때만) 마지막 공백까지 되돌아간다 — 다만 공백이 너무
	// 앞쪽에만 있다면(예: 첫 단어 자체가 maxRunes보다 길다) 오히려 잘라내는
	// 분량이 너무 많아지므로, 그럴 때는 되돌아가지 않고 원래 하드컷 지점을
	// 그대로 쓴다.
	//
	// 이 "실제로 단어 중간이었을 때만"이라는 조건이 원래 빠져 있었다 —
	// 예전에는 하드컷 지점이 우연히 이미 완전한 단어 경계(바로 다음
	// 글자가 공백)여도 무조건 마지막 공백을 찾아 되돌아갔다. 실제로 보고된
	// 사례: NewsData.io 헤드라인 description "...a record $540.2 million
	// grant..."가 briefingNewsDescriptionMaxRunes(80)에서 공교롭게도
	// "million" 바로 뒤에서 깔끔하게 잘렸는데도, 이 무조건 되돌아가기
	// 로직이 이미 온전한 "million"이라는 단어 전체를 불필요하게 잘라내
	// "...a record $540.2…"만 남겼다. 그러면 annotateNumericUnits가 매칭할
	// 단위(million)가 아예 사라져 "$540.2"가 변환되지 않은 채 그대로
	// 프롬프트에 남았고, 모델이 단위 없는 이 숫자를 스스로 어림잡다
	// "5억"(정답 5.4억과 약 7.4% 차이)을 만들어내 findUngroundedNumber에
	// 근거 없는 숫자로 걸렸다 — 검증기나 숫자 변환 계산 자체의 버그가
	// 아니라, 바로 이 잘린 단어 때문에 애초에 변환할 재료 자체가 없어진
	// 것이 진짜 원인이었다.
	if cutIdx >= len(runes) || runes[cutIdx] != ' ' {
		if idx := lastRuneIndex(runes[:cutIdx], ' '); idx > cutIdx/2 {
			cutIdx = idx
		}
	}
	// 위 단어 경계 보정만으로는 잡지 못하는 또 다른 사고가 있었다: 하드컷
	// 지점이 공교롭게도 "$100" 바로 뒤(다음 글자가 공백)처럼 이미 "깔끔한"
	// 단어 경계에 걸리면 위 보정은 아예 손대지 않는데, 그 뒤에 이어지는
	// " million"이 통째로 잘려나가 "$100…"만 남는 경우다 — 숫자 자체는
	// 살아있지만 단위가 사라지면 annotateNumericUnits가 변환할 재료가
	// 없어지는 것은 위 "$540.2" 사례와 결과적으로 동일하다.
	// extendCutToPreserveNumericToken이 이 경우를 잡아, 잘리는 위치가
	// 숫자+단위(또는 단위 없는 통화 금액) 표현 중간이면 그 표현 전체가
	// 포함되도록 cutIdx를 뒤로 늘린다.
	cutIdx = extendCutToPreserveNumericToken(s, cutIdx)
	return strings.TrimRight(string(runes[:cutIdx]), " ") + "…"
}

func lastRuneIndex(runes []rune, target rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func toBriefingNewsInput(news *NewsData) *briefingNewsInput {
	if news == nil {
		return nil
	}
	items := news.Items
	if len(items) > briefingNewsHeadlineCount {
		items = items[:briefingNewsHeadlineCount]
	}
	result := make([]briefingNewsItem, len(items))
	for i, it := range items {
		result[i] = briefingNewsItem{
			ID:          it.ID,
			Title:       annotateNumericUnits(truncateForPrompt(it.Title, briefingNewsTitleMaxRunes)),
			Description: annotateNumericUnits(truncateForPrompt(it.Description, briefingNewsDescriptionMaxRunes)),
		}
	}
	return &briefingNewsInput{Items: result}
}

// logBriefingPromptSize는 실제 Groq 호출(캐시 미스 시) 전에, 섹션별
// system/user 프롬프트의 대략적인 토큰 수를 로그로 남깁니다. groq.go의
// estimateTokenCount 문서 주석에 있듯 이는 근사치일 뿐이며, 실제 값은
// callGroqChat이 Groq 응답의 usage.prompt_tokens를 그대로 로그로 남기므로
// 그쪽이 진짜 기준입니다 — 이 로그는 어느 섹션/구성 요소가 큰지 사전에
// 가늠해보는 용도입니다.
func logBriefingPromptSize(section, systemPrompt, userContent string) {
	sysTokens := estimateTokenCount(systemPrompt)
	userTokens := estimateTokenCount(userContent)
	log.Printf("[브리핑 프롬프트 크기 추정] %s: system≈%d토큰 user≈%d토큰 합계≈%d토큰 (근사치, 실제 값은 [Groq 응답] 로그의 promptTokens 참고)",
		section, sysTokens, userTokens, sysTokens+userTokens)
}

// logNewsPromptBreakdown은 뉴스 브리핑 프롬프트를 구성하는 각 부분(헤드라인
// 제목/description, 공통 규칙, 뉴스 전용 지침+예시)의 대략적인 토큰 수를
// 개별적으로 로그로 남깁니다 — 6,148토큰까지 늘어나 TPM 한도를 넘겼던
// 문제를 조사하며, 어느 부분이 가장 큰 비중을 차지하는지 확인하기 위해
// 추가했습니다. IT 용어집은 이제 이 프롬프트에 포함되지 않으므로(뉴스
// 카테고리를 IT로 못박지 않기 위해 news_translation.go의 해외 번역
// 프롬프트로만 옮겼습니다) 더 이상 이 breakdown에 등장하지 않습니다.
func logNewsPromptBreakdown(newsInput *briefingNewsInput) {
	if newsInput == nil {
		return
	}
	var titleTokens, descTokens int
	for _, item := range newsInput.Items {
		titleTokens += estimateTokenCount(item.Title)
		descTokens += estimateTokenCount(item.Description)
	}
	commonRulesTokens := estimateTokenCount(briefingCommonRules)
	instructionsAndExampleTokens := estimateTokenCount(newsSectionSystemPrompt) - commonRulesTokens
	log.Printf("[뉴스 브리핑 프롬프트 구성 추정] 헤드라인 %d개: 제목≈%d토큰 description≈%d토큰 / 공통규칙≈%d토큰 뉴스전용지침+예시≈%d토큰",
		len(newsInput.Items), titleTokens, descTokens, commonRulesTokens, instructionsAndExampleTokens)
}

// newsGroundingText는 후보 헤드라인 전체의 title + description을 합친
// 텍스트입니다 — findUngroundedProperNoun이 생성된 문장 속 고유명사로
// 보이는 표현을 이 텍스트와 대조합니다. 모델이 결국 어떤 헤드라인을
// 요약하게 될지 사전에 알 수 없으므로, 특정 헤드라인 하나가 아니라 모든
// 헤드라인의 합집합을 사용합니다.
//
// 항목 사이는 줄바꿈으로 구분합니다("\n") — 다른 검사들(숫자/퍼센트/
// 고유명사)에게는 공백과 동등한 경계일 뿐이라 동작에 영향이 없지만,
// findTopicMismatch는 이 줄바꿈을 기준으로 groundingText를 다시 항목별로
// 쪼개 "헤드라인별 개별 계산 후 최댓값"을 구하는 데 씁니다 — 그 함수의
// 문서 주석 참고.
func newsGroundingText(input *briefingNewsInput) string {
	if input == nil {
		return ""
	}
	var b strings.Builder
	for _, item := range input.Items {
		b.WriteString(item.Title)
		b.WriteString(" ")
		b.WriteString(item.Description)
		b.WriteString("\n")
	}
	return b.String()
}

// newsHallucinationFallback은 재시도 후에도 뉴스 문장이 근거 없는
// 고유명사를 계속 언급할 때 generateSectionText가 대신 사용하는 값입니다 —
// LLM이 생성한 문장을 전혀 쓰지 않고 최상위 헤드라인 원문 그대로를
// 사용하므로, 애초에 아무것도 생성하지 않았기 때문에 hallucination이
// 생길 여지 자체가 없습니다.
func newsHallucinationFallback(news *NewsData) string {
	if news == nil || len(news.Items) == 0 {
		return ""
	}
	return "가장 인기 있는 뉴스: " + news.Items[0].Title
}

func hashJSON(v interface{}) string {
	encoded, _ := json.Marshal(v)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// hashNewsInput은 뉴스 브리핑 캐시 히트 여부를 판단하는 해시를,
// input.Items를 article id 기준으로 정렬한 사본에서 계산한다. NewsData.io가
// 같은 헤드라인 집합을 다른 순서로 돌려주더라도(지금까지 실제로 관측된 적은
// 없지만, API가 순서를 명시적으로 보장하지도 않는다) 콘텐츠 자체는 그대로면
// 해시도 그대로 나오게 하기 위해서다 — 정렬하지 않고 그대로 해시하면, 뉴스
// 내용은 동일한데 순서만 달라져도 캐시 미스로 취급되어 불필요하게 Groq를
// 재호출하게 된다. 실제로 모델에게 보내는 프롬프트(newsJSON/userContent)는
// 원래 순서를 그대로 유지한다 — 정렬은 오직 이 해시 계산에만 적용된다.
func hashNewsInput(input *briefingNewsInput) string {
	if input == nil {
		return hashJSON(input)
	}
	sorted := briefingNewsInput{Items: append([]briefingNewsItem(nil), input.Items...)}
	sort.Slice(sorted.Items, func(i, j int) bool { return sorted.Items[i].ID < sorted.Items[j].ID })
	return hashJSON(sorted)
}

// informalSentenceEndingPattern은 문장 경계("다"/"함"/"임" 바로 뒤에
// 문장부호가 오거나, 문자열이 거기서 끝남)를 찾되, 그 어미 바로 앞 글자도
// 캡처 그룹으로 함께 잡는다 — Go regexp(RE2)는 lookbehind를 지원하지
// 않으므로, "다"로 끝나는 어미가 정중한 합쇼체("~니다")인지는 매칭 이후
// findInformalSentenceEnding이 Go 코드에서 직접 비교해서 판단한다. "다"/
// "함"/"임" 뒤에 경계가 와야만 매칭되므로 "다양한", "다른"처럼 단어
// 중간에 있는 "다"는 걸리지 않는다.
//
// 경계를 "문장부호(.!?) 다음" 또는 "문자열 끝"으로만 한정하고, 예전처럼
// 그냥 공백(\s)이 뒤따르는 것만으로는 경계로 보지 않는다 — "바다"(sea)처럼
// "다"로 끝나는 평범한 명사가 문장 중간에서 공백 앞에 오는 경우(예: "…분쟁이
// 있는 바다 곳곳에서 위협받고 있습니다")까지 문장 종결로 오인해, 실제
// 문장은 "…있습니다"로 정상적인 합쇼체인데도 반말/기사체로 오탐하는 사례가
// 실제로 보고됐다. 문장부호나 문자열 끝을 요구하면 이런 하위 절/구 중간의
// "다"는 걸리지 않으면서도, 실제 문장 종결(마침표 뒤 또는 텍스트 맨 끝)은
// 그대로 잡아낸다.
var informalSentenceEndingPattern = regexp.MustCompile(`([가-힣])(다|함|임)([.!?](\s|$)|$)`)

// findInformalSentenceEnding은 문장이 존댓말(합쇼체 — "~습니다/~합니다/
// ~입니다"처럼 예외 없이 "니다"로 끝나는 어미)이 아니라 반말/기사체 어미
// ("~했다", "~있다", "~이다" 같은 문어체, 또는 "~함"/"~임" 같은 명사형
// 종결)로 끝나는지 검사한다. 뉴스 원문(NewsData.io의 title/description)이
// 이미 "~했다", "~밝혔다" 같은 기사체이다 보니, 모델이 요약하면서 원문의
// 문체를 그대로 따라가 존댓말 지침을 어기는 사례가 실제로 있었다 —
// newsSectionSystemPrompt의 절대 규칙 5번이 이를 프롬프트로 막으려
// 하지만, 이 함수가 마지막 방어선이다.
//
// 한국어 합쇼체 종결어미는 예외 없이 "니다"로 끝나므로("갑니다", "합니다",
// "있습니다", "됐습니다" 등), 문장 경계에서 "다"로 끝나는데 그 앞 글자가
// "니"가 아니면 반말/기사체로 판정한다. "함"/"임"으로 끝나는 명사형
// 종결에는 이런 정중한 대응형이 없으므로 앞 글자와 무관하게 항상 반말로
// 판정한다.
func findInformalSentenceEnding(text string) (string, bool) {
	for _, m := range informalSentenceEndingPattern.FindAllStringSubmatch(text, -1) {
		precedingChar, ending := m[1], m[2]
		if ending == "다" && precedingChar == "니" {
			continue // "~니다" — 정중한 합쇼체, 정상
		}
		return precedingChar + ending, true
	}
	return "", false
}

// bannedBriefingPhrases는 두 종류의 실패를 잡아냅니다: 내용 없는 채우기
// 문장(섹션별 재작성 기능이 애초에 존재하는 이유가 되는 바로 그 문제 —
// "다양한 논의가 진행 중입니다"는 독자에게 아무 정보도 주지 않습니다)과,
// 고정된 합니다체 톤에 어울리지 않는 인터넷 은어입니다. 여기 등록된 것은
// 어떤 맥락에서든 공허하거나 부적절한 표현만입니다 — 예를 들어 "주목받고
// 있습니다"는 실제 구체적 사실 뒤에 붙으면 정당한 마무리 문구가 될 수
// 있으므로 의도적으로 제외했습니다.
//
// 부분 문자열(strings.Contains)로 매칭되므로, 여기에는 그 자체로 이미
// 여러 음절인 온전한 표현만 둔다 — 한두 글자짜리를 넣으면 그 글자를
// 포함하는 무관한 정상 단어까지 오탐한다(과거 "핵"을 여기 뒀을 때 "핵심",
// "핵가족", "결핵"까지 걸렸던 사례 참고). "핵"으로 잡으려던 건 실제로는
// 강조 접두사로 붙는 인터넷 은어 조합이었으므로, 그 구체적인 조합만
// 남긴다. "헐"/"짱"처럼 접두사가 아니라 감탄사로 단독으로 쓰이는 은어는
// bannedBriefingWords에서 단어 경계를 확인해 매칭한다.
var bannedBriefingPhrases = []string{
	"다양한 논의가 진행 중입니다",
	"토론이 활발합니다",
	"관심이 모아지고 있습니다",
	"다양한 의견이 있습니다",
	"여러 가지 논의가 있습니다",
	"많은 관심을 받고 있습니다",
	"ㅋㅋ", "ㅎㅎ", "대박", "레전드", "TMI", "인정?",
	"핵꿀잼", "핵노잼", "핵인싸", "핵존맛", "핵대박", "핵꿀",
}

// bannedBriefingWords는 한 글자짜리라 bannedBriefingPhrases처럼 단순
// 부분 문자열로 매칭하면 무관한 정상 단어(예: "헐값", "짱구")까지
// 걸리는 감탄사성 은어다. findBannedPhrase가 앞뒤 글자가 한글 음절이
// 아닐 때(공백, 문장부호, 문자열 경계)만 매칭해, 다른 단어의 일부가
// 아니라 독립된 단어로 쓰였을 때만 잡아낸다.
var bannedBriefingWords = []string{"헐", "짱"}

func isHangulSyllable(r rune) bool {
	return r >= 0xAC00 && r <= 0xD7A3
}

func findBannedPhrase(text string) (string, bool) {
	for _, phrase := range bannedBriefingPhrases {
		if strings.Contains(text, phrase) {
			return phrase, true
		}
	}

	runes := []rune(text)
	for _, word := range bannedBriefingWords {
		wordRunes := []rune(word)
		for i := 0; i+len(wordRunes) <= len(runes); i++ {
			if string(runes[i:i+len(wordRunes)]) != word {
				continue
			}
			beforeIsBoundary := i == 0 || !isHangulSyllable(runes[i-1])
			afterIsBoundary := i+len(wordRunes) == len(runes) || !isHangulSyllable(runes[i+len(wordRunes)])
			if beforeIsBoundary && afterIsBoundary {
				return word, true
			}
		}
	}

	return "", false
}

// foreignScriptPattern은 한글이 아닌 외국 문자 체계를 폭넓게 매칭합니다 —
// 순수 한국어 응답에는 절대 있어서는 안 되는 문자들입니다. 처음에는
// 한자(중국어/한자와 공유되는 표의문자)와 일본어 가나만 검사했지만
// ("findForeignCJK"), 실제 보고된 사례: 인도 도시 "Ahmedabad"를
// "아마다바드"로 표기하려다 힌디어 데바나가리 문자(अहमदाबाद)가 그대로
// 노출됐다 — 국제 뉴스가 인도·중동·동남아·러시아 등 다양한 지역을 다루는
// 만큼, 그 지역 고유 문자 체계도 함께 대비하도록 검사 범위를 넓혔다(이름도
// "CJK"에서 이 폭을 반영해 바꿨다). 로마자(영어 고유명사)는 이 검사와
// 무관하다 — 로마자 잔존 여부는 findLeakedEnglish가 별도로 담당하므로,
// "Ahmedabad"를 영어 원문 그대로 쓰는 것은 이 검사에 걸리지 않는다. 한글
// 자체(U+AC00-D7A3 완성형 음절, U+1100-11FF / U+3130-318F 자모)는 이
// 범위에 의도적으로 포함되지 않으므로 정상적인 한국어 텍스트는 절대 이
// 패턴에 걸리지 않는다.
var foreignScriptPattern = regexp.MustCompile(`[` +
	`\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}` + // 한자(중국어/한자)
	`\x{3040}-\x{309F}\x{30A0}-\x{30FF}` + // 히라가나/가타카나(일본어)
	`\x{0900}-\x{097F}` + // 데바나가리(힌디어/마라티어/네팔어)
	`\x{0980}-\x{09FF}` + // 벵골 문자(벵골어)
	`\x{0600}-\x{06FF}` + // 아랍 문자(아랍어/페르시아어/우르두어)
	`\x{0590}-\x{05FF}` + // 히브리 문자
	`\x{0E00}-\x{0E7F}` + // 태국 문자
	`\x{0400}-\x{04FF}` + // 키릴 문자(러시아어 등)
	`\x{0370}-\x{03FF}` + // 그리스 문자
	`]`)

func findForeignScript(text string) (string, bool) {
	match := foreignScriptPattern.FindString(text)
	return match, match != ""
}

// latinRunPattern은 "이건 번역됐어야 하는 게 아닐까?" 후보 구간을
// 찾습니다: ASCII 알파벳이 4자 이상 연속되면 흔한 2~3글자 약어를 잘못
// 걸러내지 않으면서도 실제로 새어 나온 영어("fine-tune", "beat", 통째로
// 남은 영어 절 등)는 충분히 잡아낼 수 있습니다.
var latinRunPattern = regexp.MustCompile(`[A-Za-z]{4,}`)

// allowedLatinAbbreviations는 프롬프트 규칙상(그리고 프런트엔드 자체의
// CURRENCY_OPTIONS 목록상) 로마자로 남아 있어야 하는 통화/단위 코드와,
// 흔히 쓰이는 기술/비즈니스 약어 몇 개를 함께 담고 있습니다 — 후자는
// 아래의 findUngroundedProperNoun이 흔한 약어를, 단지 그 약어가 지금
// 요약 중인 헤드라인에 우연히 등장하지 않았다는 이유만으로 지어낸
// 회사명으로 잘못 판정하는 것도 막아줍니다.
var allowedLatinAbbreviations = map[string]bool{
	"usd": true, "krw": true, "eur": true, "jpy": true, "gbp": true, "cny": true,
	"api": true, "gpu": true, "cpu": true, "url": true,
	"ai": true, "ceo": true, "cfo": true, "cto": true, "ipo": true,
	"ev": true, "llm": true, "esg": true, "nft": true, "it": true,
}

// findLeakedEnglish는 한국어여야 할 문장에 번역되지 않은 영어가 그대로
// 남아있는 것으로 보이는, 4자 이상 연속된 로마자 구간 중 첫 번째를
// 찾아 보고합니다 — 의도적으로 원문 그대로 남겨둔 고유명사나 허용된
// 약어는 여기서 제외됩니다. 고유명사(회사/제품/인명)는 프롬프트 규칙상
// 로마자로 남아있어야 하며 관례적으로 대문자로 시작합니다(OpenAI, Opus,
// SlopCodeBench 등) — 반면 전부 소문자로 된 구간은 이름이라기보다는
// 번역됐어야 할 평범한 영어 단어/구문("fine-tune", "beat" 등)일 가능성이
// 훨씬 높습니다.
func findLeakedEnglish(text string) (string, bool) {
	for _, match := range latinRunPattern.FindAllString(text, -1) {
		if allowedLatinAbbreviations[strings.ToLower(match)] {
			continue
		}
		if match[0] >= 'A' && match[0] <= 'Z' {
			continue
		}
		return match, true
	}
	return "", false
}

// repeatedPhraseMinRunes는 의미 있는 신호로 취급할 최소한의 완전 반복
// 길이입니다. 짧은 반복(예: "다양한", "각각")은 정상적인 한국어에서도
// 늘 일어나지만, 두 문장짜리 출력 안에서 이보다 훨씬 긴 구절이 그대로
// 반복된다면 이는 모델이 제대로 문장을 구성하는 게 아니라 루프에 빠졌다는
// 신호입니다 — 실제로 "60.42%의 지분을 보유한 60.42%의 지분을 보유한
// 회사에 대한 지분을 매각했다는" 처럼 11자 이상의 구절이 문장 중간에
// 그대로 반복된 사례가 관측되었습니다.
const repeatedPhraseMinRunes = 10

// repeatedPhraseMaxGapRunes는 같은 부분 문자열의 두 번째 등장이 첫 번째가
// 끝난 지점으로부터 이 안에서 시작해야만 "생성 루프"로 판정하는 최대
// 간격입니다. 실제 보고된 오탐: "Mesa Laboratories" 같은 회사명이 서로
// 다른 두 문장에서 각각 한 번씩 자연스럽게 언급됐을 뿐인데도(두 등장
// 사이에 수십 자 분량의 실제 내용이 있음), 순전히 길이만 보는 예전
// 검사는 이를 반복 루프와 구분하지 못했습니다. 반면 실제 생성 루프
// (예: "60.42%의 지분을 보유한 60.42%의 지분을 보유한")는 같은 구절이
// 사이에 아무 내용 없이 바로 이어 붙어 등장합니다 — 실측 결과 이런
// 경우 간격은 (반복 구절 길이 - repeatedPhraseMinRunes) 수준으로 한
// 자릿수에 불과합니다. 그래서 길이 대신(또는 길이와 별개로) "얼마나
// 가까이 붙어 반복되는가"를 기준으로 삼으면, 자연스러운 재언급과 진짜
// 루프를 훨씬 안정적으로 구분할 수 있습니다.
const repeatedPhraseMaxGapRunes = 8

// sentenceSplitPattern은 findRepeatedSentence가 문장 단위 비교를 위해
// 텍스트를 나누는 데 쓰는, 한국어/영어 공용 문장 종결 부호입니다.
var sentenceSplitPattern = regexp.MustCompile(`[.!?]+`)

// repeatedSentenceMinRunes는 findRepeatedSentence가 "반복된 문장"으로
// 취급할 최소 길이입니다 — 마지막 문장 뒤의 빈 문자열이나 감탄사 하나
// 같은 사소한 조각까지 반복으로 잡지 않기 위함입니다.
const repeatedSentenceMinRunes = 8

// findRepeatedSentence는 마침표/느낌표/물음표로 구분한 완전한 문장이
// 그대로 두 번 이상 등장하는지 확인합니다 — findRepeatedPhrase의 부분
// 문자열 방식과 달리 문장 전체 단위이므로, 회사명 같은 고유명사가 여러
// 문장에 걸쳐 자연스럽게 반복 언급되는 것과는 애초에 겹치지 않습니다
// (그 경우 각 문장은 서로 다르니까요). 완전히 동일한 문장 자체가
// 반복된다면(예: 8B 모델이 사실상 같은 문장을 두 번 만들어내는 경우)
// 그 자체로 명백한 생성 결함입니다.
func findRepeatedSentence(text string) (string, bool) {
	seen := make(map[string]bool)
	for _, s := range sentenceSplitPattern.Split(text, -1) {
		trimmed := strings.TrimSpace(s)
		if len([]rune(trimmed)) < repeatedSentenceMinRunes {
			continue
		}
		if seen[trimmed] {
			return trimmed, true
		}
		seen[trimmed] = true
	}
	return "", false
}

// findRepeatedPhrase는 "모델이 루프에 빠져 문장이 망가지고 있다"를
// 감지하는 범용 검사기로, 여기 있는 다른 검사들과 달리 뉴스 섹션
// 전용이 아니며(날씨/환율 브리핑 텍스트도 루프에 빠질 수 있습니다)
// grounding 텍스트도 필요하지 않습니다. 두 단계로 판정합니다:
//  1. findRepeatedSentence — 완전히 동일한 문장이 통째로 반복되는 경우.
//  2. 길이 repeatedPhraseMinRunes 이상인 부분 문자열이 repeatedPhraseMaxGapRunes
//     이내의 간격을 두고 다시 등장하는 경우 — 같은 구절이 바로 이어
//     붙어 반복되는 "말더듬" 패턴만 잡고, 회사명 등 고유명사가 서로
//     멀리 떨어진 문장에서 자연스럽게 재언급되는 경우는 걸러내지
//     않습니다.
func findRepeatedPhrase(text string) (string, bool) {
	if phrase, found := findRepeatedSentence(text); found {
		return phrase, true
	}

	runes := []rune(text)
	lastEnd := make(map[string]int, len(runes))
	for i := 0; i+repeatedPhraseMinRunes <= len(runes); i++ {
		phrase := string(runes[i : i+repeatedPhraseMinRunes])
		if prevEnd, ok := lastEnd[phrase]; ok && i-prevEnd <= repeatedPhraseMaxGapRunes {
			return phrase, true
		}
		lastEnd[phrase] = i + repeatedPhraseMinRunes
	}
	return "", false
}

// percentNumberPattern은 숫자 바로 뒤에 퍼센트 기호가 붙은 형태(예:
// "60.42%")를 매칭합니다 — findFabricatedPercentage가 모델이 원래
// 퍼센트가 아니었던 다른 수량(가격, 개수 등)을 퍼센트로 재해석해버리는
// 경우를 잡아내는 데 사용됩니다.
var percentNumberPattern = regexp.MustCompile(`\d+(?:\.\d+)?\s?%`)

// findFabricatedPercentage는 원본(title+description)에 퍼센트 기호가
// 전혀 없는데도 생성된 뉴스 문장에 퍼센트가 언급된 경우를 보고합니다 —
// 실제로 "Mercantile Bank Corporation stock hits all-time high at
// 60.42 USD"라는 헤드라인(원본 어디에도 퍼센트 없음)이 "60.42%의
// 지분을 보유한 회사에 대한 지분을 매각했다는 소식"으로 돌아온 사례가
// 있었습니다 — 모델이 숫자 자체(60.42)는 올바르게 재사용했지만, 원본
// 어디에도 없는 지분 매각/퍼센트 서사에 그 숫자를 조용히 갖다 붙인
// 것입니다. findUngroundedNumber만으로는 이걸 잡을 수 없습니다: 그
// 함수는 숫자 값만 비교하는데, 60.42는 실제로 원본에 존재하기
// 때문입니다(다만 "%"가 아니라 "USD"에 붙어 있었을 뿐입니다). 이 검사는
// 의도적으로 보수적입니다 — 원본에 퍼센트 기호가 단 하나도 없을 때만
// 작동하는데, 원본에 퍼센트가 어딘가 언급되어 있다면 단순 부분 문자열
// 비교만으로 특정 퍼센트가 근거가 있는지 판단하는 것은 너무 위험하기
// 때문입니다.
func findFabricatedPercentage(text, groundingText string) (string, bool) {
	if groundingText == "" {
		return "", false
	}
	matches := percentNumberPattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return "", false
	}
	if strings.Contains(groundingText, "%") {
		return "", false
	}
	return matches[0], true
}

// newsProperNounPattern은 생성된 뉴스 문장에서 회사/기관/제품명으로
// 보이는 후보 표현을 매칭합니다 — 영어 Title Case 구간(고유명사는
// findLeakedEnglish가 의존하는 것과 같은 관례에 따라 로마자로 남아있어야
// 합니다), 또는 흔한 기업 접미사로 끝나는 한국어 구절입니다. 이는
// "모델이 특정 기관 이름을 언급했다"를 근사적으로 판단하는 장치일
// 뿐, 진짜 개체명 인식(NER)은 아닙니다 — 우연히 이런 접미사로 끝나는
// 일반 명사(예: "천연가스")는 오탐(false positive)이 될 수 있고, 이런
// 접미사가 전혀 없는 회사명(예: 인명 기반 회사명)은 놓칠 수도 있습니다
// (false negative). 이 패턴은 완전한 NER 시스템을 만들려는 게 아니라,
// 실제로 관측된 실패 유형 — 원본 헤드라인 어디에도 없는 특정 계약
// 상대방 회사를 모델이 지어내는 것 — 을 잡아내기 위해 존재합니다.
var newsProperNounPattern = regexp.MustCompile(
	`[A-Z][a-zA-Z]*(?:\s[A-Z][a-zA-Z]*)*` +
		`|[가-힣]{2,}(?:사|그룹|전자|화학|중공업|건설|증권|은행|카드|보험|캐피탈|모터스|바이오|제약|홀딩스|공사|공단|협회|테크|랩스|에너지|에너빌리티|오일|가스|조선|해양|항공|텔레콤|디스플레이|시스템즈|솔루션|네트웍스|일렉트릭|스틸|로지스틱스|인터내셔널|컴퍼니|코퍼레이션)`,
)

// newsProperNounExemptions는 newsProperNounPattern의 한국어 접미사
// 목록에 우연히 걸리지만 실제로는 회사명이 아닌 일반 명사들입니다 —
// 있을 수 있는 오탐을 전부 나열하려 하기보다는 짧고 구체적인 목록으로만
// 유지합니다.
var newsProperNounExemptions = map[string]bool{
	"천연가스": true, "도시가스": true, "액화천연가스": true, "셰일가스": true,
}

// newsContractCounterpartyPattern은 실제로 보고된 실패 유형을 정확히
// 겨냥합니다: 원본에 전혀 나오지 않은 거래의 "상대방"을 모델이 이름
// 붙여 지어내는 경우입니다. newsProperNounPattern만으로는 "노블리스
// 오일 앤 가스"처럼 여러 단어가 공백으로 구분된 지어낸 이름을 잡을 수
// 없습니다(한국어 분기 패턴은 접미사가 공백 없이 바로 앞 음절에 붙어
// 있어야 하기 때문입니다) — 이 패턴은 공백이 섞여 있더라도 "와/과" 바로
// 앞에 오고 그 뒤에 계약 관련 단어가 바짝 붙어 나오는 한국어/영어/숫자
// 구절이라면 무엇이든 잡아냅니다.
var newsContractCounterpartyPattern = regexp.MustCompile(
	`([가-힣A-Za-z0-9]+(?:\s[가-힣A-Za-z0-9]+){0,4})(?:와|과)\s?(?:계약|파트너십|제휴|협약|MOU|합작)`,
)

// findUngroundedProperNoun은 text 안에서 고유명사처럼 보이는 표현 중,
// groundingText(모든 후보 헤드라인의 title+description을 합친 것 —
// newsGroundingText 참고) 어디에도 등장하지 않는 첫 번째 표현을
// 보고합니다 — 이는 모델이 원본에서 그대로 가져온 이름이 아니라 실제
// 있지도 않은 회사/기관을 지어냈다는 강한 신호입니다. 뉴스 섹션에서만
// 의미가 있으며, 호출하는 쪽에서 빈 groundingText를 넘기면 이 검사
// 자체를 건너뜁니다(날씨/환율에는 애초에 "원본 텍스트"라는 개념이
// 없습니다).
// viaCounterparty가 true면 newsContractCounterpartyPattern(계약 상대방
// 전용 패턴)이 잡아낸 것이고, false면 newsProperNounPattern(일반 고유명사
// 후보) 쪽입니다 — 호출자(validateSectionOutput)가 이 둘을 다르게
// 취급합니다. 계약 상대방 날조(예: "두산에너빌리티 → 노블리스 오일 앤
// 가스")는 원본에 있던 회사가 원본에 없는 새 회사와 거래했다고 지어내는,
// 사실관계 자체를 조작하는 실패라 완화 대상이 될 수 없습니다. 반면
// 일반 고유명사 후보는 "Panthers가 NFL 소속"처럼 원문의 실제 개체에
// 상식적인 소속 정보를 보충한 경우까지 걸러낼 수 있어(생성문에
// findGroundedCoreProperNoun으로 원문의 핵심 개체가 여전히 남아있는지
// 확인해) 완화 여지를 둡니다.
func findUngroundedProperNoun(text, groundingText string) (match string, viaCounterparty bool, found bool) {
	if groundingText == "" {
		return "", false, false
	}

	for _, m := range newsProperNounPattern.FindAllString(text, -1) {
		if allowedLatinAbbreviations[strings.ToLower(m)] {
			continue
		}
		if newsProperNounExemptions[m] {
			continue
		}
		if strings.Contains(groundingText, m) {
			continue
		}
		return m, false, true
	}

	for _, m := range newsContractCounterpartyPattern.FindAllStringSubmatch(text, -1) {
		counterparty := strings.TrimSpace(m[1])
		if counterparty == "" || strings.Contains(groundingText, counterparty) {
			continue
		}
		return counterparty, true, true
	}

	return "", false, false
}

// hasGroundedCoreProperNoun은 groundingText(원문)에서 뽑아낸 고유명사
// 후보 중 적어도 하나가 생성문에 그대로 남아있는지 확인합니다 —
// findUngroundedProperNoun이 재시도 후에도 실패했을 때(단, 계약 상대방
// 날조가 아닌 일반 고유명사 실패에 한해), 완전히 새로운 이름을 지어낸
// 경우와 원문의 실제 개체에 상식적인 소속 정보를 보충했을 뿐인 경우를
// 구분하는 최소한의 기준으로 쓰입니다. 실제 보고된 사례: 원문에 등장한
// "Panthers"가 NFL 소속이라는 것은 일반 상식 수준의 보충 설명이지 없는
// 사실을 지어낸 게 아닌데도, 원문에 "NFL"이라는 리그명이 literal하게
// 없다는 이유만으로 hallucination 취급되어 재시도를 반복하다 폐기됐다.
//
// 완벽한 판별은 아니다 — 원문의 실제 개체 하나가 살아남아 있다는 것이
// "그 문장의 다른 모든 내용도 사실"이라는 보장은 아니다. 하지만
// 화이트리스트를 관리하는 비용보다 훨씬 저렴하고, 실제로 위험한 유형
// (원문의 핵심 개체 자체가 다른 이름으로 통째로 대체되는 경우)은
// findUngroundedProperNoun의 두 번째 루프(계약 상대방 패턴)가 여전히
// 걸러낸다 — 그 실패에는 이 완화가 적용되지 않는다.
func hasGroundedCoreProperNoun(generated, groundingText string) bool {
	if groundingText == "" {
		return false
	}
	for _, candidate := range newsProperNounPattern.FindAllString(groundingText, -1) {
		if allowedLatinAbbreviations[strings.ToLower(candidate)] {
			continue
		}
		if strings.Contains(generated, candidate) {
			return true
		}
	}
	return false
}

// topicTokenPattern은 findTopicMismatch가 비교할 "명사성" 토큰 후보를
// 뽑아내기 위해 공백/구두점 등 비한글·비영문·비숫자 문자로 문장을 자른다.
var topicTokenPattern = regexp.MustCompile(`[가-힣A-Za-z0-9]+`)

// koreanTopicParticles는 명사 뒤에 흔히 붙는 조사들이다 — 원문과 생성문에서
// 같은 단어가 다른 조사를 달고 나타나면(예: "화장품이" vs "화장품을") 토큰이
// 서로 달라 보여 중복도가 실제보다 낮게 나오므로, 비교 전에 벗겨낸다. 긴
// 접미사를 먼저 검사해야 "습니다"가 "이다"보다 먼저 잘려나가는 식의 오매칭을
// 피할 수 있다.
var koreanTopicParticles = []string{
	"에서부터", "이라는", "라는", "으로는", "에게서",
	"에서", "으로", "이나", "라도", "부터", "까지", "이며", "하고", "이랑", "만큼", "처럼", "보다", "입니다", "이다",
	"은", "는", "이", "가", "을", "를", "의", "에", "와", "과", "도", "만", "로", "나",
}

func stripKoreanTopicParticle(token string) string {
	for _, p := range koreanTopicParticles {
		if stem, ok := strings.CutSuffix(token, p); ok && len([]rune(stem)) >= 2 {
			return stem
		}
	}
	return token
}

// extractTopicTokens는 text에서 2글자(rune) 이상인 "명사성" 토큰을 대략
// 추출한다 — 형태소 분석기 없이, 토큰화 후 흔한 조사를 벗겨내는 수준의
// 근사치다. findTopicMismatch가 원문과 생성문이 실제로 같은 소재를 다루고
// 있는지 대조하는 데 쓴다.
func extractTopicTokens(text string) map[string]bool {
	tokens := make(map[string]bool)
	for _, raw := range topicTokenPattern.FindAllString(text, -1) {
		t := stripKoreanTopicParticle(raw)
		if len([]rune(t)) >= 2 {
			tokens[t] = true
		}
	}
	return tokens
}

// topicOverlapMinRatio 미만이면 "원문과 무관한 내용을 새로 지어썼다"고
// 판단한다 — 개별 고유명사/숫자 검사는 지어낸 사실 하나를 정밀하게 잡아내는
// 반면, 이 검사는 훨씬 거친 실패(기사 소재 자체가 통째로 다른 분야로
// 둔갑하는 경우, 예: 청소년 화장품 압수 기사가 AI 모델 관련 문장으로
// 바뀌는 경우)를 잡기 위한 마지막 그물이다. 그런 경우 개별 검사들은 통과할
// 수 있어도 원문과 생성문의 명사성 토큰 집합은 거의 겹치지 않는다.
//
// 0.3 -> 0.15 -> 0.1 순으로 실측하며 낮췄다: "[美특징주]KLA, 1Q 실적
// 가이드라인 실망감 주가 8%↓"처럼 압축된 증권 헤드라인의 정상적인
// 의역들은 실측 중복도가 14~25%로 나왔는데(예: "KLA는 1분기 시가총액이
// 8% 감소했다는 소식이 전해졌습니다"=14%, 더 길게 풀어쓴 의역=25%), 이는
// 실제 hallucination 사례(청소년 화장품 압수 → AI 모델, 중복도 0%)와는
// 거리가 멀다. 0.3/0.15는 여전히 정상적인 의역 중 낮은 쪽을 걸러내 안전
// 문구로 대체시켰다 — 0.1은 실측된 정상 의역의 최저치(14%)보다 낮으면서
// 완전히 다른 주제로 바뀌는 극단적인 경우(중복도 0%)는 여전히 잡아낼 수
// 있는 값이다.
//
// 이 임계값은 "헤드라인 하나"를 기준으로 실측됐고, findTopicMismatch는
// 이제 여러 헤드라인이 입력되어도 항목별로 개별 계산한 뒤 최댓값에 이
// 값을 적용한다(아래 findTopicMismatch 문서 주석 참고) — 즉 매칭된
// 헤드라인 하나에 대해서는 예전과 정확히 같은 수식(overlap/그 헤드라인의
// 토큰 수)이 그대로 적용되므로, 위에서 실측한 0.1이라는 경계값은 항목
// 개수와 무관하게 여전히 유효하다. 임계값을 이보다 높이면(예: 0.15)
// 위에서 실측한 정상 의역의 최저치(14%)가 다시 오탐 대상이 되므로,
// 여러 헤드라인 대응만을 이유로 값을 더 올리지는 않는다.
const topicOverlapMinRatio = 0.1

// hangulSyllablePattern은 groundingText(또는 그 일부)가 한국어인지
// 판별하는 데만 쓰인다 — findTopicMismatch 문서 주석 참고.
var hangulSyllablePattern = regexp.MustCompile(`[가-힣]`)

// findTopicMismatch는 groundingText(newsGroundingText가 헤드라인별로
// "\n"을 구분자로 이어붙인 텍스트)를 다시 헤드라인 단위로 쪼갠 뒤, 각
// 헤드라인의 명사성 토큰 중 생성문에도 남아있는 비율을 개별로 계산하고,
// 그 중 최댓값이 topicOverlapMinRatio 미만이면 보고한다. 뉴스 섹션에서만
// 의미가 있으며, groundingText가 비어 있으면(날씨/환율) 검사 자체를
// 건너뛴다.
//
// 예전에는 후보 헤드라인 전체를 하나로 합친 텍스트 전체를 분모로
// 썼는데, 헤드라인이 여러 개이고 모델이 그중 하나만 정확하게 요약하면
// (여러 후보 중 하나를 고르는 것은 지침상 정상적인 동작이다) 나머지
// 헤드라인들의 토큰이 분모에만 더해져 비율이 항목 수만큼 옅어져 정상
// 사례까지 오탐했다 — 실제 사례: 원유/엔화/CodeRabbit 투자유치라는 서로
// 무관한 헤드라인 3개 중 CodeRabbit 하나만 정확히 요약했는데 전체 대비
// 중복도가 6%로 나와 hallucination으로 오판됨. "생성문이 입력된
// 헤드라인 중 적어도 하나와는 충분히 일치하는가"로 기준을 바꾸면, 하나를
// 골라 요약하는 정상 사례는 그 하나의 헤드라인과 개별 비교했을 때 여전히
// 높은 비율이 나와 정상 통과하고, 반면 어떤 헤드라인과도 무관한 진짜
// hallucination은 모든 헤드라인에 대해 낮은 비율로 나오므로 여전히
// 잡힌다.
//
// 헤드라인 각각에 대해서도 분모는 생성문 토큰 수가 아니라 그 헤드라인
// 자신의 토큰 수다 — 이유는 예전과 동일하다: 압축된 증권 헤드라인의
// 정상적인 의역은 원문에 없던 연결어/서술어를 많이 새로 쓰게 되어,
// 생성문 토큰 수로 나누면 정상 의역까지 낮은 비율로 나와 오탐한다(실측:
// 8~17%). "원문의 핵심 토큰이 생성문에 얼마나 남아있는가"로 보면 정상
// 의역은 비율이 높게, 소재가 통째로 바뀐 경우는 0%로 나온다.
//
// 헤드라인이 한국어가 아니면(해외 모드 — 원문은 영어) 그 헤드라인은
// 계산에서 건너뛴다. 실측 결과, 정확한 번역조차 원문과 정확히 같은
// 문자열을 공유하지 않는다 — 예: "Trump"/"Dulles Airport"가 표기
// 관례에 따라 "트럼프"/"덜레스 국제공항"으로 옮겨지면 원문과 생성문의
// 토큰 문자열 자체가 다르다. 이 검사는 같은 언어 안에서 소재가 통째로
// 바뀌는 것만 잡을 수 있는 근사치라, 번역이 개입하는 순간 항상
// 오탐한다(실측: 정확한 번역인데도 중복도 0~4%). 해외 모드에서 번역
// 자체의 정확성은 findLeakedEnglish/findForeignScript 및
// news_translation.go의 별도 검증이 담당한다. 비교 가능한(한국어)
// 헤드라인이 하나도 없으면(전부 해외 모드거나 groundingText가 비어
// 있으면) 검사 전체를 건너뛴다.
func findTopicMismatch(generated, groundingText string) (float64, bool) {
	if groundingText == "" {
		return 0, false
	}
	genTokens := extractTopicTokens(generated)

	best := 0.0
	compared := false
	for _, headline := range strings.Split(groundingText, "\n") {
		headline = strings.TrimSpace(headline)
		if headline == "" || !hangulSyllablePattern.MatchString(headline) {
			continue
		}
		srcTokens := extractTopicTokens(headline)
		if len(srcTokens) == 0 {
			continue
		}
		compared = true

		overlap := 0
		for t := range srcTokens {
			if genTokens[t] {
				overlap++
			}
		}
		if ratio := float64(overlap) / float64(len(srcTokens)); ratio > best {
			best = ratio
		}
	}
	if !compared {
		return 0, false
	}
	if best < topicOverlapMinRatio {
		return best, true
	}
	return best, false
}

// errBriefingValidationFailed는 한 번 재시도한 뒤에도 여전히 강한
// 콘텐츠 검증(비한글 외국 문자, 새어나온 영어, 근거 없는 숫자)을 통과하지
// 못한 섹션을 표시합니다 — resolveBriefingSection은 이를 일반적인
// 생성 오류와 다르게 처리합니다: 되돌아갈 오래된 캐시가 없다면 해당
// 섹션을 조용히 생략하는 대신 명시적으로 "⚠️ 생성 실패"를 표시합니다.
var errBriefingValidationFailed = errors.New("briefing section failed content validation")

// errBriefingDataMissing은 섹션의 원본 데이터(WeatherData/ExchangeData/
// NewsData)가 애초에 없거나(nil) 비어있어서(뉴스는 Items가 0개) Groq를
// 호출할 근거 자체가 없는 상태를 표시합니다 — resolveBriefingSection은
// 이 경우 Groq를 아예 호출하지 않고 곧바로 errBriefingValidationFailed와
// 동일한 방식(되돌아갈 캐시가 있으면 stale_fallback, 없으면 명시적 실패
// 문구)으로 처리합니다. 실제로 보고된 사례: NewsData.io 조회가 context
// deadline exceeded로 실패해 news가 nil인 채로 getBriefing에 전달됐는데,
// 이 가드가 없어서 "[뉴스 데이터]: null"이라는 무의미한 프롬프트가 그대로
// Groq에 전달되고, groundingText가 비어 hallucination 검사기들마저
// 무력화된 상태로 의미 없는 응답을 만들어냈다.
var errBriefingDataMissing = errors.New("briefing section source data unavailable")

// koreanMagnitudeUnits는 한국어가 큰 수를 자릿수별로 묶을 때 쓰는
// 접미사입니다(서양식 천 단위 콤마 표기와 대응됩니다) — "1200만"
// (12,000,000)이나 "90억"(9,000,000,000) 같은 표기를 비교 가능한
// float64 값으로 파싱하는 데 필요합니다.
var koreanMagnitudeUnits = []struct {
	suffix     string
	multiplier float64
}{
	{"조", 1e12},
	{"억", 1e8},
	{"만", 1e4},
}

// koreanNumberPattern은 소수(및 서양식 천 단위 쉼표 표기, 예: "3,500",
// "1,000.5")를 포함한 숫자 뒤에 한국어 자릿수 접미사가(있다면) 바로
// 이어지는 형태를 매칭합니다. 쉼표 그룹 쪽 대안을 먼저 두어야, "3,500"이
// "3"과 "500"이라는 서로 무관한 두 숫자로 쪼개지지 않고 하나의 토큰으로
// 매칭됩니다(Go regexp는 알아본 대안을 왼쪽부터 우선 시도합니다) — 쉼표
// 그룹은 정확히 3자리씩만 인정해서("3,500", "1,234,567"), "3,50"처럼
// 불완전한 그룹은 숫자 하나로 오인하지 않습니다. 실제 보고된 사례: 원문
// "$3,500"이 이 쉼표 처리 없이는 [3, 500] 두 숫자로 추출되어, 응답의
// "3500"(쉼표 없는 표기, 동일한 값)이 둘 중 어느 쪽과도 매칭되지 않아
// 근거 없는 숫자로 오탐됐습니다.
var koreanNumberPattern = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?(?:만|억|조)?|\d+(?:\.\d+)?(?:만|억|조)?`)

// extractNumbers는 text에 언급된 모든 숫자를, 표기 방식(쉼표 유무, 만/억/조
// 배수)과 무관하게 실제 수치 값(float64)으로 정규화해서 반환합니다 —
// 쉼표는 제거하고, 만/억/조 접미사는 배수를 적용해서 "90억"과
// "9000000000"이, "3,500"과 "3500"이 모두 같은 값으로 비교되게 합니다.
// 원본 데이터(헤드라인 제목)에서 "정답" 숫자를 읽어낼 때와, 생성된
// 문장이 실제로 어떤 숫자를 주장하는지 확인할 때 모두 사용되며, 호출자
// (findUngroundedNumber)는 이렇게 정규화된 값끼리 numbersMatch로
// 비교합니다 — 원본 문자열 표기를 그대로 비교하는 곳은 없습니다.
func extractNumbers(text string) []float64 {
	matches := koreanNumberPattern.FindAllString(text, -1)
	result := make([]float64, 0, len(matches))
	for _, m := range matches {
		multiplier := 1.0
		numPart := m
		for _, unit := range koreanMagnitudeUnits {
			if strings.HasSuffix(m, unit.suffix) {
				multiplier = unit.multiplier
				numPart = strings.TrimSuffix(m, unit.suffix)
				break
			}
		}
		numPart = strings.ReplaceAll(numPart, ",", "")
		val, err := strconv.ParseFloat(numPart, 64)
		if err != nil {
			continue
		}
		result = append(result, val*multiplier)
	}
	return result
}

// numbersMatch는 숫자가 주어지는 방식(예: float64 값)과 모델이 실제로
// 써내는 방식(예: "약 90억") 사이의 반올림/표기 차이는 허용하면서도,
// 진짜로 다른 숫자 — 즉 hallucination — 는 그대로 불일치로 취급합니다.
// 작은 절대 오차 허용치는 반올림된 소수(26.75 대 "26.8")를 커버하고,
// 작은 상대 오차 허용치는 반올림된 큰 자릿수 값(9,012,345,678에 대한
// "약 90억")을 커버합니다.
func numbersMatch(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff <= 0.05 {
		return true
	}
	larger := math.Max(math.Abs(a), math.Abs(b))
	return larger > 0 && diff/larger < 0.02
}

// sportsRoundExceptions는 영어 원문에서는 숫자 없이("quarters",
// "semifinals" 등) 표현되지만 한국어에서는 관용적으로 숫자를 붙여
// 옮기는("8강", "4강") 스포츠 대회 라운드 용어를 매핑합니다 —
// findUngroundedNumber가 이런 정당한 번역까지 "지어낸 숫자"로 오탐하지
// 않도록 하기 위한 예외 목록입니다. 실제 사례: 원문 "Shelton sweeps
// Fonseca to reach Montreal quarters"를 "몬트리올 8강에 진출"로 정확히
// 번역했는데도, 원문에 literal한 "8"이 없다는 이유로 근거 없는 숫자로
// 판정되어 재시도만 반복하다 결국 생성 실패로 처리됐다.
//
// groundingText(원문)에 영어 키워드 중 하나라도 등장하면, 생성문의
// 대응하는 한국어 숫자는 근거 있는 것으로 간주합니다. "결승"(final/
// finals)은 애초에 숫자를 쓰지 않는 표현이라 이 검사와 무관하므로
// (참고용으로만 언급) 목록에 넣지 않습니다.
var sportsRoundExceptions = []struct {
	englishTerms []string
	koreanNumber float64
}{
	{englishTerms: []string{"quarterfinal", "quarters"}, koreanNumber: 8},
	{englishTerms: []string{"semifinal", "semis"}, koreanNumber: 4},
	{englishTerms: []string{"round of 16"}, koreanNumber: 16},
	{englishTerms: []string{"round of 32"}, koreanNumber: 32},
}

// isGroundedSportsRoundNumber는 num이 sportsRoundExceptions에 등록된
// 라운드 숫자이면서, 그 라운드를 가리키는 영어 용어가 groundingText(원문)에
// 실제로 등장하는지 확인합니다 — 원문과 무관하게 아무 숫자에나 면죄부를
// 주지 않도록, 반드시 원문에 해당 용어가 있어야만 근거 있는 것으로
// 인정합니다.
func isGroundedSportsRoundNumber(num float64, groundingText string) bool {
	if groundingText == "" {
		return false
	}
	lower := strings.ToLower(groundingText)
	for _, ex := range sportsRoundExceptions {
		if !numbersMatch(num, ex.koreanNumber) {
			continue
		}
		for _, term := range ex.englishTerms {
			if strings.Contains(lower, term) {
				return true
			}
		}
	}
	return false
}

// extractEnglishUnitNumbers는 groundingText(원문 영어 헤드라인/description)에서
// numericUnitPattern으로 잡히는 "숫자+단위" 표현("£25bn", "$6.6bn", "25
// million", "£16m")을 찾아, parseNumericUnitMatch(news_number_annotate.go)로
// annotateNumericUnits와 완전히 같은 배수·예외 규칙을 적용한 값으로
// 반환합니다 — 사전 변환 단계와 똑같은 계산을 검증 단계에서 원문을
// 대상으로 한 번 더 수행하는 것입니다. 두 곳이 같은 함수를 공유하므로,
// "bn은 예외 처리했는데 m은 빠뜨림" 같은 재발이 구조적으로 어렵습니다.
//
// 정상적인 흐름이라면 briefingNewsInput.Items는 이미 annotateNumericUnits를
// 거친 뒤라 groundingText에는 "bn"/"million" 같은 원문 단위 표기가 전혀
// 남아있지 않고 "250억"처럼 이미 변환된 한글 숫자만 있어야 합니다 — 그
// 경우 allowedNumbers 쪽 extractNumbers가 이미 250억을 뽑아내므로 이
// 함수는 아무것도 찾지 못한 채 지나갑니다. 이 함수는 그 사전 변환이
// 놓친 경우(예: description이 잘리며 단위 글자까지 잘려나간 경우, 또는
// annotateNumericUnits가 아직 인식하지 못하는 새 표기)에 대한 이중
// 방어선입니다 — "£25bn"이 무슨 이유로든 원문 그대로 남아있어도, 생성문의
// "250억"을 여전히 근거 있는 값으로 인정할 수 있게 합니다.
func extractEnglishUnitNumbers(groundingText string) []float64 {
	if groundingText == "" {
		return nil
	}
	var result []float64
	for _, m := range numericUnitPattern.FindAllStringSubmatch(groundingText, -1) {
		value, _, ok := parseNumericUnitMatch(m)
		if !ok {
			continue
		}
		result = append(result, value)
	}
	return result
}

// findUngroundedNumber는 text에 언급된 숫자 중, allowedNumbers 안의
// 어떤 숫자와도 (numbersMatch의 오차 범위 내에서) 대응되지 않고,
// sportsRoundExceptions에도, groundingText의 단위 표기를 환산한
// extractEnglishUnitNumbers에도 해당하지 않는 첫 번째 숫자를 반환합니다
// — 즉, 모델이 주어진 데이터에서 가져온 게 아니라 지어낸 것으로 보이는
// 수치입니다. 이 검사는 예를 들어 헤드라인의 평범한 "$500"이 "1200만
// 달러"로 둔갑해 돌아오는 경우를 잡아냅니다: 여기엔 annotateNumericUnits가
// 처리할 K/M/B 축약 자체가 없으므로, 이 검사가 모델이 숫자를 그냥 잘못
// 읽거나 지어내는 것을 막는 마지막 방어선입니다.
//
// allowedNumbers에는 각 섹션이 다루는 데이터 값뿐 아니라 그 섹션의
// 프롬프트 문구 자체에 고정으로 박혀 있는 숫자도 포함되어야 합니다 —
// 예를 들어 날씨 프롬프트는 항상 "오전 8시"/"오후 2시"를 언급하고
// 환율 프롬프트는 항상 "지난 7일간"을 언급하므로, 8, 2, 7을 미리
// 허용해두지 않으면 모든 응답이 이 검사에 잘못 걸리게 됩니다.
//
// groundingText는 뉴스 섹션에서만 의미가 있습니다(날씨/환율은 빈 문자열을
// 넘깁니다) — isGroundedSportsRoundNumber/extractEnglishUnitNumbers가
// 각각의 예외를 판별하는 데만 사용하며, 그 외의 숫자 검증 로직에는
// 영향을 주지 않습니다.
func findUngroundedNumber(text, groundingText string, allowedNumbers []float64) (float64, bool) {
	unitNumbers := extractEnglishUnitNumbers(groundingText)
	for _, found := range extractNumbers(text) {
		matched := false
		for _, allowed := range allowedNumbers {
			if numbersMatch(found, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			for _, unitNum := range unitNumbers {
				if numbersMatch(found, unitNum) {
					matched = true
					break
				}
			}
		}
		if !matched && isGroundedSportsRoundNumber(found, groundingText) {
			matched = true
		}
		if !matched {
			return found, true
		}
	}
	return 0, false
}

// ungroundedNumberReasonPattern은 validateSectionOutput이
// findUngroundedNumber의 결과를 포맷팅한 문자열("근거 없는 숫자
// 감지(1e+08)")에서 감지된 숫자 부분만 다시 뽑아낸다 —
// generateSectionText가 재시도 사이에 감지된 숫자 자체가 바뀌는지
// 비교하려면 이 문자열이 필요한데, findUngroundedNumber를 다시 호출하지
// 않고 이미 계산된 reason 문자열을 재사용하는 편이 검증 로직을 중복
// 실행하지 않아도 된다.
var ungroundedNumberReasonPattern = regexp.MustCompile(`^근거 없는 숫자 감지\((.+)\)$`)

func extractUngroundedNumberFromReason(reason string) (string, bool) {
	m := ungroundedNumberReasonPattern.FindStringSubmatch(reason)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// weatherFixedNumbers/exchangeFixedNumbers는 데이터가 아니라 각 섹션의
// 프롬프트 문구 자체에 고정으로 들어있는 숫자입니다 —
// findUngroundedNumber의 문서 주석 참고.
var weatherFixedNumbers = []float64{8, 2}  // "오전 8시", "오후 2시"
var exchangeFixedNumbers = []float64{7, 1} // "지난 7일간", "1 {from}당 ..."

func allowedWeatherNumbers(input *briefingWeatherInput) []float64 {
	if input == nil {
		return weatherFixedNumbers
	}
	nums := append(append([]float64{}, weatherFixedNumbers...), input.Current.TemperatureC)
	if input.Today.Morning != nil {
		nums = append(nums, input.Today.Morning.TemperatureC)
	}
	if input.Today.Afternoon != nil {
		nums = append(nums, input.Today.Afternoon.TemperatureC)
	}
	return nums
}

func allowedExchangeNumbers(input *briefingExchangeInput) []float64 {
	if input == nil {
		return exchangeFixedNumbers
	}
	// input.BaseUnits는 JPY의 "100 JPY당" 표기를 커버합니다 — 다른 통화쌍은
	// 전부 1입니다(exchangeFixedNumbers 자체의 1과 이미 중복되지만 문제
	// 없습니다).
	nums := append(append([]float64{}, exchangeFixedNumbers...), input.Rate, input.BaseUnits)
	if input.Trend != nil {
		nums = append(nums, math.Abs(input.ChangePercent))
	}
	if input.SevenDaysAgoRate != 0 {
		nums = append(nums, input.SevenDaysAgoRate)
	}
	return nums
}

// allowedNewsNumbers는 전달된 모든 헤드라인의 숫자를 모아 둡니다(모델이
// 결국 어떤 헤드라인을 고를지는 생성이 끝나야 알 수 있으므로, 특정
// 헤드라인 하나만이 아니라 전체를 대상으로 합니다).
func allowedNewsNumbers(input *briefingNewsInput) []float64 {
	if input == nil {
		return nil
	}
	var nums []float64
	for _, item := range input.Items {
		nums = append(nums, extractNumbers(item.Title)...)
		nums = append(nums, extractNumbers(item.Description)...)
	}
	return nums
}

// 이 프롬프트들(briefingCommonRules + 섹션별 지침)은 세 섹션 모두가
// 캐시 미스마다 매번 전송되므로 토큰 비용이 3배로 누적된다. 뉴스 프롬프트가
// 실측 6,148토큰, 이후 2,464토큰까지 두 차례 커져 (당시 기본 모델이던)
// llama-3.1-8b-instant의 분당 한도(6,000 TPM)를 위협한 전례가 있어서
// (반복될 때마다 프로덕션 로그로 뒤늦게 발견해 수동으로 다시 압축해야
// 했다), 총 토큰 수는
// TestWeatherBriefingPromptFitsWithinTokenBudget/
// TestExchangeBriefingPromptFitsWithinTokenBudget/
// TestNewsBriefingPromptFitsWithinTokenBudget(briefing_section_test.go)으로
// 고정해두었다. 새 실패 유형을 발견했을 때는 여기 문장을 늘리기 전에
// 먼저 validateSectionOutput에 검사기를 추가해 해결할 수 없는지 검토할
// 것 — 검사기는 실행 시점에만 비용이 들지만 프롬프트 문구는 매 요청마다
// 토큰 비용이 든다. 규칙을 추가/수정했다면 위 테스트를 반드시 통과시켜야
// 한다.
//
// (과거에는 simple/detailed 두 문장을 JSON으로 함께 요청했으나, 출력
// 토큰과 프롬프트 복잡도를 줄이기 위해 detailed 하나의 순수 텍스트만
// 반환하도록 단순화했다 — callGroqChat도 jsonMode=false로 호출한다.)
const briefingCommonRules = `공통 규칙:
- [최우선] 응답은 반드시 순수 한국어로만 — 한자·중국어·일본어 문자는 단 하나도 쓰지 마세요. 입력에 영어 문장과 이미 계산된 한글 숫자 표기가 섞여 있어도(예: "revenue of 6010만 달러 misses") 그 숫자 값은 그대로 쓰되 문장 전체를 완전한 한국어로 재구성하세요 — 영어 단어를 그대로 옮기지 말고, 절대 한자·일본어로 바꿔 쓰지 마세요(숫자, USD/KRW 같은 약어, 고유명사는 예외).
- 항상 합니다체(존댓말)로 — 반말·기사체·인터넷 은어·이모지 금지.
- 마크다운·제목 없이 순수 문장만.
- 데이터에 없는 내용을 지어내지 마세요.
- 같은 구절을 문장 안에서 반복하지 마세요.
- 문장 텍스트만 그대로 출력 — 따옴표·설명·코드블록 금지.

문장 수: 부연할 데이터가 있으면 2문장, 없으면 1문장 — 채우려고 지어내지 마세요.`

// umbrellaAdvice는 Go에서 미리 계산되어 있으니(computeUmbrellaAdvice 참고)
// 모델은 그 값을 그대로 따르기만 하면 된다 — precipProbability를 직접
// 판단하게 하면 8B 모델이 틀린다(실측: 강수확률 22%에서도 비가 온다고
// 지어낸 사례).
const weatherSectionSystemPrompt = briefingCommonRules + `

당신은 날씨 데이터로 하루 브리핑의 날씨 문장을 쓰는 비서입니다.
- current.cityLabel을 정확히 그대로 사용하세요(다른 도시로 착각 금지).
- today.morning=오전 8시, today.afternoon=오후 2시 값입니다. 필드가 없으면 그 시간대는 아예 언급하지 말고(0 등으로 지어내지 말 것) 있는 필드만으로 문장을 구성하세요.
- umbrellaAdvice.needed가 true면 period를 언급하며 우산을 권하고, false면 비/우산을 언급하지 말고 다른 실용 조언을 하세요 — 이 값은 이미 계산되어 있으니 precipProbability를 직접 판단하지 마세요.
- 첫 문장: (있다면) 오전 8시/오후 2시 날씨, 없으면 현재 날씨. 두 번째 문장: 존재하는 시간대의 temperatureC로 "오전 8시엔 X도, 오후 2시엔 Y도" 형태(숫자는 실제 값으로 치환).

예시: 서울은 오늘 대체로 맑아 우산 없이 외출하기 좋은 날씨입니다. 오전 8시엔 18도, 오후 2시엔 23도이며 맑은 하늘이 이어집니다.`

// trend(direction/implication)도 computeExchangeTrend가 미리 계산해두므로,
// 모델은 상승/하락과 약세/강세를 스스로 짝짓지 않고 주어진 값을 그대로
// 쓰기만 하면 된다 — 실측 결과 8B 모델은 하락을 강세와 짝짓는(정반대)
// 오류를 냈다.
const exchangeSectionSystemPrompt = briefingCommonRules + `

당신은 환율 데이터로 하루 브리핑의 환율 문장을 쓰는 비서입니다.
- rate/baseUnits/baseCurrency/quoteCurrency는 이미 계산되어 있으니 그대로 사용하세요(뒤집거나 재계산 금지). baseUnits가 100이면 반드시 "100 JPY당"처럼 쓰고 "1 JPY당"으로 쓰지 마세요.
- 첫 문장은 "환율은"으로 시작해 "{baseUnits} {baseCurrency}당 {rate} {quoteCurrency}입니다" 형태로 현재 수준과 실용 조언만 쓰세요 — 상승/하락은 직접 판단하지 말고 trend 값을 그대로 따르세요.
- trend가 있으면 두 번째 문장에 changePercent/trend.direction/trend.implication을 그대로 사용해 추세를 반드시 포함하세요. trend가 없으면 1문장만 쓰세요.

예시: 환율은 1 USD당 1320.55 KRW입니다. 지난 7일간 환율은 1.3% 하락해 원화가 소폭 강세를 보이고 있습니다.
예시(JPY): 환율은 100 JPY당 905.00 KRW입니다. 지난 7일간 환율은 0.5% 상승해 엔화가 소폭 강세를 보이고 있습니다.`

// 국내/해외 모든 카테고리가 공유하는 범용 페르소나여야 한다 — 특정
// 분야로 못박으면 무관한 기사까지 왜곡된 전례가 있다(과거 IT 용어집이
// 여기 포함돼 있었을 때 "청소년 화장품 압수" 기사가 AI 관련 내용으로
// 왜곡됨). 용어집은 실제로 번역이 필요한 news_translation.go의
// newsTranslationSystemPrompt에만 남겼다. 뉴스는 헤드라인이 요청마다
// 함께 실려 다른 두 섹션과 달리 입력 크기가 매 요청 늘어날 수 있으므로,
// 개수/길이는 briefingNewsHeadlineCount·briefingNewsTitleMaxRunes·
// briefingNewsDescriptionMaxRunes로 별도 제한한다.
//
// 규칙 5번(의학/과학/법률 전문 용어)은 실제 보고된 새로운 유형의 CJK
// 유출 사례에서 나왔다: "belly size beats BMI at predicting heart
// attacks" 헤드라인을 다루다가 "배圍"(한글 "배" + 한자 "圍"가 뒤섞인,
// 어느 언어에도 존재하지 않는 표현)가 생성됐다. 기존의 4번 규칙("일본어·
// 중국어식 음차 금지")은 회사명 등 고유명사를 소리 나는 대로 옮기려다
// 가나/한자가 섞이는 것을 막기 위한 규칙이라 이 사례를 커버하지
// 못한다 — 이번 실패는 고유명사가 아니라 "허리둘레"의 한자어 표현인
// 腹圍(복위)처럼, 흔히 한자로도 표기되는 전문 용어를 무리하게 정확히
// 옮기려다 생긴 실패이기 때문이다. findForeignScript가 사후에 이미
// 걸러내고 있었지만(validateSectionOutput 최우선 검사), 검증만으로는
// 재시도 후에도 같은 헤드라인이 다시 선택되면 같은 실패가 반복될 수
// 있어(pickNewsItemToExclude가 매번 정확히 이 헤드라인을 제외 대상으로
// 골라준다는 보장이 없다), 생성 단계에서부터 이런 시도 자체를 막고
// 실패 시 전문 용어를 억지로 살리는 대신 쉬운 말로 풀어 쓰도록 유도하는
// 규칙을 추가했다.
const newsSectionSystemPrompt = briefingCommonRules + `

당신은 뉴스 헤드라인(title/description) 하나를 골라 브리핑 문장을 쓰는 비서입니다. 카테고리를 바꾸지 말고 숫자·명칭이 있는 항목을 우선 고르세요.

절대 규칙:
1. 원문에 없는 사건·인물·회사명·계약 상대방을 지어내거나 숫자를 다른 단위(예: %)로 바꿔치기하지 마세요.
2. 구체적 사실(숫자·명칭) 1개 이상 포함 — "다양한 논의가 진행 중입니다" 같은 빈 문장 금지. K/M/B는 이미 한국어로 환산되어 있으니 그 표기 그대로 쓰세요 — 억/만으로 다시 쪼개지 마세요(자릿수를 틀리기 쉽습니다).
3. 원문이 기사체("~했다")여도 반드시 합니다체로 재작성하세요.
4. 영어 고유명사(회사명·제품명)는 외래어 표기법에 맞는 한글이나 영어 원문 그대로만 쓰세요 — 일본어·중국어식 음차 금지.
5. 의학·과학·법률 등 전문 용어도 한자를 섞지 말고 한글로만 쓰세요(예: "belly size" → "배 둘레", 한자 "腹圍" 금지). 한글로 옮기기 애매하면 억지로 옮기지 말고 쉬운 말로 풀어 쓰세요.
6. title이나 description이 말줄임표(…)로 끝나 문장이 불완전하면, 그 안의 숫자·세부 정보를 추측해서 채우지 마세요 — 명시된 부분까지만 쓰거나 그 항목은 간략히만 언급하세요.
7. 인도·중동·동남아·러시아 등 영어권이 아닌 지명·인명을 표기할 때도 그 지역 고유 문자(힌디어 데바나가리, 아랍 문자, 태국 문자, 키릴 문자 등)를 절대 섞지 마세요 — 한글 표기(외래어 표기법에 맞게) 또는 영어 원문 그대로만 쓰세요. 예: "Ahmedabad" → "아마다바드" 또는 "Ahmedabad" 그대로, 힌디어 문자(अहमदाबाद)는 절대 쓰지 마세요.

예시: 한 스타트업이 5000만 달러 투자를 유치했습니다.`

// maxSectionRegenerations는 어떤 검사(또는 몇 개의 검사)가 실패했는지와
// 무관하게, 섹션 하나의 콘텐츠 검증 전체 예산을 공유되는 재시도 한 번으로
// 제한합니다. 이전에는 강한 실패(비한글 외국 문자/새어나온 영어/근거 없는 숫자/근거
// 없는 고유명사)와 약한 금칙어 검사가 각자 독립적으로 재시도 횟수를
// 추적했기 때문에, 시도마다 다른 검사가 번갈아 실패하면 섹션 하나에
// Groq 호출이 최대 3번까지 들 수 있었습니다. 전체를 합쳐 재시도 1회로
// 제한하면(각 시도에서도 모든 검사는 여전히 전부 실행됩니다) 섹션 하나에
// Groq 호출이 절대 2번을 넘지 않으며, 그 단 한 번의 재시도는 같은 모델을
// 다시 굴리는 데 쓰이는 게 아니라 더 정확한 모델로 승격하는 데 쓰입니다.
const maxSectionRegenerations = 1

// validateSectionOutput은 생성된 섹션에 대해 모든 콘텐츠 검사를 고정된
// 우선순위 순서(비한글 외국 문자 -> 영어 유출 -> 반복 구문 -> 근거 없는 숫자 -> 주제
// 불일치 -> 조작된 퍼센트 -> 근거 없는 고유명사 -> 반말/기사체 어미 ->
// 금칙 문구)로 실행하고 처음 발견된 실패를 보고합니다. hardFailure는
// "절대 그대로 내보낼 수 없는" 실패(비한글 외국 문자 오염, 새어나온 영어, 근거 없는
// 숫자, 주제 불일치, 근거 없는 고유명사, 반말/기사체 어미)와 "약한"
// 금칙어 검사를 구분하며, 이 값에 따라 resolveBriefingSection의 호출자가
// 재시도 예산 소진을 오류(및 stale_fallback으로 대체)로 취급할지 아니면
// 마지막 결과를 그대로 내보낼지가 결정됩니다. 반말/기사체 어미는 톤
// 문제이긴 하지만 금칙어(인터넷 은어)보다 훨씬 눈에 띄는 house-style
// 위반이라, 재시도 후에도 남아있으면 마지막 결과를 그냥 내보내는 대신
// (다른 hardFailure들처럼) stale_fallback으로 넘어가도록 hardFailure로
// 분류합니다.
// useFallback은 (뉴스 전용) 주제 불일치/퍼센트 조작/고유명사 검사에서만
// true가 됩니다 — generateSectionText 참고.
// lenientIfCoreNounSurvives는 오직 findUngroundedProperNoun이 계약
// 상대방 패턴이 아니라 일반 고유명사 후보 루프에서 실패를 잡아냈을
// 때만 true입니다 — generateSectionText가 재시도까지 소진한 뒤,
// hasGroundedCoreProperNoun으로 원문의 핵심 개체가 생성문에 남아있는지
// 추가로 확인해 완화 여부를 결정하는 데 씁니다(그 함수의 문서 주석
// 참고). 계약 상대방 날조는 이 값이 항상 false라 완화 대상이 아닙니다.
func validateSectionOutput(combined string, allowedNumbers []float64, groundingText string) (reason string, hardFailure, useFallback, lenientIfCoreNounSurvives bool) {
	if match, found := findForeignScript(combined); found {
		return fmt.Sprintf("비한글 외국 문자 감지(%q)", match), true, false, false
	}
	if match, found := findLeakedEnglish(combined); found {
		return fmt.Sprintf("번역되지 않은 영어(%q) 감지", match), true, false, false
	}
	if phrase, found := findRepeatedPhrase(combined); found {
		return fmt.Sprintf("반복되는 구절(%q) 감지(생성 루프 의심)", phrase), true, false, false
	}
	if num, found := findUngroundedNumber(combined, groundingText, allowedNumbers); found {
		return fmt.Sprintf("근거 없는 숫자 감지(%v)", num), true, false, false
	}
	if ratio, found := findTopicMismatch(combined, groundingText); found {
		return fmt.Sprintf("원문과 무관한 주제로 생성된 것으로 의심됨(토큰 중복도 %.0f%%)", ratio*100), true, true, false
	}
	if match, found := findFabricatedPercentage(combined, groundingText); found {
		return fmt.Sprintf("원문에 없는 퍼센트(%q) 감지(hallucination 의심)", match), true, true, false
	}
	if match, viaCounterparty, found := findUngroundedProperNoun(combined, groundingText); found {
		if viaCounterparty {
			return fmt.Sprintf("원문에 없는 계약 상대방(%q) 감지(hallucination 의심)", match), true, true, false
		}
		return fmt.Sprintf("원문에 없는 고유명사(%q) 감지(hallucination 의심 — 원문 핵심 개체가 남아있으면 완화됨)", match), true, true, true
	}
	if ending, found := findInformalSentenceEnding(combined); found {
		return fmt.Sprintf("존댓말이 아닌 문장 종결(%q) 감지 — 원문 기사체를 그대로 따라간 것으로 의심됨", ending), true, false, false
	}
	if phrase, softViolated := findBannedPhrase(combined); softViolated {
		return fmt.Sprintf("금칙어 감지(%q)", phrase), false, false, false
	}
	return "", false, false, false
}

// generateSectionText는 단일 섹션에 대해 model(호출자가 고른 저렴하고
// 쿼터 여유가 큰 모델 — frequentGroqModel 참고)로 시작해 Groq를
// 호출하고, 결과를 validateSectionOutput으로 검증합니다. 강한 실패든
// 약한 실패든 어떤 실패가 나든 섹션에 주어진 단 한 번의
// maxSectionRegenerations 재시도를 소모하며, 이때는 같은 모델에 같은
// 질문을 다시 던지는 게 아니라 escalationGroqModel()로 승격합니다. 그
// 재시도마저 소진되면:
//   - 강한 실패는 errBriefingValidationFailed를 반환합니다. 단,
//     hallucinationFallback이 비어 있지 않은 근거 없는 고유명사 실패는
//     예외로 오류 대신 그 fallback을 반환합니다 — hallucination된
//     회사명에 대해서는 오류 카드를 보여주거나(더 나쁘게는) 지어낸
//     내용을 그대로 내보내는 것보다, 평범한 헤드라인 원문을 보여주는
//     편이 훨씬 나은 실패 방식이기 때문입니다.
//   - 약한 실패(금칙어/톤)는 마지막 시도 결과를 그대로 내보냅니다.
//     약간 어색한 문장이라도 브리핑이 아예 없는 것보다는 낫기
//     때문입니다.
//
// 강한 실패로 errBriefingValidationFailed를 반환할 때는 text에도 마지막
// 시도의(검증에 실패한) 원문을 그대로 채워 함께 돌려준다 — err != nil이면
// text를 쓰지 않는 대부분의 호출자에는 영향이 없지만,
// generateNewsSectionText는 이 원문을 보고 어느 헤드라인이 문제였는지
// 추정해 그 항목만 제외한 재시도를 한 번 더 시도한다.
//
// isFallback은 text가 hallucinationFallback(제목 기반 안전 문구)로
// 대체된 경우에만 true를 반환합니다 — err는 nil이라 호출자
// (resolveBriefingSection)는 이걸 "정상 생성 성공"으로 취급하지만, 실제
// 내용은 LLM이 생성한 문장이 아니라 원문 제목 그대로입니다. 이 신호가
// 없던 시절에는 resolveBriefingSection이 이 텍스트를 다른 정상 결과와
// 똑같이 data_hash 기준으로 영구 캐싱해서, 실제 보고된 사례처럼("가장
// 인기 있는 뉴스: A 3.6-ton mirror..." 원문이 그대로 캐시에 고정된 채
// 계속 재사용됨) 같은 뉴스 데이터가 남아있는 한(뉴스 원본 캐시 TTL
// 30분) 정상 생성을 다시 시도할 기회조차 주어지지 않았습니다.
//
// groundingText/hallucinationFallback은 뉴스 섹션에서만 의미가
// 있습니다 — groundingText가 비어 있으면 고유명사 검사 자체를 건너뜁니다
// (날씨/환율은 빈 문자열 ""을 넘깁니다).
//
// briefingSectionTemperature/briefingSectionMaxTokens: 날씨/환율 섹션에서
// 8B 모델이 같은 문장을 반복 생성하는 현상이 관측되어 두 가지를 함께
// 조정했습니다. (1) max_tokens가 아예 설정되어 있지 않아 Groq의 모델별
// 기본 상한이 그대로 적용되고 있었는데, 반복 루프에 빠지면 그 상한에
// 도달할 때까지 계속 토큰을 낭비할 수 있었습니다 — 최대 2문장이면
// 충분하므로 넉넉하되 유한한 값으로 낮춰 루프를 훨씬 일찍 끊어냅니다.
// simple까지 함께 생성하던 시절보다 필요한 출력이 절반으로 줄어서
// 값도 함께 낮췄습니다. (2) temperature 0.2가 오히려 일부 모델에서
// 반복을 유발하는 사례가 보고되어 있어 0.3으로 소폭 올렸습니다 — 어느
// 조정이 실제 원인이었는지와 무관하게 validateSectionOutput의
// findRepeatedPhrase가 여전히 최종 방어선으로 남아 있습니다.
const briefingSectionTemperature = 0.3
const briefingSectionMaxTokens = 300

// briefingSectionFrequencyPenalty: 반복 생성 루프가 max_tokens 상한(300)에
// 도달해서가 아니라(실제 completionTokens는 로그상 32~145 수준으로 한참
// 못 미쳤다) 훨씬 짧은 응답 안에서 같은 구절이 그대로 재등장하는 방식으로
// 계속 관측되어, 그 원인에 직접 대응하는 디코딩 파라미터를 추가했다 —
// callGroqChat의 frequencyPenalty 문서 주석 참고. 0.3~0.7 구간이 일반적인
// 권장값이라 중간값인 0.4로 시작한다. 값을 더 올리면 이론적으로는 반복을
// 더 강하게 억제하지만, 조사, 어미("~습니다") 등 한국어 문장에서 자연스럽게
// 반복되는 정상적인 토큰까지 회피하게 만들어 문장이 어색해질 위험이
// 커지므로 보수적으로 시작한다.
const briefingSectionFrequencyPenalty = 0.4

// generateSectionText는 Groq에 순수 텍스트 응답을 요청합니다(jsonMode=false
// — 예전에는 {"simple":"...","detailed":"..."} JSON을 요청했지만, 출력
// 토큰을 줄이고 프롬프트를 단순화하기 위해 detailed 하나만 남기고 JSON
// 구조 자체를 없앴습니다). 모델이 지침을 어기고 따옴표나 코드블록으로
// 감싸서 응답하는 경우에 대비해 trimSurroundingQuotes로 방어적으로
// 벗겨냅니다.
func generateSectionText(ctx context.Context, name, model, systemPrompt, userContent string, allowedNumbers []float64, groundingText, hallucinationFallback string) (text string, isFallback bool, err error) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		return "", false, errGroqKeyMissing
	}

	currentModel := model
	var previousUngroundedNumber string
	var hasPreviousUngroundedNumber bool

	for attempt := 0; attempt <= maxSectionRegenerations; attempt++ {
		content, callErr := callGroqChat(ctx, apiKey, currentModel, []groqChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		}, briefingSectionTemperature, briefingSectionMaxTokens, briefingSectionFrequencyPenalty, false)
		if callErr != nil {
			return "", false, callErr
		}

		text = trimSurroundingQuotes(strings.TrimSpace(content))
		if text == "" {
			return "", false, fmt.Errorf("%s briefing response was empty", name)
		}

		reason, hardFailure, useFallback, lenientIfCoreNounSurvives := validateSectionOutput(text, allowedNumbers, groundingText)
		if reason == "" {
			return text, false, nil
		}

		// 검증 실패의 입력과 원문 전체를 로그로 남깁니다 — reason에는 매칭된
		// 짧은 구절만 담기므로, 반복이 정확히 어느 지점부터 시작됐는지, 그리고
		// (해외 모드처럼) 원문 헤드라인/description 자체가 이미 뒤섞이거나
		// 잘려서 문제였는지 보려면 입력과 출력 전체가 함께 필요합니다.
		log.Printf("브리핑(%s) 시도 %d/%d 검증 실패: %s\n입력: %s\n전체 응답: %s", name, attempt+1, maxSectionRegenerations+1, reason, userContent, text)

		// 재시도마다 findUngroundedNumber가 감지하는 숫자 자체가 바뀌는 것은
		// "모델이 매번 다른 값을 지어낸다"는 신호다 — 실제로 있었던 사고:
		// title이 "$100…"으로 잘려 단위(million)가 사라지자, 모델이 1차
		// 시도에서 "1억"으로, 모델 승격 후 2차 시도에서 "10억"으로 서로 다른
		// 값을 추측해 두 시도 모두 검증에 실패했다. 이 패턴이 감지되면
		// 검증기나 프롬프트 자체보다 원문 입력(title/description)이 잘려서
		// 애초에 근거가 없었을 가능성을 의심해야 하므로, 원인 추적이 쉽도록
		// 명시적으로 경고를 남긴다.
		if num, ok := extractUngroundedNumberFromReason(reason); ok {
			if hasPreviousUngroundedNumber && num != previousUngroundedNumber {
				log.Printf("브리핑(%s): 재시도마다 감지된 근거 없는 숫자가 다름(%s -> %s) — 검증기/프롬프트 문제가 아니라 원문 title/description 자체가 잘려서 불완전할 가능성이 있습니다. 입력: %s", name, previousUngroundedNumber, num, userContent)
			}
			previousUngroundedNumber = num
			hasPreviousUngroundedNumber = true
		}

		if attempt >= maxSectionRegenerations {
			if hardFailure {
				if lenientIfCoreNounSurvives && hasGroundedCoreProperNoun(text, groundingText) {
					// 원문에 없는 고유명사가 감지됐지만, 계약 상대방 날조가
					// 아니고(lenientIfCoreNounSurvives) 원문의 핵심 개체가
					// 응답에 그대로 남아있다 — "Panthers가 NFL 소속"처럼
					// 상식적인 소속 정보를 보충했을 가능성이 새 이름을 통째로
					// 지어냈을 가능성보다 높다고 보고, 재시도 후 마지막
					// 결과를 안전 문구로 대체하는 대신 그대로 사용한다.
					log.Printf("브리핑(%s): 재시도 후에도 %s이지만, 원문 핵심 개체가 응답에 남아있어 부분적으로 신뢰 가능하다고 보고 그대로 사용합니다", name, reason)
					return text, false, nil
				}
				if useFallback && hallucinationFallback != "" {
					log.Printf("브리핑(%s): 재시도 후에도 %s, 제목 기반 안전 문구로 대체", name, reason)
					return hallucinationFallback, true, nil
				}
				// text에는 검증에 실패한 마지막 응답을 그대로 담아 반환한다 —
				// generateNewsSectionText가 이 실패 원문을 보고 어느 헤드라인이
				// 문제였는지 추정해 그 항목만 제외한 재시도를 시도할 수 있게
				// 하기 위해서다. 다른 호출자들은 err != nil이면 text를 쓰지
				// 않으므로 기존 동작에는 영향이 없다.
				return text, false, fmt.Errorf("%s: %w (%s 반복 감지)", name, errBriefingValidationFailed, reason)
			}
			log.Printf("브리핑(%s): 재시도 후에도 %s — 마지막 결과를 그대로 사용합니다", name, reason)
			return text, false, nil
		}

		if groqEscalationCountToday() >= maxDailyGroqEscalations {
			log.Printf("브리핑(%s): %s, 그러나 오늘 70B 승격 횟수가 안전 한도(%d회)에 도달해 승격 없이 마지막 결과를 사용합니다", name, reason, maxDailyGroqEscalations)
			if hardFailure {
				if lenientIfCoreNounSurvives && hasGroundedCoreProperNoun(text, groundingText) {
					log.Printf("브리핑(%s): 승격 한도 도달 상태에서도 원문 핵심 개체가 응답에 남아있어 그대로 사용합니다", name)
					return text, false, nil
				}
				if useFallback && hallucinationFallback != "" {
					return hallucinationFallback, true, nil
				}
				return text, false, fmt.Errorf("%s: %w (%s, 승격 한도 도달)", name, errBriefingValidationFailed, reason)
			}
			return text, false, nil
		}

		escalated := escalationGroqModel()
		log.Printf("브리핑(%s): %s, 모델 승격 후 재생성 시도 (%s -> %s, 오늘 승격 %d/%d회째)",
			name, reason, currentModel, escalated, groqEscalationCountToday()+1, maxDailyGroqEscalations)
		currentModel = escalated
	}

	// 도달 불가능한 코드: 위의 attempt == maxSectionRegenerations 분기
	// 안에서 루프가 항상 return하므로 여기까지 오지 않습니다.
	return text, false, nil
}

// pickNewsItemToExclude는 8B와 70B 모두에서 강한 검증 실패로 끝난 뉴스
// 생성 결과(failedText)를 보고, 후보 헤드라인 items 중 어느 것이 그 결과의
// 소재였을 가능성이 가장 높은지 추정한다 — generateNewsSectionText가 그
// 항목 하나만 제외하고 재시도하는 데 쓴다.
//
// 완벽한 판별은 불가능하다. 형태소 분석기 없이, 그리고 언어에 무관하게
// (해외 모드는 원문이 영어라 한국어 명사 토큰 중복도 같은 방법을 쓸 수
// 없다 — findTopicMismatch가 해외 모드를 건너뛰는 이유와 같다) 동작해야
// 하므로, 번역을 거쳐도 그대로 남는 숫자를 단서로 쓴다: 생성문에 등장한
// 숫자와 가장 많이 겹치는 헤드라인을 "모델이 실제로 다루려 했던" 항목으로
// 보고 제외 대상으로 삼는다. 겹치는 숫자가 전혀 없어 판별할 수 없으면,
// newsSectionSystemPrompt가 "숫자·명칭이 있는 항목을 우선 고르세요"라고
// 우선순위를 매겨두었으므로 우선순위가 가장 낮은 마지막 항목을 기본값으로
// 제외한다.
func pickNewsItemToExclude(failedText string, items []briefingNewsItem) int {
	failedNumbers := extractNumbers(failedText)
	bestIdx := len(items) - 1
	bestScore := 0
	for i, item := range items {
		itemNumbers := extractNumbers(item.Title + " " + item.Description)
		score := 0
		for _, fn := range failedNumbers {
			for _, in := range itemNumbers {
				if numbersMatch(fn, in) {
					score++
					break
				}
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	return bestIdx
}

// generateNewsSectionText는 generateSectionText를 감싸서 뉴스 섹션에만
// 해당하는 마지막 폴백 하나를 추가한다: 8B로 시작해 70B로 승격까지 했는데도
// 강한 검증(비한글 외국 문자 오염, 새어나온 영어, 반말/기사체 어미 등)에 실패했다면,
// 같은 헤드라인 3개를 다시 굴리는 대신(입력이 그대로면 같은 실패가 반복될
// 가능성이 높다) pickNewsItemToExclude로 추정한 문제 항목 하나를 빼고
// 나머지 헤드라인만으로 딱 한 번 더 생성을 시도한다. 이 재시도는 다시
// frequentGroqModel()(8B)에서 시작하는데, 항목이 줄어든 더 쉬운 입력이라
// 굳이 비싼 70B로 바로 가지 않아도 되고, 그래도 실패하면
// generateSectionText가 내부적으로 다시 한번 승격을 시도한다.
//
// 항목이 하나뿐이면(더 뺄 게 없다) 시도하지 않고 원래 오류를 그대로
// 돌려준다 — 그래야 호출자(resolveBriefingSection)가 기존처럼
// stale_fallback으로 넘어간다. 이 재시도마저 실패해도 마찬가지로 원래
// 오류를 돌려준다 — 여기서 더 시도하면 사용자가 겪는 지연과 Groq 쿼터
// 소모만 늘어날 뿐이다.
func generateNewsSectionText(ctx context.Context, name, model, systemPrompt, userContent string, allowedNumbers []float64, groundingText, hallucinationFallback string, newsInput *briefingNewsInput) (string, bool, error) {
	text, isFallback, err := generateSectionText(ctx, name, model, systemPrompt, userContent, allowedNumbers, groundingText, hallucinationFallback)
	if err == nil || !errors.Is(err, errBriefingValidationFailed) {
		return text, isFallback, err
	}
	if newsInput == nil || len(newsInput.Items) < 2 {
		return text, isFallback, err
	}

	excludeIdx := pickNewsItemToExclude(text, newsInput.Items)
	reduced := &briefingNewsInput{
		Items: append(append([]briefingNewsItem{}, newsInput.Items[:excludeIdx]...), newsInput.Items[excludeIdx+1:]...),
	}
	reducedJSON, _ := json.Marshal(reduced)
	reducedUserContent := fmt.Sprintf("[뉴스 데이터]: %s\n\n위 데이터를 바탕으로 한국어 뉴스 브리핑 문장을 작성하세요.", reducedJSON)
	log.Printf("브리핑(%s): 승격 재시도 후에도 검증 실패(%v) — 문제로 의심되는 항목(id=%q) 제외하고 나머지 %d개 헤드라인으로 재생성 시도",
		name, err, newsInput.Items[excludeIdx].ID, len(reduced.Items))

	retryText, retryIsFallback, retryErr := generateSectionText(ctx, name, frequentGroqModel(), systemPrompt, reducedUserContent, allowedNewsNumbers(reduced), newsGroundingText(reduced), hallucinationFallback)
	if retryErr != nil {
		// retryErr(이번 시도의 실제 실패 사유)을 반환해야 한다 — 예전에는
		// 여기서 최초 실패(err, 예: "비한글 외국 문자 감지")를 그대로
		// 반환했는데, 그러면 이 세 번째 시도가 실제로는 API 오류든 빈
		// 응답이든 또 다른 검증 실패든, 로그와 최종 실패 사유(및
		// classifyBriefingFailureReason 분류)에는 항상 두 번째 시도의
		// 낡은 사유만 남아 실제 원인을 알 수 없었다.
		log.Printf("브리핑(%s): 문제 항목 제외 후에도 생성 실패(%v) — stale_fallback으로 넘어갑니다 (최초 실패 사유는 참고용: %v)", name, retryErr, err)
		return retryText, retryIsFallback, retryErr
	}
	log.Printf("브리핑(%s): 문제 항목 제외 후 재생성 성공", name)
	return retryText, retryIsFallback, nil
}

// trimSurroundingQuotes는 모델이 "응답은 문장 텍스트만 그대로 출력하라"는
// 지침을 어기고 응답 전체를 따옴표로 감싸는 경우를 방어적으로 처리합니다
// — JSON 모드를 껐으므로 더 이상 파서가 이를 대신 걸러주지 않습니다.
func trimSurroundingQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// isFallback은 이 캐시 행의 텍스트가 hallucinationFallback(제목 기반
// 안전 문구)이었는지를 나타낸다 — resolveBriefingSection이 data_hash가
// 일치해도 이 값이 true면 캐시를 그대로 재사용하지 않고 재생성을
// 시도하는 데 쓰인다(resolveBriefingSection 문서 주석 참고).
type briefingSectionCacheRow struct {
	dataHash    string
	text        string
	generatedAt time.Time
	isFallback  bool
}

func lookupBriefingSectionCache(ctx context.Context, conn *sql.DB, section string) (briefingSectionCacheRow, bool) {
	if conn == nil {
		return briefingSectionCacheRow{}, false
	}

	var row briefingSectionCacheRow
	err := conn.QueryRowContext(ctx,
		`SELECT data_hash, detailed_text, generated_at, is_fallback FROM briefing_section_cache WHERE section = ?`, section,
	).Scan(&row.dataHash, &row.text, &row.generatedAt, &row.isFallback)
	if err != nil {
		return briefingSectionCacheRow{}, false
	}
	return row, true
}

func upsertBriefingSectionCache(ctx context.Context, conn *sql.DB, section, dataHash, text string, generatedAt time.Time, isFallback bool) error {
	if conn == nil {
		return nil
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO briefing_section_cache (section, data_hash, detailed_text, generated_at, is_fallback)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(section) DO UPDATE SET data_hash = excluded.data_hash, detailed_text = excluded.detailed_text, generated_at = excluded.generated_at, is_fallback = excluded.is_fallback`,
		section, dataHash, text, generatedAt, isFallback,
	)
	return err
}

// briefingSectionStatus* 값들은 BriefingSectionMeta.Status로 그대로
// JSON에 노출되어 프론트엔드가 안내 배지를 띄울지 결정하는 데 쓰인다.
const (
	briefingStatusFresh         = "fresh"          // 방금 새로 생성됨
	briefingStatusCached        = "cached"         // 입력 불변 — 정상적인 캐시 재사용
	briefingStatusStaleFallback = "stale_fallback" // 생성 실패, 이전 캐시로 대체 — 사용자 안내 필요
	briefingStatusFailed        = "failed"         // 생성 실패, 대체할 캐시도 없음
)

// classifyBriefingFailureReason은 generateSectionText가 반환한 에러를,
// 사용자에게 그대로 노출할 상세 사유가 아니라 프론트엔드가 안내 문구를
// 고르는 데 참고할 대략적인 카테고리로 나눈다.
func classifyBriefingFailureReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errGroqKeyMissing) {
		return "missing_api_key"
	}
	if errors.Is(err, errBriefingValidationFailed) {
		return "validation_failed"
	}
	if errors.Is(err, errBriefingDataMissing) {
		return "data_missing"
	}
	if msg := strings.ToLower(err.Error()); strings.Contains(msg, "rate limit") || strings.Contains(msg, "tokens per minute") || strings.Contains(msg, "(tpm)") {
		return "rate_limit"
	}
	return "generation_error"
}

type briefingSectionOutput struct {
	Text        string
	Cached      bool
	GeneratedAt time.Time
	// Status/FailureReason은 그대로 BriefingSectionMeta에 실려 응답에
	// 포함된다 — briefingStatus* 상수와 classifyBriefingFailureReason 참고.
	Status        string
	FailureReason string
}

// resolveBriefingSection은 섹션별로 캐시를 쓸지 새로 생성할지 결정하는
// 단계입니다: briefing_section_cache에 이 섹션의 행이 이미 있고
// data_hash가 일치하면 그 텍스트를 그대로 재사용하고 Groq는 아예
// 호출하지 않습니다. 그렇지 않으면 새로 텍스트를 생성하고(best-effort로)
// 저장합니다. 생성이 실패했지만 오래된 캐시 행이 존재한다면, 섹션을
// 통째로 빼버리는 대신 그 오래된 텍스트를 사용합니다 — 다소 낡은
// 문장이라도 아예 없는 것보다는 낫습니다. 이 경우 Status를
// stale_fallback으로 명확히 마킹해서, 프론트엔드가 "방금 생성된 것"과
// "실패해서 어쩔 수 없이 대체된 것"을 구분해 사용자에게 알릴 수 있게
// 합니다.
//
// data_hash가 일치해도 cached.isFallback이 true면(직전 생성이
// hallucinationFallback으로 대체된 결과였다면) 캐시를 그대로 재사용하지
// 않고 다시 생성을 시도합니다 — 실제 보고된 사례: "가장 인기 있는
// 뉴스: A 3.6-ton mirror..." 같은 원문 그대로의 안전 문구가 뉴스 데이터가
// 안 바뀌는 동안(원본 뉴스 캐시 TTL 30분) 영구히 재사용되고 있었습니다.
// hallucinationFallback도 err == nil로 반환되는 "성공"이라 이전에는 다른
// 정상 결과와 구분 없이 그대로 캐싱됐기 때문입니다. 이 재시도가 다시
// 실패하면(err != nil) 아래의 기존 stale_fallback 처리 경로가 그대로
// 이 캐시 행(과 그 안의 폴백 텍스트)을 대체 값으로 서빙합니다 — 즉
// 폴백은 "매 요청마다 새로 생성을 시도하되, 실패하면 이전 폴백을 잠깐
// 더 보여준다"는 동작이 되어, 짧은 TTL을 따로 두지 않고도 사실상 매
// 요청이 복구 기회가 됩니다.
//
// newsInput은 뉴스 작업에서만 nil이 아니며, 그 경우 generateSectionText
// 대신 generateNewsSectionText를 사용해 "8B/70B 모두 실패하면 문제로
// 의심되는 헤드라인 하나만 제외하고 재시도"하는 뉴스 전용 폴백을 태웁니다
// — 날씨/환율은 제외할 "항목"이라는 개념 자체가 없으므로 nil을 넘겨
// 기존과 동일하게 동작합니다.
//
// hasData는 이 섹션의 원본 데이터(WeatherData/ExchangeData/NewsData)가
// 실제로 있는지를 나타냅니다. false면 Groq를 아예 호출하지 않고
// errBriefingDataMissing으로 바로 아래의 실패 처리 분기(캐시가 있으면
// stale_fallback, 없으면 dataMissingMessage를 담은 명시적 실패)로
// 넘어갑니다 — 원본 데이터가 없는 채로 "[뉴스 데이터]: null" 같은 의미
// 없는 프롬프트를 Groq에 보내 hallucination만 유발하던 문제를 막기
// 위해서입니다. dataMissingMessage는 hasData가 false일 때만 쓰이며,
// 섹션마다("⚠️ 날씨/환율/뉴스 데이터를 가져오지 못해...") 다른 문구를
// 쓸 수 있도록 호출자가 정합니다.
func resolveBriefingSection(ctx context.Context, section, model, hash, systemPrompt, userContent string, allowedNumbers []float64, groundingText, hallucinationFallback string, newsInput *briefingNewsInput, hasData bool, dataMissingMessage string) briefingSectionOutput {
	cached, found := lookupBriefingSectionCache(ctx, db, section)
	if found && cached.dataHash == hash && !cached.isFallback {
		recordGroqCacheHit()
		log.Printf("[캐시 재사용] 브리핑(%s): 입력 데이터 변경 없음 (Groq 미호출)", section)
		return briefingSectionOutput{Text: cached.text, Cached: true, GeneratedAt: cached.generatedAt, Status: briefingStatusCached}
	}
	if found && cached.dataHash == hash && cached.isFallback {
		log.Printf("브리핑(%s): 캐시가 안전 폴백 결과라 데이터 변경 없이도 재생성을 시도합니다", section)
	}

	var text string
	var isFallback bool
	var err error
	if !hasData {
		err = errBriefingDataMissing
		log.Printf("브리핑(%s): 원본 데이터를 가져오지 못해 Groq 호출을 생략합니다", section)
	} else {
		if found {
			log.Printf("브리핑(%s): 입력 데이터 변경 감지, Groq 재호출 (모델: %s)", section, model)
		} else {
			log.Printf("브리핑(%s): 캐시 없음, Groq 최초 호출 (모델: %s)", section, model)
		}
		if newsInput != nil {
			text, isFallback, err = generateNewsSectionText(ctx, section, model, systemPrompt, userContent, allowedNumbers, groundingText, hallucinationFallback, newsInput)
		} else {
			text, isFallback, err = generateSectionText(ctx, section, model, systemPrompt, userContent, allowedNumbers, groundingText, hallucinationFallback)
		}
	}
	if err != nil {
		reason := classifyBriefingFailureReason(err)
		log.Printf("브리핑(%s): 생성 실패(사유: %s): %v", section, reason, err)
		if found {
			log.Printf("브리핑(%s): 이전 캐시로 대체 (stale_fallback)", section)
			return briefingSectionOutput{
				Text: cached.text, Cached: true, GeneratedAt: cached.generatedAt,
				Status: briefingStatusStaleFallback, FailureReason: reason,
			}
		}
		if errors.Is(err, errBriefingDataMissing) {
			return briefingSectionOutput{Text: dataMissingMessage, GeneratedAt: time.Now(), Status: briefingStatusFailed, FailureReason: reason}
		}
		if errors.Is(err, errBriefingValidationFailed) {
			// 조용히 대체할 오래된 캐시가 없고, 재시도 후에도 이 섹션이 여전히
			// 강한 콘텐츠 검증(비한글 외국 문자/새어나온 영어)에 실패한 경우입니다 — 합쳐진
			// 브리핑에서 이 섹션을 조용히 빼는 대신 명시적으로 표시합니다.
			return briefingSectionOutput{Text: "⚠️ 생성 실패", GeneratedAt: time.Now(), Status: briefingStatusFailed, FailureReason: reason}
		}
		return briefingSectionOutput{Status: briefingStatusFailed, FailureReason: reason}
	}

	generatedAt := time.Now()
	if isFallback {
		log.Printf("브리핑(%s): 안전 폴백 결과를 캐싱합니다 — 다음 요청에서 데이터가 그대로여도 재생성을 다시 시도합니다", section)
	}
	if upsertErr := upsertBriefingSectionCache(ctx, db, section, hash, text, generatedAt, isFallback); upsertErr != nil {
		log.Printf("브리핑(%s): 캐시 저장 실패: %v", section, upsertErr)
	}
	return briefingSectionOutput{Text: text, Cached: false, GeneratedAt: generatedAt, Status: briefingStatusFresh}
}

// getBriefing은 날씨/환율/뉴스 섹션의 텍스트를 각각 독립적으로, 그리고
// 병렬로 생성(또는 캐시 재사용)한 다음 순서대로 합칩니다. Groq 호출이
// 실패했는데 되돌아갈 캐시도 없는 섹션은 합쳐진 텍스트에서 그냥
// 생략됩니다 — 모든 섹션이 실패하지 않는 한, 브리핑은 완전히 실패하는
// 대신 섹션 단위로 우아하게 성능이 저하됩니다. newsBriefingCacheKey는
// 뉴스 섹션의 briefing_section_cache 기본 키입니다 — 날씨/환율과 달리
// 뉴스는 카테고리와 지역에 따라 달라지므로, 예를 들어 "국내/주요"와
// "해외/기술" 사이를 전환할 때마다 공유되는 하나의 "news" 행을 덮어쓰는
// 게 아니라 각각 독립된 캐시 행에 접근해야 합니다.
func newsBriefingCacheKey(region, category string) string {
	return "news:" + region + ":" + category
}

// weatherBriefingCacheKey/exchangeBriefingCacheKey도
// newsBriefingCacheKey와 정확히 같은 이유로 존재합니다: 도시나 통화쌍을
// 바꾸면 모든 도시/통화쌍이 공유하는 하나의 "weather"/"exchange" 행이
// 아니라 각각 독립된 캐시 행에 접근해야 합니다. 이 수정 이전에는
// data_hash 비교가 도시 변경을 정확히 감지해서 재생성을 시도했지만,
// 그 Groq 호출이 실패하면(예: rate-limit) resolveBriefingSection의
// 오래된 캐시 폴백에는 공유되는 행 하나밖에 없었습니다 — 그 결과
// *이전* 도시나 통화쌍에 남아있던 텍스트를 마치 현재 것인 양 조용히
// 내보내게 됐습니다. 각 대상별로 복합 키를 쓰면 애초에 실수로
// 되돌아갈 다른 도시/통화쌍의 행 자체가 존재하지 않습니다.
func weatherBriefingCacheKey(city string) string {
	if city == "" {
		city = "unknown"
	}
	return "weather:" + city
}

func exchangeBriefingCacheKey(from, to string) string {
	if from == "" {
		from = "unknown"
	}
	if to == "" {
		to = "unknown"
	}
	return "exchange:" + from + ":" + to
}

func getBriefing(ctx context.Context, weather *WeatherData, exchange *ExchangeData, news *NewsData, newsCategory, newsRegion string) (*BriefingData, error) {
	weatherInput := toBriefingWeatherInput(weather)
	exchangeInput := toBriefingExchangeInput(exchange)
	newsInput := toBriefingNewsInput(news)

	weatherJSON, _ := json.Marshal(weatherInput)
	exchangeJSON, _ := json.Marshal(exchangeInput)
	newsJSON, _ := json.Marshal(newsInput)

	type job struct {
		name           string
		model          string
		hash           string
		systemPrompt   string
		userContent    string
		allowedNumbers []float64
		// groundingText/hallucinationFallback/newsInput은 뉴스 작업에서만
		// 설정됩니다 — findUngroundedProperNoun과 generateSectionText/
		// generateNewsSectionText의 문서 주석 참고.
		groundingText         string
		hallucinationFallback string
		newsInput             *briefingNewsInput
		// hasData/dataMissingMessage는 세 섹션 모두에 적용됩니다 —
		// resolveBriefingSection의 문서 주석 참고.
		hasData            bool
		dataMissingMessage string
	}
	// 세 섹션 모두 첫 시도에는 frequentGroqModel()(저렴하고 쿼터가 넉넉한
	// 모델)을 사용합니다 — 브리핑 섹션은 캐시가 미스될 때마다(도시 전환,
	// 통화쌍 전환, 뉴스 카테고리 변경) 재생성되므로 호출 빈도가 높은
	// 지점이라 70B 모델의 하루 1,000회 쿼터를 금방 소진시킬 수 있습니다.
	// 8B 모델의 출력이 강한 콘텐츠 검증(비한글 외국 문자 오염, 새어나온 영어, 근거 없는
	// 숫자, hallucination된 고유명사)에 실패하면 generateSectionText가 단
	// 한 번 escalationGroqModel()로 승격 재시도하므로, 정확도가 중요한
	// 출력은 기본값이 아니라 실제로 필요할 때 더 큰 모델을 받게 됩니다.
	briefingModel := frequentGroqModel()

	weatherCity := "unknown"
	if weather != nil && weather.Current.City != "" {
		weatherCity = weather.Current.City
	}
	exchangeFrom, exchangeTo := "unknown", "unknown"
	if exchange != nil {
		if exchange.From != "" {
			exchangeFrom = exchange.From
		}
		if exchange.To != "" {
			exchangeTo = exchange.To
		}
	}

	jobs := [3]job{
		{
			name:               weatherBriefingCacheKey(weatherCity),
			model:              briefingModel,
			hash:               hashJSON(weatherInput),
			systemPrompt:       weatherSectionSystemPrompt,
			userContent:        fmt.Sprintf("[날씨 데이터]: %s\n\n위 데이터를 바탕으로 한국어 날씨 브리핑 문장을 작성하세요.", weatherJSON),
			allowedNumbers:     allowedWeatherNumbers(weatherInput),
			hasData:            weather != nil,
			dataMissingMessage: "⚠️ 날씨 데이터를 가져오지 못해 브리핑을 생성할 수 없습니다",
		},
		{
			name:               exchangeBriefingCacheKey(exchangeFrom, exchangeTo),
			model:              briefingModel,
			hash:               hashJSON(exchangeInput),
			systemPrompt:       exchangeSectionSystemPrompt,
			userContent:        fmt.Sprintf("[환율 데이터]: %s\n\n위 데이터를 바탕으로 한국어 환율 브리핑 문장을 작성하세요.", exchangeJSON),
			allowedNumbers:     allowedExchangeNumbers(exchangeInput),
			hasData:            exchange != nil,
			dataMissingMessage: "⚠️ 환율 데이터를 가져오지 못해 브리핑을 생성할 수 없습니다",
		},
		{
			name:                  newsBriefingCacheKey(newsRegion, newsCategory),
			model:                 briefingModel,
			hash:                  hashNewsInput(newsInput),
			systemPrompt:          newsSectionSystemPrompt,
			userContent:           fmt.Sprintf("[뉴스 데이터]: %s\n\n위 데이터를 바탕으로 한국어 뉴스 브리핑 문장을 작성하세요.", newsJSON),
			allowedNumbers:        allowedNewsNumbers(newsInput),
			groundingText:         newsGroundingText(newsInput),
			hallucinationFallback: newsHallucinationFallback(news),
			newsInput:             newsInput,
			hasData:               news != nil && len(news.Items) > 0,
			dataMissingMessage:    "⚠️ 뉴스 데이터를 가져오지 못해 브리핑을 생성할 수 없습니다",
		},
	}
	for _, j := range jobs {
		logBriefingPromptSize(j.name, j.systemPrompt, j.userContent)
	}
	logNewsPromptBreakdown(newsInput)

	// jobs는 항상 [weather, exchange, news] 순서로 고정되어 있습니다 —
	// 아래에서 j.name과 매칭하는 대신 위치(인덱스) 기준으로
	// BriefingSectionsMeta를 채우는 데 사용됩니다. 뉴스 작업의 name이
	// 이제 리터럴 "news"가 아니라 복합 캐시 키(예:
	// "news:international:technology")이기 때문입니다.

	var outputs [3]briefingSectionOutput
	var wg sync.WaitGroup
	wg.Add(len(jobs))
	for i, j := range jobs {
		go func(i int, j job) {
			defer wg.Done()
			outputs[i] = resolveBriefingSection(ctx, j.name, j.model, j.hash, j.systemPrompt, j.userContent, j.allowedNumbers, j.groundingText, j.hallucinationFallback, j.newsInput, j.hasData, j.dataMissingMessage)
		}(i, j)
	}
	wg.Wait()

	var texts []string
	var meta BriefingSectionsMeta
	anySuccess := false
	allCached := true
	var latestGeneratedAt time.Time

	for i, out := range outputs {
		sectionMeta := BriefingSectionMeta{Cached: out.Cached, Detailed: out.Text, Status: out.Status, FailureReason: out.FailureReason}
		if out.Text != "" {
			anySuccess = true
			texts = append(texts, out.Text)
			if !out.Cached {
				allCached = false
			}
			if out.GeneratedAt.After(latestGeneratedAt) {
				latestGeneratedAt = out.GeneratedAt
			}
			sectionMeta.GeneratedAt = out.GeneratedAt.Format(time.RFC3339)
		}

		switch i {
		case 0:
			meta.Weather = sectionMeta
		case 1:
			meta.Exchange = sectionMeta
		case 2:
			meta.News = sectionMeta
		}
	}

	if !anySuccess {
		if os.Getenv("GROQ_API_KEY") == "" {
			return nil, errGroqKeyMissing
		}
		return nil, errors.New("failed to generate any briefing section")
	}

	return &BriefingData{
		Detailed:     strings.Join(texts, " "),
		Cached:       allCached,
		GeneratedAt:  latestGeneratedAt.Format(time.RFC3339),
		BriefingMeta: meta,
	}, nil
}
