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
		ctx, cancel := context.WithTimeout(r.Context(), sectionTimeout)
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
		ctx, cancel := context.WithTimeout(r.Context(), sectionTimeout)
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
		ctx, cancel := context.WithTimeout(r.Context(), sectionTimeout)
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
