package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// lottoTimeout은 DB 조회(및 AI 인사이트/추천 계산)만을 위한 예산이다.
// dhlottery에서 회차를 실제로 채워 넣는, 오래 걸릴 수 있는 작업은 이제
// 이 핸들러가 전혀 트리거하지 않는다 — 화면의 ON/OFF 토글이 POST
// /api/lotto/collection/{start,stop}을 통해 명시적으로만 시작/중단한다
// (lotto_collection_handler.go 참고). 이 핸들러는 그저 현재 DB에 있는
// 데이터를 읽어서 보여줄 뿐이다.
const lottoTimeout = 15 * time.Second

// parseLottoMode는 ?mode= 쿼리 파라미터를 검증한다 — trend/regression/
// uniform 중 하나가 아니면(값이 없는 경우 포함) 조용히 기본값
// uniform으로 대체한다. uniform이 기본값인 이유는 다른 모드들과 달리
// 통계적으로 편향이 없기 때문이다.
func parseLottoMode(raw string) string {
	if isValidLottoMode(raw) {
		return raw
	}
	return lottoModeUniform
}

func lottoHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w.Header().Set("Content-Type", "application/json")

	if db == nil {
		writeLottoSection(w, LottoSection{
			SectionMeta: SectionMeta{
				Success:    false,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      "⚠️ 데이터베이스에 연결할 수 없습니다",
			},
			DBErrorType: currentDBErrorType(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lottoTimeout)
	defer cancel()

	isCollecting := lottoIsCollecting()

	mode := parseLottoMode(r.URL.Query().Get("mode"))
	data, err := buildLottoData(ctx, db, mode)
	durationMs := time.Since(start).Milliseconds()

	if err != nil {
		writeLottoSection(w, LottoSection{
			SectionMeta:   SectionMeta{Success: false, DurationMs: durationMs, Error: err.Error()},
			IsBackfilling: isCollecting,
		})
		return
	}

	if data == nil {
		// 아직 수집된 회차가 하나도 없다 — 에러가 아니다. 수집 중이면 곧
		// 채워질 것이고, 수집이 꺼져 있으면 사용자가 토글을 켜야 한다.
		notice := "아직 수집된 로또 데이터가 없습니다. 데이터 수집을 시작해 주세요."
		if isCollecting {
			notice = "로또 데이터를 수집하는 중입니다. 잠시 후 다시 확인해 주세요."
		}
		writeLottoSection(w, LottoSection{
			SectionMeta:   SectionMeta{Success: true, DurationMs: durationMs, Notice: notice},
			IsBackfilling: isCollecting,
		})
		return
	}

	// 이미 보여줄 데이터가 있다면 초기 채우기는 끝난 것이다 — 그 뒤로
	// "매주 자동 업데이트" 토글이 계속 켜져 있다는 사실과는 무관하다.
	// isCollecting을 여기서도 그대로 IsBackfilling에 넣으면, 토글이 켜져
	// 있는 동안 useLotto가 5초마다 이 무거운 GET /api/lotto(AI 인사이트
	// 캐시 조회 포함)를 영원히 폴링하게 된다 — GET
	// /api/lotto/collection/status의 가벼운 5초 폴링과 뒤섞여 로그에 매번
	// "AI 인사이트 캐시 재사용"이 따라붙던 원인이 바로 이것이었다.
	writeLottoSection(w, LottoSection{
		SectionMeta:   SectionMeta{Success: true, DurationMs: durationMs},
		IsBackfilling: false,
		Data:          data,
	})
}

func buildLottoData(ctx context.Context, conn *sql.DB, recommendationMode string) (*LottoData, error) {
	history, err := queryLottoHistory(ctx, conn, lottoHistoryWindow)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 {
		// 에러가 아니다 — 백그라운드 채우기가 아직 끝나지 않은 것뿐이므로,
		// 호출자(lottoHandler)가 isBackfilling 상태로 판단하게 둔다.
		return nil, nil
	}

	frequency, err := queryFrequency(ctx, conn, lottoHistoryWindow)
	if err != nil {
		return nil, err
	}

	recentAppeared, err := queryRecentAppeared(ctx, conn, lottoRecentWindow)
	if err != nil {
		return nil, err
	}

	latest := history[0]

	insightText, cached, generatedAt, insightErr := getLottoAIInsight(ctx, conn, latest.DrwNo, frequency, recentAppeared)

	var aiInsight LottoAIInsight
	if insightErr != nil {
		aiInsight = LottoAIInsight{Available: false, Text: "⚠️ AI 인사이트를 사용할 수 없습니다"}
	} else {
		aiInsight = LottoAIInsight{
			Available:   true,
			Text:        insightText,
			Cached:      cached,
			GeneratedAt: generatedAt.Format(time.RFC3339),
		}
	}

	recommendation := getLottoRecommendation(ctx, conn, history, frequency, latest.DrwNo, recommendationMode, time.Now())

	return &LottoData{
		Latest:         latest,
		History:        history,
		Frequency:      frequency,
		RecentAppeared: recentAppeared,
		AIInsight:      aiInsight,
		Recommendation: recommendation,
	}, nil
}

func writeLottoSection(w http.ResponseWriter, section LottoSection) {
	json.NewEncoder(w).Encode(section)
}
