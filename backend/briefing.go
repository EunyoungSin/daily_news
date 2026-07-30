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

// briefingDayForecast는 DayForecast와 구조는 같지만 포인터 필드를 사용해서,
// 해당 시간대가 Available하지 않으면 Groq로 보내는 JSON에서 그 필드 자체를
// 아예 생략합니다 — DayForecast의 제로값 PeriodForecast를 그대로 쓰면 실제
// 데이터가 없는 시간대에도 모델에게 "temperatureC": 0이라는 값을 그대로
// 넘겨주게 되기 때문입니다(PeriodForecast의 문서 주석 참고).
type briefingDayForecast struct {
	Morning   *PeriodForecast `json:"morning,omitempty"`
	Afternoon *PeriodForecast `json:"afternoon,omitempty"`
}

func toBriefingDayForecast(day DayForecast) briefingDayForecast {
	var out briefingDayForecast
	if day.Morning.Available {
		m := day.Morning
		out.Morning = &m
	}
	if day.Afternoon.Available {
		a := day.Afternoon
		out.Afternoon = &a
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
// llama-3.1-8b-instant의 분당 한도(6,000 TPM)를 단일 요청만으로 초과하는
// 것이 확인되어, 100자로 더 줄였습니다. 요약에는 구체적 사실 하나를 더
// 뽑아낼 만큼의 description만 있으면 충분하지 전체가 필요한 게 아니므로,
// 문맥을 약간 포기하는 대신 요청당 토큰 비용을 실질적으로 낮춥니다.
const briefingNewsDescriptionMaxRunes = 100

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
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
			Title:       annotateNumericUnits(it.Title),
			Description: annotateNumericUnits(truncateRunes(it.Description, briefingNewsDescriptionMaxRunes)),
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
func newsGroundingText(input *briefingNewsInput) string {
	if input == nil {
		return ""
	}
	var b strings.Builder
	for _, item := range input.Items {
		b.WriteString(item.Title)
		b.WriteString(" ")
		b.WriteString(item.Description)
		b.WriteString(" ")
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

// bannedBriefingPhrases는 두 종류의 실패를 잡아냅니다: 내용 없는 채우기
// 문장(섹션별 재작성 기능이 애초에 존재하는 이유가 되는 바로 그 문제 —
// "다양한 논의가 진행 중입니다"는 독자에게 아무 정보도 주지 않습니다)과,
// 고정된 합니다체 톤에 어울리지 않는 인터넷 은어입니다. 여기 등록된 것은
// 어떤 맥락에서든 공허하거나 부적절한 표현만입니다 — 예를 들어 "주목받고
// 있습니다"는 실제 구체적 사실 뒤에 붙으면 정당한 마무리 문구가 될 수
// 있으므로 의도적으로 제외했습니다.
var bannedBriefingPhrases = []string{
	"다양한 논의가 진행 중입니다",
	"토론이 활발합니다",
	"관심이 모아지고 있습니다",
	"다양한 의견이 있습니다",
	"여러 가지 논의가 있습니다",
	"많은 관심을 받고 있습니다",
	"ㅋㅋ", "ㅎㅎ", "대박", "헐", "레전드", "핵", "TMI", "인정?", "짱",
}

func findBannedPhrase(text string) (string, bool) {
	for _, phrase := range bannedBriefingPhrases {
		if strings.Contains(text, phrase) {
			return phrase, true
		}
	}
	return "", false
}

// foreignCJKPattern은 한자(중국어/한자와 공유되는 표의문자)와 일본어
// 가나를 매칭합니다 — 순수 한국어 응답에는 절대 있어서는 안 되는
// 문자들입니다. 한글 자체(U+AC00-D7A3 완성형 음절, U+1100-11FF /
// U+3130-318F 자모)는 이 범위에 의도적으로 포함되지 않으므로 정상적인
// 한국어 텍스트는 절대 이 패턴에 걸리지 않습니다.
var foreignCJKPattern = regexp.MustCompile(`[\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}\x{3040}-\x{309F}\x{30A0}-\x{30FF}]`)

func findForeignCJK(text string) (string, bool) {
	match := foreignCJKPattern.FindString(text)
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

// findRepeatedPhrase는 text 안에서 두 번 이상 등장하는, 길이
// repeatedPhraseMinRunes 이상인 부분 문자열 중 첫 번째를 보고합니다 —
// "모델이 루프에 빠져 문장이 망가지고 있다"를 감지하는 범용 검사기로,
// 여기 있는 다른 검사들과 달리 뉴스 섹션 전용이 아니며(날씨/환율 브리핑
// 텍스트도 루프에 빠질 수 있습니다) grounding 텍스트도 필요하지
// 않습니다.
func findRepeatedPhrase(text string) (string, bool) {
	runes := []rune(text)
	seen := make(map[string]bool, len(runes))
	for i := 0; i+repeatedPhraseMinRunes <= len(runes); i++ {
		phrase := string(runes[i : i+repeatedPhraseMinRunes])
		if seen[phrase] {
			return phrase, true
		}
		seen[phrase] = true
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
func findUngroundedProperNoun(text, groundingText string) (string, bool) {
	if groundingText == "" {
		return "", false
	}

	for _, match := range newsProperNounPattern.FindAllString(text, -1) {
		if allowedLatinAbbreviations[strings.ToLower(match)] {
			continue
		}
		if newsProperNounExemptions[match] {
			continue
		}
		if strings.Contains(groundingText, match) {
			continue
		}
		return match, true
	}

	for _, m := range newsContractCounterpartyPattern.FindAllStringSubmatch(text, -1) {
		counterparty := strings.TrimSpace(m[1])
		if counterparty == "" || strings.Contains(groundingText, counterparty) {
			continue
		}
		return counterparty, true
	}

	return "", false
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
const topicOverlapMinRatio = 0.1

// findTopicMismatch는 원문(groundingText)의 명사성 토큰 중 생성문에도
// 그대로 남아있는 비율을 계산해서, 그 비율이 topicOverlapMinRatio 미만이면
// 보고한다. 뉴스 섹션에서만 의미가 있으며, groundingText가 비어 있으면
// (날씨/환율) 검사 자체를 건너뛴다. 원문이 한국어가 아니면(해외 모드)도
// 건너뛴다 — 아래 두 번째 주석 참고.
//
// 분모를 생성문 토큰 수가 아니라 원문 토큰 수로 잡은 것은 실측으로 드러난
// 오탐을 고친 결과다: "[美특징주]KLA, 1Q 실적 가이드라인 실망감 주가
// 8%↓"처럼 압축된 증권 헤드라인은, 정상적으로 풀어쓴 요약("미국의 반도체
// 장비 업체 KLA는 1분기 실적 가이드라인에서 실망한 실적을 기록하여 주가가
// 8% 하락했다")조차 원문에 없던 연결어/서술어를 많이 새로 쓰게 만든다.
// 생성문 토큰 수로 나누면 이런 정상적인 의역까지 낮은 비율로 나와
// 오탐했다(실측: 8~17%). 반대로 "원문의 핵심 토큰이 생성문에 얼마나
// 남아있는가"로 보면, 정상적인 의역은 원문 토큰 상당수를 그대로 담고
// 있어 비율이 높게 나오는 반면, 완전히 다른 주제(청소년 화장품 압수 →
// AI 모델 벤치마크)로 둔갑한 경우는 원문 토큰이 생성문에 하나도 남지
// 않아 어느 분모를 쓰든 0%로 동일하게 잡힌다.
// hangulSyllablePattern은 groundingText가 한국어인지 판별하는 데만
// 쓰인다 — findTopicMismatch 문서 주석 참고.
var hangulSyllablePattern = regexp.MustCompile(`[가-힣]`)

func findTopicMismatch(generated, groundingText string) (float64, bool) {
	if groundingText == "" {
		return 0, false
	}
	if !hangulSyllablePattern.MatchString(groundingText) {
		// 원문이 한국어가 아니면(해외 모드 — 원문은 영어) 이 검사를 아예
		// 건너뛴다. 실측 결과, 정확한 번역조차 원문과 정확히 같은 문자열을
		// 공유하지 않는다 — 예: "Trump"/"Dulles Airport"가 표기 관례에 따라
		// "트럼프"/"덜레스 국제공항"으로 옮겨지면 원문과 생성문의 토큰
		// 문자열 자체가 다르다. 이 검사는 같은 언어 안에서 소재가 통째로
		// 바뀌는 것만 잡을 수 있는 근사치라, 번역이 개입하는 순간 항상
		// 오탐한다(실측: 정확한 번역인데도 중복도 0~4%). 해외 모드에서
		// 번역 자체의 정확성은 findLeakedEnglish/findForeignCJK 및
		// news_translation.go의 별도 검증이 담당한다.
		return 0, false
	}
	srcTokens := extractTopicTokens(groundingText)
	if len(srcTokens) == 0 {
		return 0, false
	}
	genTokens := extractTopicTokens(generated)

	overlap := 0
	for t := range srcTokens {
		if genTokens[t] {
			overlap++
		}
	}
	ratio := float64(overlap) / float64(len(srcTokens))
	if ratio < topicOverlapMinRatio {
		return ratio, true
	}
	return ratio, false
}

// errBriefingValidationFailed는 한 번 재시도한 뒤에도 여전히 강한
// 콘텐츠 검증(외국 CJK 문자, 새어나온 영어, 근거 없는 숫자)을 통과하지
// 못한 섹션을 표시합니다 — resolveBriefingSection은 이를 일반적인
// 생성 오류와 다르게 처리합니다: 되돌아갈 오래된 캐시가 없다면 해당
// 섹션을 조용히 생략하는 대신 명시적으로 "⚠️ 생성 실패"를 표시합니다.
var errBriefingValidationFailed = errors.New("briefing section failed content validation")

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

// koreanNumberPattern은 소수를 포함한 숫자 뒤에 한국어 자릿수 접미사가
// (있다면) 바로 이어지는 형태를 매칭합니다.
var koreanNumberPattern = regexp.MustCompile(`\d+(?:\.\d+)?(?:만|억|조)?`)

// extractNumbers는 text에 언급된 모든 숫자를 실제 수치 값으로 파싱하며,
// 만/억/조 배수를 적용해서 "90억"과 "9000000000"이 같은 값으로 비교되게
// 합니다. 원본 데이터(헤드라인 제목)에서 "정답" 숫자를 읽어낼 때와,
// 생성된 문장이 실제로 어떤 숫자를 주장하는지 확인할 때 모두 사용됩니다.
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

// findUngroundedNumber는 text에 언급된 숫자 중, allowedNumbers 안의
// 어떤 숫자와도 (numbersMatch의 오차 범위 내에서) 대응되지 않는 첫
// 번째 숫자를 반환합니다 — 즉, 모델이 주어진 데이터에서 가져온 게
// 아니라 지어낸 것으로 보이는 수치입니다. 이 검사는 예를 들어 헤드라인의
// 평범한 "$500"이 "1200만 달러"로 둔갑해 돌아오는 경우를 잡아냅니다:
// 여기엔 annotateNumericUnits가 처리할 K/M/B 축약 자체가 없으므로, 이
// 검사가 모델이 숫자를 그냥 잘못 읽거나 지어내는 것을 막는 마지막
// 방어선입니다.
//
// allowedNumbers에는 각 섹션이 다루는 데이터 값뿐 아니라 그 섹션의
// 프롬프트 문구 자체에 고정으로 박혀 있는 숫자도 포함되어야 합니다 —
// 예를 들어 날씨 프롬프트는 항상 "오전 8시"/"오후 2시"를 언급하고
// 환율 프롬프트는 항상 "지난 7일간"을 언급하므로, 8, 2, 7을 미리
// 허용해두지 않으면 모든 응답이 이 검사에 잘못 걸리게 됩니다.
func findUngroundedNumber(text string, allowedNumbers []float64) (float64, bool) {
	for _, found := range extractNumbers(text) {
		matched := false
		for _, allowed := range allowedNumbers {
			if numbersMatch(found, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return found, true
		}
	}
	return 0, false
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

// 아래 프롬프트는 세 섹션(날씨/환율/뉴스) 모두가 공유하며 캐시 미스마다
// 매번 전송되므로, 여기서 아끼는 토큰은 3배로 누적됩니다. 각 불릿이
// validateSectionOutput의 특정 검사(CJK/영어 잔존/반복 구절 등)에 대응되는
// 규칙 자체는 그대로 유지하면서, 문장을 더 짧게 압축했습니다.
//
// 예전에는 simple(1문장)과 detailed(1~2문장) 두 버전을 JSON 객체
// {"simple": "...", "detailed": "..."}로 함께 요청했는데, 출력 토큰을
// 줄이고 프롬프트를 단순화하기 위해 detailed 하나만 남기고 JSON 구조
// 자체를 없앴습니다 — 모델은 이제 순수 텍스트 문장만 그대로 반환하면
// 됩니다. JSON 파싱이 필요 없어져 callGroqChat도 jsonMode=false로
// 호출합니다(groq.go 참고).
const briefingCommonRules = `공통 규칙:
- 항상 정중한 존댓말(합니다체)로, 반말/명령조·인터넷 은어("ㅋㅋ","대박","헐","인정","TMI" 등)·과장된 표현·이모지 없이 담백하게 작성하세요.
- 마크다운(#, **, -, 번호 목록)이나 제목 없이 순수한 문장으로만 구성하세요.
- 데이터에 없는 내용이나 일반 상식(계절적 특성 등 추상적 설명 포함)을 지어내지 마세요.
- 같은 구절을 문장 안에서 반복하지 마세요(예: "60.42%의 지분을 보유한 60.42%의 지분을 보유한"처럼 같은 어구를 두 번 잇는 것 금지).
- 응답은 순수 한국어로만 작성하세요. 한자·중국어·일본어 문자는 하나도 섞지 마세요(숫자, USD/KRW 같은 알파벳 약어는 예외).
- 영어 원문을 그대로 남기지 말고 완전한 한국어 문장으로 재구성하세요(고유명사는 예외).
- 응답은 문장 텍스트만 그대로 출력하세요. 따옴표나 설명, 마크다운 코드블록으로 감싸지 마세요.

문장 수: 부연할 구체적 데이터가 있으면 2문장(첫 문장=핵심 사실, 두 번째 문장=데이터 근거 부연), 없으면 1문장만 — 문장 수를 맞추려고 없는 내용을 지어내지 마세요.`

const weatherSectionSystemPrompt = briefingCommonRules + `

당신은 날씨 데이터를 바탕으로 하루 브리핑의 날씨 문장을 작성하는 비서입니다. [날씨 데이터]의 current.cityLabel 값을 다른 도시로 착각하지 말고 정확히 그대로 사용하세요. today.morning이 있으면 오전 8시, today.afternoon이 있으면 오후 2시 시점의 실제 관측/예보 값입니다.

중요 — today.morning과 today.afternoon은 둘 다 있을 수도, 하나만 있을 수도, 둘 다 없을 수도 있습니다(그 시각 데이터를 신뢰할 수 없어 아예 제공하지 않은 경우입니다). JSON에 없는 필드는 반드시 완전히 무시하세요 — "오전 8시엔 0도"처럼 없는 시간대의 값을 0이나 다른 숫자로 지어내거나 언급하는 것은 절대 금지입니다. 있는 필드만으로 자연스러운 문장을 구성하세요:
- morning/afternoon 둘 다 있으면: 기존처럼 두 시점을 모두 언급하세요.
- 하나만 있으면: 그 하나만 언급하고 없는 시간대는 아예 문장에서 빼세요(예: afternoon만 있으면 "오늘 낮 최고 35도까지 오르며 맑은 날씨입니다"처럼 자연스럽게).
- 둘 다 없으면: 오전/오후 시각 언급 없이 current(현재) 값만으로 문장을 구성하세요.

첫 문장은 (있다면) 오전 8시/오후 2시 기준 날씨를, 없다면 현재 날씨를 반영하세요. umbrellaAdvice.needed가 true이면 umbrellaAdvice.period에 적힌 시점을 그대로 언급하며 우산 준비를 권하세요. 예: "오늘 오후 2시경 비가 올 가능성이 있어 외출 시 우산을 준비하세요." umbrellaAdvice.needed가 false이면 우산이나 비 가능성을 절대 언급하지 말고, 대신 실용적 조언(예: 야외활동, 자외선, 환기 등)을 하세요. umbrellaAdvice.needed는 이미 계산된 값이니 precipProbability 숫자를 직접 비교하려 하지 말고 이 값을 그대로 따르세요. 두 번째 문장은 today.morning/today.afternoon 중 실제로 존재하는 필드의 temperatureC 값만 그대로 사용해서 "오전 8시엔 X도, 오후 2시엔 Y도이며 맑은 하늘이 이어집니다" (또는 하나만 있으면 그 하나만) 형태로 작성하세요 (X, Y는 실제 데이터 값으로 치환 — 아래 예시의 숫자를 베끼지 마세요).

예시 (문장 구조 참고용일 뿐이며, 아래 숫자·도시 이름을 절대 그대로 베끼지 말고 실제 데이터 값으로 바꿔서 작성하세요):
서울은 오늘 대체로 맑아 우산 없이 외출하기 좋은 날씨입니다. 오전 8시엔 18도, 오후 2시엔 23도이며 맑은 하늘이 이어집니다.`

const exchangeSectionSystemPrompt = briefingCommonRules + `

당신은 환율 데이터를 바탕으로 하루 브리핑의 환율 문장을 작성하는 비서입니다. [환율 데이터]의 rate 값은 이미 사람이 읽기 자연스러운 방향으로 정해진 baseCurrency/quoteCurrency 기준으로 계산되어 있습니다 — baseCurrency의 baseUnits 단위(보통 1, 단 JPY(엔화)가 관여하면 국제 관례상 100)가 quoteCurrency로 얼마인지를 나타냅니다. rate 값은 그대로 주어진 숫자를 사용하세요 — 1보다 작을 수도 있습니다(예: 1 USD당 0.92 EUR처럼). 그 경우에도 rate 값을 임의로 뒤집거나 다른 값을 상상해서 만들어내지 말고 주어진 그대로 사용하세요. rate 값이 오늘 기준으로 상승/하락인지 임의로 판단하지 말고, 단순히 "환율은 {baseUnits} {baseCurrency}당 {rate} {quoteCurrency}입니다" 형태의 현재 환율 수준과 그것이 의미하는 실용적 조언만 작성하세요 — baseUnits 값을 반드시 그대로 사용하세요(baseUnits가 100이면 "100 JPY당"처럼 표현하고, 절대 "1 JPY당"으로 쓰지 마세요). baseCurrency/quoteCurrency 순서를 절대 바꾸지 마세요. 첫 문장은 반드시 "환율은"으로 시작해야 합니다. trend가 null이 아니면 두 번째 문장에 반드시 changePercent 값과 trend.direction, trend.implication을 그대로 사용해 "지난 7일간 1.2% {trend.direction}해 {trend.implication}를 보이고 있습니다" 형태로 실제 추세를 포함하세요 — 상승/하락 방향이나 약세/강세 여부를 직접 판단하지 말고 trend.direction과 trend.implication에 이미 계산되어 있는 값을 그대로 따르세요. trend가 null이 아닌데 이 문장을 생략하면 안 됩니다. trend가 null이면 추세를 지어내지 말고 1개의 문장만 작성하세요.

예시 (형식 참고용일 뿐이며, 아래 숫자를 절대 그대로 베끼지 말고 실제 데이터 값으로 바꿔서 작성하세요 — baseUnits가 1일 때와 100(JPY)일 때 두 형태를 참고하세요):
환율은 1 USD당 1320.55 KRW입니다. 지난 7일간 환율은 1.3% 하락해 원화가 소폭 강세를 보이고 있습니다.
환율은 100 JPY당 905.00 KRW입니다. 지난 7일간 환율은 0.5% 상승해 엔화가 소폭 강세를 보이고 있습니다.`

// 아래는 실측 결과 6,148토큰까지 늘어나 llama-3.1-8b-instant의 분당 한도
// (6,000 TPM)를 단일 요청만으로 초과시켰던 뉴스 섹션 프롬프트를 압축한
// 버전입니다 — 각 "절대 규칙"이 막으려는 구체적 hallucination 사례(계약
// 상대방 지어내기, 주가를 지분율로 둔갑시키기)는 grounding 예시로 남기되,
// 반복되던 수식어와 부연 설명은 덜어냈습니다. briefingCommonRules에 이미
// 있는 "반복 구절 금지"는 여기서 다시 쓰지 않습니다.
//
// 이 프롬프트는 국내/해외 뉴스 브리핑에 공통으로 쓰이므로(newsBriefingCacheKey
// 참고), 카테고리를 특정 분야로 못박지 않는 범용 페르소나여야 합니다.
// 예전에는 이 서비스가 Hacker News 전용이었던 시절의 흔적으로 itTermGlossary
// (IT/AI 전문 용어집)가 여기 그대로 포함돼 있었는데, 실제로는 top/business/
// technology/sports/entertainment/health/science 등 모든 카테고리가 이
// 프롬프트를 공유하다 보니, 예를 들어 "청소년 화장품 압수" 같은 완전히
// 무관한 기사도 IT 용어 목록을 참고하라는 지침 아래에서 요약되면서 AI/기술
// 관련 내용으로 왜곡되는 사례가 있었습니다. 용어집은 번역이 실제로 필요한
// 해외 모드 헤드라인 번역 프롬프트(news_translation.go의
// newsTranslationSystemPrompt)에만 남기고 여기서는 제거했습니다.
const newsSectionSystemPrompt = briefingCommonRules + `

당신은 다양한 분야(정치, 경제, 사회, 문화, 스포츠, 기술 등)의 뉴스 헤드라인 중 하나를 골라 하루 브리핑의 뉴스 문장을 작성하는 비서입니다. 뉴스의 실제 카테고리나 소재를 임의로 특정 분야(기술, AI, 의료 등)로 바꾸거나 재해석하지 마세요. 각 항목은 title(제목)과 description(설명)으로 구성됩니다. 숫자·명칭·구체적 성과가 있는 항목을 우선 선택하고, "우리의 입장" 같은 추상적 의견 제목이라 구체적 사실이 없다면 건너뛰고 다른 항목을 고르세요.

절대 규칙:
1. 근거 없는 내용 금지 — 요약할 항목의 title과 description을 반드시 먼저 읽고 그 안의 핵심 사건·소재만 다루세요. 원문에 없는 새로운 사건, 기술, 인물, 상황, 회사명·인명·기관명·계약 상대방을 절대 지어내지 마세요(예: 계약 상대방이 description에 없으면 "A사가 B사와 계약"처럼 상대방을 지어내지 말 것). 원문의 실제 주제와 다른 분야로 바꿔 서술하는 것도 금지합니다. 덧붙일 사실이 없으면 title만으로 짧은 문장 하나만 쓰세요.
2. 숫자 단위/의미 바꿔치기 금지 — 숫자는 원래 의미(가격/금액/인원수 등) 그대로만 쓰고 다른 의미로 재해석하지 마세요(예: "60.42 USD"를 "60.42%"로 둔갑시키는 것 금지). title/description에 %가 없으면 %를 지어내지 마세요.
3. 최소 1개의 구체적 사실(숫자, 명칭, 사건)을 포함하고, "다양한 논의가 진행 중입니다" 류의 내용 없는 문장은 금지합니다. K/M/B 단위는 이미 한국어로 환산되어 있으니(예: "9B"→"90억") 그 값을 그대로 쓰고 다시 계산하지 마세요.
4. 두 번째 문장은 첫 문장과 같은 항목의 다른 구체적 사실로 채우고, 부연할 사실이 없으면 1문장만 쓰세요.

예시(형식 참고용, 아래 내용을 베끼지 말고 실제 title/description으로 바꿔서 작성 — 실제 기사가 어느 분야든 그 분야 그대로 요약하세요):
한 스타트업이 12명 규모의 팀으로 5000만 달러 투자를 유치했다는 소식이 전해졌습니다. 이는 직원 1인당 약 400만 달러에 해당하는 규모로, 업계에서도 이례적인 사례로 주목받고 있습니다.`

// maxSectionRegenerations는 어떤 검사(또는 몇 개의 검사)가 실패했는지와
// 무관하게, 섹션 하나의 콘텐츠 검증 전체 예산을 공유되는 재시도 한 번으로
// 제한합니다. 이전에는 강한 실패(CJK/새어나온 영어/근거 없는 숫자/근거
// 없는 고유명사)와 약한 금칙어 검사가 각자 독립적으로 재시도 횟수를
// 추적했기 때문에, 시도마다 다른 검사가 번갈아 실패하면 섹션 하나에
// Groq 호출이 최대 3번까지 들 수 있었습니다. 전체를 합쳐 재시도 1회로
// 제한하면(각 시도에서도 모든 검사는 여전히 전부 실행됩니다) 섹션 하나에
// Groq 호출이 절대 2번을 넘지 않으며, 그 단 한 번의 재시도는 같은 모델을
// 다시 굴리는 데 쓰이는 게 아니라 더 정확한 모델로 승격하는 데 쓰입니다.
const maxSectionRegenerations = 1

// validateSectionOutput은 생성된 섹션에 대해 모든 콘텐츠 검사를 고정된
// 우선순위 순서(CJK -> 영어 유출 -> 반복 구문 -> 근거 없는 숫자 -> 주제
// 불일치 -> 조작된 퍼센트 -> 근거 없는 고유명사 -> 금칙 문구)로 실행하고
// 처음 발견된 실패를 보고합니다. hardFailure는 "절대 그대로 내보낼 수
// 없는" 실패(CJK 오염, 새어나온 영어, 근거 없는 숫자, 주제 불일치, 근거
// 없는 고유명사)와 "약한" 금칙어/톤 검사를 구분하며, 이 값에 따라
// resolveBriefingSection의 호출자가 재시도 예산 소진을 오류로 취급할지
// 아니면 마지막 결과를 그대로 내보낼지가 결정됩니다.
// useFallback은 (뉴스 전용) 주제 불일치/퍼센트 조작/고유명사 검사에서만
// true가 됩니다 — generateSectionText 참고.
func validateSectionOutput(combined string, allowedNumbers []float64, groundingText string) (reason string, hardFailure, useFallback bool) {
	if match, found := findForeignCJK(combined); found {
		return fmt.Sprintf("한자/CJK 문자 감지(%q)", match), true, false
	}
	if match, found := findLeakedEnglish(combined); found {
		return fmt.Sprintf("번역되지 않은 영어(%q) 감지", match), true, false
	}
	if phrase, found := findRepeatedPhrase(combined); found {
		return fmt.Sprintf("반복되는 구절(%q) 감지(생성 루프 의심)", phrase), true, false
	}
	if num, found := findUngroundedNumber(combined, allowedNumbers); found {
		return fmt.Sprintf("근거 없는 숫자 감지(%v)", num), true, false
	}
	if ratio, found := findTopicMismatch(combined, groundingText); found {
		return fmt.Sprintf("원문과 무관한 주제로 생성된 것으로 의심됨(토큰 중복도 %.0f%%)", ratio*100), true, true
	}
	if match, found := findFabricatedPercentage(combined, groundingText); found {
		return fmt.Sprintf("원문에 없는 퍼센트(%q) 감지(hallucination 의심)", match), true, true
	}
	if match, found := findUngroundedProperNoun(combined, groundingText); found {
		return fmt.Sprintf("원문에 없는 고유명사(%q) 감지(hallucination 의심)", match), true, true
	}
	if phrase, softViolated := findBannedPhrase(combined); softViolated {
		return fmt.Sprintf("금칙어 감지(%q)", phrase), false, false
	}
	return "", false, false
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

// generateSectionText는 Groq에 순수 텍스트 응답을 요청합니다(jsonMode=false
// — 예전에는 {"simple":"...","detailed":"..."} JSON을 요청했지만, 출력
// 토큰을 줄이고 프롬프트를 단순화하기 위해 detailed 하나만 남기고 JSON
// 구조 자체를 없앴습니다). 모델이 지침을 어기고 따옴표나 코드블록으로
// 감싸서 응답하는 경우에 대비해 trimSurroundingQuotes로 방어적으로
// 벗겨냅니다.
func generateSectionText(ctx context.Context, name, model, systemPrompt, userContent string, allowedNumbers []float64, groundingText, hallucinationFallback string) (text string, err error) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		return "", errGroqKeyMissing
	}

	currentModel := model

	for attempt := 0; attempt <= maxSectionRegenerations; attempt++ {
		content, callErr := callGroqChat(ctx, apiKey, currentModel, []groqChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		}, briefingSectionTemperature, briefingSectionMaxTokens, false)
		if callErr != nil {
			return "", callErr
		}

		text = trimSurroundingQuotes(strings.TrimSpace(content))
		if text == "" {
			return "", fmt.Errorf("%s briefing response was empty", name)
		}

		reason, hardFailure, useFallback := validateSectionOutput(text, allowedNumbers, groundingText)
		if reason == "" {
			return text, nil
		}

		// 검증 실패의 원문 전체를 로그로 남깁니다 — reason에는 매칭된 짧은
		// 구절만 담기므로, 반복이 정확히 어느 지점부터 시작됐는지 보려면
		// 전체 텍스트가 필요합니다.
		log.Printf("브리핑(%s) 시도 %d/%d 검증 실패: %s\n전체 응답: %s", name, attempt+1, maxSectionRegenerations+1, reason, text)

		if attempt >= maxSectionRegenerations {
			if hardFailure {
				if useFallback && hallucinationFallback != "" {
					log.Printf("브리핑(%s): 재시도 후에도 %s, 제목 기반 안전 문구로 대체", name, reason)
					return hallucinationFallback, nil
				}
				return "", fmt.Errorf("%s: %w (%s 반복 감지)", name, errBriefingValidationFailed, reason)
			}
			log.Printf("브리핑(%s): 재시도 후에도 %s — 마지막 결과를 그대로 사용합니다", name, reason)
			return text, nil
		}

		if groqEscalationCountToday() >= maxDailyGroqEscalations {
			log.Printf("브리핑(%s): %s, 그러나 오늘 70B 승격 횟수가 안전 한도(%d회)에 도달해 승격 없이 마지막 결과를 사용합니다", name, reason, maxDailyGroqEscalations)
			if hardFailure {
				if useFallback && hallucinationFallback != "" {
					return hallucinationFallback, nil
				}
				return "", fmt.Errorf("%s: %w (%s, 승격 한도 도달)", name, errBriefingValidationFailed, reason)
			}
			return text, nil
		}

		escalated := escalationGroqModel()
		log.Printf("브리핑(%s): %s, 모델 승격 후 재생성 시도 (%s -> %s, 오늘 승격 %d/%d회째)",
			name, reason, currentModel, escalated, groqEscalationCountToday()+1, maxDailyGroqEscalations)
		currentModel = escalated
	}

	// 도달 불가능한 코드: 위의 attempt == maxSectionRegenerations 분기
	// 안에서 루프가 항상 return하므로 여기까지 오지 않습니다.
	return text, nil
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

type briefingSectionCacheRow struct {
	dataHash    string
	text        string
	generatedAt time.Time
}

func lookupBriefingSectionCache(ctx context.Context, conn *sql.DB, section string) (briefingSectionCacheRow, bool) {
	if conn == nil {
		return briefingSectionCacheRow{}, false
	}

	var row briefingSectionCacheRow
	err := conn.QueryRowContext(ctx,
		`SELECT data_hash, detailed_text, generated_at FROM briefing_section_cache WHERE section = ?`, section,
	).Scan(&row.dataHash, &row.text, &row.generatedAt)
	if err != nil {
		return briefingSectionCacheRow{}, false
	}
	return row, true
}

// simple_text 컬럼은 더 이상 쓰이지 않는다(브리핑이 simple/detailed 두
// 버전 대신 텍스트 하나만 생성하도록 단순화됐다) — INSERT에서 아예
// 빼버려서 NULL로 남긴다(db.go의 makeSimpleTextNullable 마이그레이션이
// 이 컬럼의 NOT NULL 제약을 미리 풀어둔다).
func upsertBriefingSectionCache(ctx context.Context, conn *sql.DB, section, dataHash, text string, generatedAt time.Time) error {
	if conn == nil {
		return nil
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO briefing_section_cache (section, data_hash, detailed_text, generated_at)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE data_hash = VALUES(data_hash), detailed_text = VALUES(detailed_text), generated_at = VALUES(generated_at)`,
		section, dataHash, text, generatedAt,
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
func resolveBriefingSection(ctx context.Context, section, model, hash, systemPrompt, userContent string, allowedNumbers []float64, groundingText, hallucinationFallback string) briefingSectionOutput {
	cached, found := lookupBriefingSectionCache(ctx, db, section)
	if found && cached.dataHash == hash {
		recordGroqCacheHit()
		log.Printf("[캐시 재사용] 브리핑(%s): 입력 데이터 변경 없음 (Groq 미호출)", section)
		return briefingSectionOutput{Text: cached.text, Cached: true, GeneratedAt: cached.generatedAt, Status: briefingStatusCached}
	}

	if found {
		log.Printf("브리핑(%s): 입력 데이터 변경 감지, Groq 재호출 (모델: %s)", section, model)
	} else {
		log.Printf("브리핑(%s): 캐시 없음, Groq 최초 호출 (모델: %s)", section, model)
	}

	text, err := generateSectionText(ctx, section, model, systemPrompt, userContent, allowedNumbers, groundingText, hallucinationFallback)
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
		if errors.Is(err, errBriefingValidationFailed) {
			// 조용히 대체할 오래된 캐시가 없고, 재시도 후에도 이 섹션이 여전히
			// 강한 콘텐츠 검증(CJK/새어나온 영어)에 실패한 경우입니다 — 합쳐진
			// 브리핑에서 이 섹션을 조용히 빼는 대신 명시적으로 표시합니다.
			return briefingSectionOutput{Text: "⚠️ 생성 실패", GeneratedAt: time.Now(), Status: briefingStatusFailed, FailureReason: reason}
		}
		return briefingSectionOutput{Status: briefingStatusFailed, FailureReason: reason}
	}

	generatedAt := time.Now()
	if upsertErr := upsertBriefingSectionCache(ctx, db, section, hash, text, generatedAt); upsertErr != nil {
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
		// groundingText/hallucinationFallback은 뉴스 작업에서만 설정됩니다 —
		// findUngroundedProperNoun과 generateSectionText의 문서 주석 참고.
		groundingText         string
		hallucinationFallback string
	}
	// 세 섹션 모두 첫 시도에는 frequentGroqModel()(저렴하고 쿼터가 넉넉한
	// 모델)을 사용합니다 — 브리핑 섹션은 캐시가 미스될 때마다(도시 전환,
	// 통화쌍 전환, 뉴스 카테고리 변경) 재생성되므로 호출 빈도가 높은
	// 지점이라 70B 모델의 하루 1,000회 쿼터를 금방 소진시킬 수 있습니다.
	// 8B 모델의 출력이 강한 콘텐츠 검증(CJK 오염, 새어나온 영어, 근거 없는
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
			name:           weatherBriefingCacheKey(weatherCity),
			model:          briefingModel,
			hash:           hashJSON(weatherInput),
			systemPrompt:   weatherSectionSystemPrompt,
			userContent:    fmt.Sprintf("[날씨 데이터]: %s\n\n위 데이터를 바탕으로 한국어 날씨 브리핑 문장을 작성하세요.", weatherJSON),
			allowedNumbers: allowedWeatherNumbers(weatherInput),
		},
		{
			name:           exchangeBriefingCacheKey(exchangeFrom, exchangeTo),
			model:          briefingModel,
			hash:           hashJSON(exchangeInput),
			systemPrompt:   exchangeSectionSystemPrompt,
			userContent:    fmt.Sprintf("[환율 데이터]: %s\n\n위 데이터를 바탕으로 한국어 환율 브리핑 문장을 작성하세요.", exchangeJSON),
			allowedNumbers: allowedExchangeNumbers(exchangeInput),
		},
		{
			name:                  newsBriefingCacheKey(newsRegion, newsCategory),
			model:                 briefingModel,
			hash:                  hashJSON(newsInput),
			systemPrompt:          newsSectionSystemPrompt,
			userContent:           fmt.Sprintf("[뉴스 데이터]: %s\n\n위 데이터를 바탕으로 한국어 뉴스 브리핑 문장을 작성하세요.", newsJSON),
			allowedNumbers:        allowedNewsNumbers(newsInput),
			groundingText:         newsGroundingText(newsInput),
			hallucinationFallback: newsHallucinationFallback(news),
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
			outputs[i] = resolveBriefingSection(ctx, j.name, j.model, j.hash, j.systemPrompt, j.userContent, j.allowedNumbers, j.groundingText, j.hallucinationFallback)
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
