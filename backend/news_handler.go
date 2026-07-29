package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const newsTimeout = 8 * time.Second

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
