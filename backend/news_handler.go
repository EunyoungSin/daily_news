package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// newsTimeout은 handler.go의 newsSectionTimeout과 같은 값을 쓴다 — 이
// 엔드포인트도 결국 같은 getCachedOrFetchNews/NewsData.io 호출 경로를
// 타므로, 브리핑용 내부 조회만 여유를 늘리고 뉴스 카드 자체는 그대로
// 8초로 남겨두면 카드 쪽만 여전히 쉽게 타임아웃되는 비대칭이 생긴다.
const newsTimeout = 12 * time.Second

// newsHandler는 (GET /api/dashboard에 묶여 있는 날씨/환율/브리핑과 달리)
// 독립된 엔드포인트다. 덕분에 뉴스 카드는 다른 섹션을 건드리지 않고
// 카테고리나 지역만 바꿔서 자체적으로 다시 조회할 수 있다 — 프론트엔드의
// App.tsx / useNews.ts 참고.
func newsHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), newsTimeout)
	defer cancel()

	query := r.URL.Query()
	category := normalizeNewsCategory(query.Get("category"))
	region := normalizeNewsRegion(query.Get("region"))

	data, notice, err := getCachedOrFetchNews(ctx, category, region)
	durationMs := time.Since(start).Milliseconds()

	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		json.NewEncoder(w).Encode(NewsSection{
			SectionMeta: SectionMeta{Success: false, DurationMs: durationMs, Error: "⚠️ 뉴스를 불러올 수 없습니다"},
		})
		return
	}

	json.NewEncoder(w).Encode(NewsSection{
		SectionMeta: SectionMeta{Success: true, DurationMs: durationMs, Notice: notice},
		Data:        data,
	})
}
