package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

const sectionTimeout = 8 * time.Second

// weatherSectionTimeout은 날씨 섹션만을 위한 별도 예산이다. 기상청(KMA)
// 호출 자체가 최악의 경우 kmaSubTimeout(9초)까지 걸릴 수 있고, 그 이후
// Open-Meteo 폴백에도 시간이 더 필요하므로, 다른 섹션들과 같은
// sectionTimeout(8초)을 그대로 썼다가는 KMA가 실제로 응답하고 있는
// 도중에 잘려나가 버린다. 날씨만 별도 상수로 분리해서, 이 값을 늘려도
// 환율/뉴스/브리핑 섹션의 타임아웃에는 영향을 주지 않는다.
//
// 이미 지난 시각 슬롯(예: 오후 2시가 지난 뒤의 08:00/14:00 슬롯)이 API
// 응답과 DB 캐시 어디에도 없으면, weather_slot_cache.go가 최대 두 단계로
// 복구를 시도한다(각각 최대 backfillFetchTimeout=9초) — 메인 조회가 이미
// 예산을 다 쓴 뒤라도 최소 한 단계는 온전히 시도할 여유가 남도록, 기존
// 12초에 9초를 더했다. 그래도 메인 조회가 최악의 경우(9초)를 다 쓰고
// 게다가 두 복구 단계가 모두 게이트웨이 지연을 겪는, 여러 악조건이 겹치는
// 드문 경우에는 여전히 past_missing으로 남을 수 있다 — 그 정도까지 완전히
// 방지하려면 지연이 훨씬 길어질 수 있는데, 사용자가 느끼는 응답 지연이
// 더 크게 늘어나는 손해가 더 크다고 판단했다.
const weatherSectionTimeout = 21 * time.Second

// newsSectionTimeout은 뉴스(브리핑용 내부 조회)만을 위한 별도 예산이다 —
// weatherSectionTimeout과 같은 이유로 sectionTimeout(8초)에서 분리했다.
// 실측 결과 NewsData.io는 평소 1초 안팎으로 응답하지만, "context deadline
// exceeded"로 실패한 사례가 실제로 보고되어 약간의 여유를 더 뒀다 — 8초를
// 유지한 채 원인을 크레딧 소진/타임아웃 설정으로 좁혀본 결과 둘 다 원인이
// 아니었고(오늘 남은 크레딧 충분, 재시도 시 0.9초 만에 정상 응답) 일시적
// 외부 지연 쪽에 무게가 실렸으므로, 근본 원인을 알 수 없는 그런 순간적
// 지연을 흡수할 여지를 조금 늘리는 선에서 12초로 올렸다. 환율은 여전히
// sectionTimeout(8초)을 그대로 쓴다.
const newsSectionTimeout = 12 * time.Second

// briefingGenerationTimeout은 getBriefing이 날씨/환율/뉴스 3섹션의 AI 문장을
// 생성하는 단계(resolveBriefingSection -> generateSectionText -> Groq
// 호출)에 공유되는 예산이다. 위 raw-data 조회용 타임아웃들(sectionTimeout/
// weatherSectionTimeout/newsSectionTimeout)과는 완전히 별개의 단계다 —
// dashboardHandler는 raw 조회가 모두 끝난 뒤에야 이 단계를 순차적으로
// 시작한다(위 "AI 브리핑은 위 결과들에 의존" 주석 참고).
//
// 예전에는 이 단계도 그냥 sectionTimeout(8초)을 그대로 재사용했는데, Groq
// rate limit 재시도(callGroqChat의 "Please try again in {N}s" 대기)가
// 걸리면 8초는 너무 타이트했다 — 실제 사례: 대기 시간이 8.18초였는데
// 섹션 예산이 8초라, 기다리는 도중 ctx가 만료돼 재시도 자체를 시도해보지도
// 못한 채 "context deadline exceeded"로 실패했다(callGroqChat의
// groqRateLimitRetryBudgetRatio 문서 주석도 참고 — 이제는 그런 상황에서
// 아예 기다리지 않고 즉시 폴백하도록 고쳤지만, 그러면 짧은 대기(몇 초
// 수준)로 충분히 성공할 수 있었던 정상적인 재시도까지 예산 부족으로 함께
// 포기하게 된다). 날씨(21초)만큼 넉넉할 필요는 없지만(날씨는 KMA 자체
// 호출이 원래 느릴 수 있어 별도로 그렇게 잡아둔 것이다), rate limit 재시도
// 한 번(대기 최대 10초 + 호출 1~2초)이 실패 없이 들어갈 여유를 두어
// 15초로 잡았다. 환율은 이 단계에 rate limit 재시도로 인한 실패 사례가
// 보고된 적 없지만, 세 섹션이 이 예산을 공유하는 구조라 함께 늘어나는
// 것이 자연스럽고 손해도 없다.
const briefingGenerationTimeout = 15 * time.Second

