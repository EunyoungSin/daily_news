package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

const lottoAISystemPrompt = `당신은 로또 당첨 데이터의 통계적 특징을 재미로 설명하는 어시스턴트입니다.
1. 최근 50회 데이터에서 발견되는 흥미로운 통계 패턴 1~2가지를 짧게 설명 (특정 번호 출현 빈도, 홀짝 비율, 번호대별 분포 등)
2. "과거 출현 빈도가 다음 회차 당첨 확률에 영향을 주지 않는다"는 점을 반드시 명확히 언급
3. 마지막 줄에 "※ 통계적 재미를 위한 분석입니다." 포함
4. 번호 추천이나 "이 번호가 유리하다"는 표현 절대 금지, 순수 통계 사실만 설명
5. 전체 4~5문장 이내`

// getLottoAIInsight는 주어진 최신 회차에 대한 캐시가 있으면 그 값을
// 반환하고, 없으면 Groq를 한 번 호출해서 그 회차 번호를 키로 결과를
// 캐시한다 — 다음 회차 요청 시에는 캐시가 미스가 나서 다시 생성된다.
func getLottoAIInsight(ctx context.Context, conn *sql.DB, latestDrwNo int, frequency map[int]int, recentAppeared []int) (text string, cached bool, generatedAt time.Time, err error) {
	var cachedText string
	var cachedAt time.Time
	scanErr := conn.QueryRowContext(ctx,
		`SELECT insight_text, generated_at FROM ai_insight_cache WHERE latest_drw_no = ?`, latestDrwNo,
	).Scan(&cachedText, &cachedAt)

	if scanErr == nil {
		recordGroqCacheHit()
		log.Printf("[캐시 재사용] 로또 AI 인사이트(회차 %d)", latestDrwNo)
		return cachedText, true, cachedAt, nil
	}
	if scanErr != sql.ErrNoRows {
		return "", false, time.Time{}, scanErr
	}

	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		return "", false, time.Time{}, errGroqKeyMissing
	}

	log.Printf("로또: 캐시 없음(회차 %d), Groq 호출 (모델: %s)", latestDrwNo, escalationGroqModel())
	userContent := buildLottoInsightPrompt(frequency, recentAppeared)
	content, err := callGroqChat(ctx, apiKey, escalationGroqModel(), []groqChatMessage{
		{Role: "system", Content: lottoAISystemPrompt},
		{Role: "user", Content: userContent},
	}, 0.3, false)
	if err != nil {
		return "", false, time.Time{}, err
	}

	generatedAt = time.Now()
	_, execErr := conn.ExecContext(ctx, `
		INSERT INTO ai_insight_cache (latest_drw_no, insight_text, generated_at)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE insight_text = VALUES(insight_text), generated_at = VALUES(generated_at)`,
		latestDrwNo, content, generatedAt,
	)
	if execErr != nil {
		log.Printf("로또: AI 인사이트 캐시 저장 실패: %v", execErr)
	}

	return content, false, generatedAt, nil
}

func buildLottoInsightPrompt(frequency map[int]int, recentAppeared []int) string {
	nums := make([]int, 0, len(frequency))
	for n := range frequency {
		nums = append(nums, n)
	}
	sort.Ints(nums)

	freqParts := make([]string, 0, len(nums))
	maxNum, maxCount := nums[0], frequency[nums[0]]
	minNum, minCount := nums[0], frequency[nums[0]]
	for _, n := range nums {
		freqParts = append(freqParts, fmt.Sprintf("%d번:%d회", n, frequency[n]))
		if frequency[n] > maxCount {
			maxNum, maxCount = n, frequency[n]
		}
		if frequency[n] < minCount {
			minNum, minCount = n, frequency[n]
		}
	}

	return fmt.Sprintf(
		"[번호별 출현 횟수(최근 50회)]: %s\n[최근 10회 출현 번호]: %v\n[최다 출현 번호]: %d번(%d회)\n[최소 출현 번호]: %d번(%d회)\n\n위 통계를 바탕으로 시스템 프롬프트의 규칙에 따라 로또 통계 인사이트를 작성하세요.",
		strings.Join(freqParts, ", "), recentAppeared, maxNum, maxCount, minNum, minCount,
	)
}
