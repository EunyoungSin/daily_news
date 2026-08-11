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
	"unicode"
)

// disclaimer("※ 통계적 재미를 위한 분석입니다.")는 이제 아예 요구하지
// 않는다 — 화면에 이미 고정 disclaimer(LottoRecommendation.tsx의
// lotto__rec-disclaimer, LottoCard.tsx의 lotto__disclaimer)가 있어서
// AI 인사이트 안에 또 붙일 필요가 없다는 판단이다. 정확히 3문장(최다
// 출현/최소 출현/최근 10회 특이 번호)만 요구하고, 그 앞뒤로 어떤 문장도
// 붙이지 말라고 명시한다 — 예전에 "3~4문장"처럼 범위를 주고 "요약
// 문장 금지"만 나열했을 때도 모델이 문장 수를 채우려고 구체적 정보
// 없는 마무리 문장을 계속 끼워 넣는 경향이 있었어서, 이번에는 아예
// 정확한 문장 수와 각 문장이 담을 내용을 못박고 예시 텍스트까지
// 그대로 넣어 그 형태를 따르게 한다. 그래도 모델이 습관적으로
// disclaimer를 다시 붙일 가능성에 대비해 getLottoAIInsight에서
// stripLeakedDisclaimer로 한 번 더 걸러낸다.

// insightPromptVersion은 lottoAISystemPrompt의 내용을 식별하는 버전
// 문자열이다 — getLottoAIInsight의 캐시 조회/저장 키에 포함되어, 통계
// 입력(data_hash)과 회차(latest_drw_no)가 그대로라도 프롬프트가 바뀌면
// 캐시를 무효화하고 Groq를 다시 호출하게 만든다. **system prompt(바로
// 아래 lottoAISystemPrompt)의 내용을 바꿀 때마다 이 문자열도 반드시
// 함께 올려야 한다** — 안 그러면 예전 프롬프트로 생성된 캐시 텍스트가
// 새 프롬프트 배포 후에도 그대로 서빙된다(2026-08 발생한 실제 사고:
// 3문장 고정 규칙으로 프롬프트를 바꿨는데 캐시 키에 버전이 없어서
// 예전 4문장짜리 캐시가 계속 나갔다).
const insightPromptVersion = "v3"

const lottoAISystemPrompt = `당신은 로또 당첨 데이터의 통계적 특징을 재미로 설명하는 어시스턴트입니다. 아래 내용으로 정확히 3문장만 작성하세요.

1문장: 최근 50회 데이터에서 가장 많이 출현한 번호(공동 1위가 있으면 모두 포함)와 그 횟수
2문장: 가장 적게 출현한 번호와 그 횟수
3문장: [최근 10회 출현 번호]에서 두드러지게 나온 번호 — "1번부터 45번까지의 번호 중"처럼 전체 번호 범위를 나열하지 말고 자연스럽게 표현

예시:
"최근 50회 로또 데이터를 분석한 결과, 번호별 출현 횟수에서 15번과 27번이 각각 11회로 가장 많이 출현한 것으로 나타났습니다.
반면, 14번은 3회로 가장 적게 출현했습니다.
또한, 최근 10회 동안에는 43번과 44번이 두드러지는 출현 빈도를 보였습니다."

규칙:
- 번호 추천이나 "이 번호가 유리하다"는 표현, 특정 조합이 다른 조합보다 당첨 가능성이 높다는 식의 표현은 절대 사용하지 마세요 — 순수 통계 사실만 설명하세요.
- 마지막에 disclaimer나 안내 문구, 마무리 문장을 절대 추가하지 마세요. 정확히 위 3문장만 출력하세요.`

// hashLottoInsightInput은 getLottoAIInsight가 실제로 모델에 넘기는 통계
// 입력(frequency + recentAppeared) 전체의 해시다 — latest_drw_no만으로
// 캐시 유효성을 판단하면, 관리자가 새 회차 없이 기존 회차의 오타만
// 정정했을 때(latest_drw_no는 그대로라서) 그 정정이 반영되지 않은 낡은
// 통계 기반 인사이트가 계속 재사용된다. 이 해시가 그런 경우까지 잡아낸다.
func hashLottoInsightInput(frequency map[int]int, recentAppeared []int) string {
	return hashJSON(struct {
		Frequency      map[int]int `json:"frequency"`
		RecentAppeared []int       `json:"recentAppeared"`
	}{frequency, recentAppeared})
}