func withTiming(fn func() error) (int64, error) {
	start := time.Now()
	err := fn()
	return time.Since(start).Milliseconds(), err
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	query := r.URL.Query()
	city := normalizeCity(query.Get("city"))
	from := query.Get("from")
	if from == "" {
		from = defaultFromCurrency
	}
	to := query.Get("to")
	if to == "" {
		to = defaultToCurrency
	}
	newsCategory := normalizeNewsCategory(query.Get("category"))
	newsRegion := normalizeNewsRegion(query.Get("region"))

	var weatherSection WeatherSection
	var exchangeSection ExchangeSection
	// 뉴스는 스트리밍 응답에 포함되지 않는다(뉴스 카드는 GET /api/news를
	// 통해 별도로 가져온다) — 여기서 가져오는 건 오직 아래 브리핑에
	// 넣기 위한 것으로, /api/news와 같은 캐시를 사용해서 같은 요청에
	// 대해 NewsData.io 크레딧을 두 번 쓰지 않도록 한다.
	var newsData *NewsData

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(r.Context(), weatherSectionTimeout)
		defer cancel()

		var data *WeatherData
		durationMs, err := withTiming(func() error {
			var innerErr error
			data, innerErr = getCachedOrFetchWeather(ctx, city)
			return innerErr
		})

		weatherSection = WeatherSection{
			SectionMeta: SectionMeta{Success: err == nil, DurationMs: durationMs, Error: errString(err)},
			Data:        data,
		}
	}()

	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(r.Context(), sectionTimeout)
		defer cancel()

		var data *ExchangeData
		durationMs, err := withTiming(func() error {
			var innerErr error
			data, innerErr = getCachedOrFetchExchange(ctx, from, to)
			return innerErr
		})

		exchangeSection = ExchangeSection{
			SectionMeta: SectionMeta{Success: err == nil, DurationMs: durationMs, Error: errString(err)},
			Data:        data,
		}
	}()

	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(r.Context(), newsSectionTimeout)
		defer cancel()

		data, _, err := getCachedOrFetchNews(ctx, newsCategory, newsRegion)
		if err != nil {
			log.Printf("뉴스(브리핑용 내부 조회): 실패: %v", err)
			return
		}
		newsData = data
	}()

	wg.Wait()

	// 부분 결과(날씨/환율)가 준비되는 즉시 스트리밍해서, 프론트엔드가
	// 아래의 더 느리고 순차적인 브리핑 단계를 기다리지 않고 두 카드를
	// 바로 렌더링할 수 있게 한다.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	writeNDJSON(w, DashboardResponse{
		Stage:    "partial",
		City:     city,
		From:     from,
		To:       to,
		Weather:  weatherSection,
		Exchange: exchangeSection,
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// AI 브리핑은 위 결과들에 의존하므로, 병렬 조회 단계와 동시에 실행하지
	// 않고 그 이후에 순차적으로 실행한다.
	var briefingSection BriefingSection
	{
		ctx, cancel := context.WithTimeout(r.Context(), briefingGenerationTimeout)
		defer cancel()

		var data *BriefingData
		durationMs, err := withTiming(func() error {
			var innerErr error
			data, innerErr = getBriefing(ctx, weatherSection.Data, exchangeSection.Data, newsData, newsCategory, newsRegion)
			return innerErr
		})

		errMsg := ""
		if err != nil {
			data = nil
			if err == errGroqKeyMissing {
				errMsg = "⚠️ AI 브리핑을 사용할 수 없습니다 (API 키 확인 필요)"
			} else {
				errMsg = "⚠️ AI 브리핑 생성에 실패했습니다"
			}
		}

		briefingSection = BriefingSection{
			SectionMeta: SectionMeta{Success: err == nil, DurationMs: durationMs, Error: errMsg},
			Data:        data,
		}
	}

	writeNDJSON(w, DashboardResponse{
		Stage:    "final",
		City:     city,
		From:     from,
		To:       to,
		Weather:  weatherSection,
		Exchange: exchangeSection,
		Briefing: briefingSection,
		TotalMs:  time.Since(start).Milliseconds(),
	})
}

func writeNDJSON(w http.ResponseWriter, v DashboardResponse) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return
	}
	w.Write(encoded)
	w.Write([]byte("\n"))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
