package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// lottoTimeout은 이제 DB 조회(및 AI 인사이트/추천 계산)만을 위한 예산이다.
// dhlottery에서 회차를 실제로 채워 넣는, 오래 걸릴 수 있는 작업은
// lottoEnsureBackfillStarted를 통해 이 요청과 무관한 백그라운드
// goroutine(lottoBackfillTimeout)에서 처리되므로 더 이상 이 타임아웃 안에
// 묶여 있지 않다.
const lottoTimeout = 15 * time.Second

func lottoHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w.Header().Set("Content-Type", "application/json")

	if !lottoEnabled() {
		// DB 조회조차 하지 않고 곧바로 응답한다 — LOTTO_ENABLED=false는
		// dhlottery 쪽 문제(차단/장애)를 배포 환경에서 완전히 격리해
		// 나머지 섹션에 영향을 주지 않게 하려는 것이 목적이므로, 이 경로는
		// 어떤 외부 의존성(DB 포함)도 건드리지 않는다.
		writeLottoSection(w, LottoSection{
			SectionMeta: SectionMeta{
				Success:    true,
				DurationMs: time.Since(start).Milliseconds(),
				Notice:     "로또 섹션은 현재 점검 중입니다.",
			},
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lottoTimeout)
	defer cancel()

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

	// 채워야 할 회차가 있는지는 DB 조회만으로 빠르게 확인한다. 실제 채우기는
	// (이미 진행 중이 아니라면) 백그라운드에서 시작하고, 이 요청은 그 완료를
	// 기다리지 않는다 — 최초 50회 수집처럼 오래 걸리는 작업이 사용자 요청의
	// 타임아웃에 묶여 실패하는 것을 막기 위함이다.
	needsSync, syncCheckErr := lottoNeedsSync(ctx, db)
	if syncCheckErr != nil {
		log.Printf("로또: 동기화 필요 여부 확인 실패: %v", syncCheckErr)
	} else if needsSync {
		lottoEnsureBackfillStarted(db)
	}
	isBackfilling := needsSync || lottoIsBackfilling()

	data, err := buildLottoData(ctx, db)
	durationMs := time.Since(start).Milliseconds()

	if err != nil {
		writeLottoSection(w, LottoSection{
			SectionMeta:   SectionMeta{Success: false, DurationMs: durationMs, Error: err.Error()},
			IsBackfilling: isBackfilling,
		})
		return
	}

	if data == nil {
		// 아직 수집된 회차가 하나도 없다 — 에러가 아니라, 백그라운드 채우기가
		// 아직 끝나지 않은 정상적인 초기 상태다. 프론트엔드는 isBackfilling을
		// 보고 짧은 간격으로 다시 요청한다.
		writeLottoSection(w, LottoSection{
			SectionMeta: SectionMeta{
				Success:    true,
				DurationMs: durationMs,
				Notice:     "로또 데이터를 처음 준비하는 중입니다. 잠시 후 다시 확인해 주세요.",
			},
			IsBackfilling: true,
		})
		return
	}

	writeLottoSection(w, LottoSection{
		SectionMeta:   SectionMeta{Success: true, DurationMs: durationMs},
		IsBackfilling: isBackfilling,
		Data:          data,
	})
}

func buildLottoData(ctx context.Context, conn *sql.DB) (*LottoData, error) {
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
