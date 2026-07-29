package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
)

const lottoTimeout = 20 * time.Second

// lottoSyncMu는 동시 요청 간에 syncLottoDraws가 순차적으로만 실행되도록
// 직렬화한다. 겹치는 두 GET /api/lotto 요청이 동시에 같은 신규 회차를
// 각자 삽입하려 드는 상황을 막기 위함이다.
var lottoSyncMu sync.Mutex

func lottoHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), lottoTimeout)
	defer cancel()

	w.Header().Set("Content-Type", "application/json")

	if db == nil {
		writeLottoSection(w, LottoSection{
			SectionMeta: SectionMeta{
				Success:    false,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      "⚠️ 데이터베이스에 연결할 수 없습니다",
			},
		})
		return
	}

	lottoSyncMu.Lock()
	syncErr := syncLottoDraws(ctx, db)
	lottoSyncMu.Unlock()
	if syncErr != nil {
		log.Printf("로또: 회차 동기화 실패: %v", syncErr)
		// 계속 진행한다 — dhlottery의 일시적인 문제 때문에 섹션 전체를
		// 실패로 처리하지 않고, DB에 이미 있는 데이터로 응답한다.
	}

	data, err := buildLottoData(ctx, db)
	durationMs := time.Since(start).Milliseconds()

	if err != nil {
		writeLottoSection(w, LottoSection{
			SectionMeta: SectionMeta{Success: false, DurationMs: durationMs, Error: err.Error()},
		})
		return
	}

	writeLottoSection(w, LottoSection{
		SectionMeta: SectionMeta{Success: true, DurationMs: durationMs},
		Data:        data,
	})
}

func buildLottoData(ctx context.Context, conn *sql.DB) (*LottoData, error) {
	history, err := queryLottoHistory(ctx, conn, lottoHistoryWindow)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return nil, errors.New("아직 수집된 로또 회차가 없습니다")
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

	recommendation := getLottoRecommendation(ctx, conn, frequency, time.Now())

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