// getLottoAIInsight는 주어진 최신 회차 + 통계 입력 해시에 대한 캐시가
// 있으면 그 값을 반환하고, 없으면 Groq를 한 번 호출해서 결과를 캐시한다
// — 다음 회차 요청 시(또는 같은 회차라도 기존 데이터가 정정되어 통계
// 입력이 바뀌었을 때)에는 캐시가 미스가 나서 다시 생성된다.
func getLottoAIInsight(ctx context.Context, conn *sql.DB, latestDrwNo int, frequency map[int]int, recentAppeared []int) (text string, cached bool, generatedAt time.Time, err error) {
	dataHash := hashLottoInsightInput(frequency, recentAppeared)

	var cachedText string
	var cachedAt time.Time
	scanErr := conn.QueryRowContext(ctx,
		`SELECT insight_text, generated_at FROM ai_insight_cache WHERE latest_drw_no = ? AND data_hash = ? AND prompt_version = ?`,
		latestDrwNo, dataHash, insightPromptVersion,
	).Scan(&cachedText, &cachedAt)

	if scanErr == nil {
		recordGroqCacheHit()
		log.Printf("[캐시 재사용] 로또 AI 인사이트(회차 %d)", latestDrwNo)
		return stripLeakedDisclaimer(cachedText), true, cachedAt, nil
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
	}, 0.3, 500, 0, false)
	if err != nil {
		return "", false, time.Time{}, err
	}
	content = stripLeakedDisclaimer(content)

	generatedAt = time.Now()
	_, execErr := conn.ExecContext(ctx, `
		INSERT INTO ai_insight_cache (latest_drw_no, insight_text, data_hash, prompt_version, generated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(latest_drw_no) DO UPDATE SET insight_text = excluded.insight_text, data_hash = excluded.data_hash, prompt_version = excluded.prompt_version, generated_at = excluded.generated_at`,
		latestDrwNo, content, dataHash, insightPromptVersion, generatedAt,
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
		"[번호별 출현 횟수(최근 50회)]: %s\n[최근 10회 출현 번호]: %v\n[최다 출현 번호]: %d번(%d회)\n[최소 출현 번호]: %d번(%d회)\n\n위 통계를 바탕으로 시스템 프롬프트의 규칙에 따라 정확히 3문장으로 로또 통계 인사이트를 작성하세요.",
		strings.Join(freqParts, ", "), recentAppeared, maxNum, maxCount, minNum, minCount,
	)
}

// splitLottoInsightSentences는 frontend/src/utils/sentenceSplit.ts의
// splitSentences와 동일한 규칙(소수점은 문장 경계로 보지 않음)으로
// '.' 기준 문장을 나눈다 — 그래야 stripLeakedDisclaimer가 disclaimer
// 문장 하나만 정확히 골라내고, 화면에 실제로 보일 나머지 문장(예:
// "1470.11원") 안의 마침표를 실수로 문장 경계로 오인하지 않는다.
func splitLottoInsightSentences(text string) []string {
	runes := []rune(text)
	sentences := make([]string, 0, 4)
	var current strings.Builder

	for i, ch := range runes {
		current.WriteRune(ch)
		if ch != '.' {
			continue
		}

		hasPrev := i > 0
		hasNext := i < len(runes)-1
		isDecimalPoint := hasPrev && hasNext && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1])
		isBoundary := !hasNext || unicode.IsSpace(runes[i+1])

		if !isDecimalPoint && isBoundary {
			sentences = append(sentences, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	if rest := strings.TrimSpace(current.String()); rest != "" {
		sentences = append(sentences, rest)
	}
	return sentences
}

// stripLeakedDisclaimer는 시스템 프롬프트가 더 이상 요구하지 않는
// "※ 통계적 재미를 위한 분석입니다." 같은 disclaimer 문장을 모델이
// 습관적으로 다시 붙이는 경우에 대비한 방어 로직이다 — "※"나
// "통계적 재미"가 들어간 문장만 통째로 제거하고 나머지 문장은
// 그대로 이어붙인다. 캐시에서 읽은 값과 새로 생성한 값 양쪽에 모두
// 적용해서, 이 규칙이 배포되기 전에 이미 disclaimer가 박혀서 캐시된
// 예전 행도 다음 조회부터는 깨끗하게 나간다(캐시를 수동으로 비울
// 필요가 없다).
func stripLeakedDisclaimer(text string) string {
	sentences := splitLottoInsightSentences(text)
	kept := make([]string, 0, len(sentences))
	for _, s := range sentences {
		if strings.Contains(s, "※") || strings.Contains(s, "통계적 재미") {
			continue
		}
		kept = append(kept, s)
	}
	return strings.Join(kept, " ")
}
