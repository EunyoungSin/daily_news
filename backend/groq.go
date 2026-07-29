package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"

var errGroqKeyMissing = errors.New("GROQ_API_KEY not set")

// frequentGroqModel은 호출량이 많은 모든 곳에서 사용된다 — 세 개의 AI
// 브리핑 섹션(weather/exchange/news, 캐시 미스마다 재생성됨)과 뉴스 헤드라인
// 번역(새로 발견된 헤드라인마다 배치 1회) — Groq의 소형 모델은 70B 모델
// (1,000 req/day, 100,000 tokens/day)보다 훨씬 여유로운 일일 쿼터(14,400
// req/day)를 갖고 있기 때문이다. generateSectionText와 fetchNewsTranslation
// 은 모두 이 모델의 출력이 하드 콘텐츠 검증에 실패하면 단 한 번의 재시도를
// escalationGroqModel로 에스컬레이션한다. 그래서 정확도가 중요한 출력은
// 실제로 필요할 때만 더 큰 모델을 사용하게 된다. GROQ_MODEL 환경변수로
// 재정의할 수 있다.
func frequentGroqModel() string {
	return envOrDefault("GROQ_MODEL", "llama-3.1-8b-instant")
}

// escalationGroqModel은 정확도가 더 높은 모델로, (a) 최대 주 1회만
// 생성되어 1,000 req/day 쿼터 대비 비용이 미미한 로또 AI 인사이트와,
// (b) generateSectionText/fetchNewsTranslation에서 검증 실패 시의 단 한
// 번뿐인 재시도 용도로 남겨둔 것이다. 반복적인 테스트 결과, 8B 모델은
// 더 큰 모델에서는 나타나지 않는, 사용자 눈에 보이는 결함들을 만들어냈다:
// 한국어 텍스트 사이에 뜬금없이 섞여 드는 중국어/일본어 문자, 번역되지
// 않고 그대로 남은 영어 구문, 잘못 계산된 숫자 단위 변환 등이다 — 이 모델은
// 호출량이 많은 곳의 기본값이 아니라, 바로 이런 경우들을 위한 폴백이다.
// GROQ_ESCALATION_MODEL 환경변수로 재정의할 수 있다.
func escalationGroqModel() string {
	return envOrDefault("GROQ_ESCALATION_MODEL", "llama-3.3-70b-versatile")
}

// groqUsage는 현재 KST 기준 날짜에 대해 실제 Groq API 호출 수(및 호출을
// 피한 캐시 히트 수)를 추적하여, 외부 메트릭 시스템 없이도
// /api/debug/groq-usage가 "오늘 실제로 몇 번 호출했는지"에 답할 수 있게
// 한다. 새로운 날짜에 호출/히트가 기록될 때마다 초기화되는데 — 별도의
// 백그라운드 타이머 없이, 기록할 때마다 게으르게(lazy) 날짜만 확인하는
// 방식이다.
var groqUsage = struct {
	mu             sync.Mutex
	day            string
	callsByModel   map[string]int
	cacheHits      int
	remainingReqs  string
	remainingToks  string
	lastHeaderTime time.Time
}{callsByModel: make(map[string]int)}

func groqUsageRolloverLocked() {
	today := time.Now().In(kst).Format("2006-01-02")
	if groqUsage.day != today {
		groqUsage.day = today
		groqUsage.callsByModel = make(map[string]int)
		groqUsage.cacheHits = 0
	}
}

// recordGroqCall은 사용량 보고를 위해 실제 Groq API 호출 1회를 센다.
// callGroqChat 자체 내부에서 호출되는데(브리핑 섹션, 뉴스 번역, 로또
// 인사이트 등 모든 호출부가 거쳐 가는 단 하나의 병목 지점이다), 그래서
// 각 호출부가 자기 자신을 계측(instrument)해야 한다는 걸 따로 기억할
// 필요가 없다.
func recordGroqCall(model string) {
	groqUsage.mu.Lock()
	defer groqUsage.mu.Unlock()
	groqUsageRolloverLocked()
	groqUsage.callsByModel[model]++
}

// recordGroqCacheHit은 피한 Groq 호출 1회를 센다 — Groq를 호출하는 대신
// 캐시된 결과를 재사용한 섹션, 번역 배치, 또는 로또 인사이트를 의미한다.
// 각 호출부는 자체적으로 "캐시 재사용" 메시지를 로깅하며, 이 함수는 그저
// /api/debug/groq-usage가 사용하는 동일한 카운터에 값을 더해줄 뿐이다.
func recordGroqCacheHit() {
	groqUsage.mu.Lock()
	defer groqUsage.mu.Unlock()
	groqUsageRolloverLocked()
	groqUsage.cacheHits++
}

// recordGroqRateLimitHeaders는 가장 최근 응답에 담긴 Groq 자체의
// rate-limit 기록(x-ratelimit-remaining-requests/-tokens)을 보관해두어,
// /api/debug/groq-usage가 Groq 스스로 생각하는 오늘의 남은 한도를 보고할
// 수 있게 한다 — 이는 우리 쪽 로컬 카운터에 대한 교차 검증 역할을 하는데,
// 로컬 카운터는 이 프로세스의 호출만 볼 수 있어서 여러 인스턴스가 하나의
// API 키를 공유하면 값이 어긋날 수 있기 때문이다.
func recordGroqRateLimitHeaders(header http.Header) {
	remainingReqs := header.Get("x-ratelimit-remaining-requests")
	remainingToks := header.Get("x-ratelimit-remaining-tokens")
	if remainingReqs == "" && remainingToks == "" {
		return
	}

	groqUsage.mu.Lock()
	groqUsage.remainingReqs = remainingReqs
	groqUsage.remainingToks = remainingToks
	groqUsage.lastHeaderTime = time.Now()
	groqUsage.mu.Unlock()

	log.Printf("[Groq 호출] 남은 한도 — 요청: %s, 토큰: %s", orDash(remainingReqs), orDash(remainingToks))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// groqUsageSnapshot은 GET /api/debug/groq-usage가 반환하는 JSON 형태다.
type groqUsageSnapshot struct {
	Day                 string         `json:"day"`
	CallsByModel        map[string]int `json:"callsByModel"`
	TotalCalls          int            `json:"totalCalls"`
	CacheHits           int            `json:"cacheHits"`
	RemainingRequests   string         `json:"remainingRequests,omitempty"`
	RemainingTokens     string         `json:"remainingTokens,omitempty"`
	RateLimitHeaderAsOf string         `json:"rateLimitHeaderAsOf,omitempty"`
}

func getGroqUsageSnapshot() groqUsageSnapshot {
	groqUsage.mu.Lock()
	defer groqUsage.mu.Unlock()
	groqUsageRolloverLocked()

	snapshot := groqUsageSnapshot{
		Day:               groqUsage.day,
		CallsByModel:      make(map[string]int, len(groqUsage.callsByModel)),
		CacheHits:         groqUsage.cacheHits,
		RemainingRequests: groqUsage.remainingReqs,
		RemainingTokens:   groqUsage.remainingToks,
	}
	for model, count := range groqUsage.callsByModel {
		snapshot.CallsByModel[model] = count
		snapshot.TotalCalls += count
	}
	if !groqUsage.lastHeaderTime.IsZero() {
		snapshot.RateLimitHeaderAsOf = groqUsage.lastHeaderTime.Format(time.RFC3339)
	}
	return snapshot
}

// groqUsageHandler는 GET /api/debug/groq-usage를 서빙한다 — 오늘 실제
// Groq 호출 횟수(모델별로 나뉘어 있어 frequentGroqModel/escalationGroqModel
// 사용 비율이 바로 보인다), 캐시가 피한 호출 수, 그리고 Groq 자신이 보고한
// 가장 최근의 x-ratelimit-remaining-* 값을, 서버 로그를 grep할 필요 없이
// 한곳에서 모두 보여준다.
func groqUsageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getGroqUsageSnapshot())
}

type groqChatRequest struct {
	Model          string              `json:"model"`
	Messages       []groqChatMessage   `json:"messages"`
	Temperature    float64             `json:"temperature"`
	ResponseFormat *groqResponseFormat `json:"response_format,omitempty"`
}

type groqResponseFormat struct {
	Type string `json:"type"`
}

type groqChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqChatResponse struct {
	Choices []struct {
		Message groqChatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// callGroqChat은 Groq에 chat-completion 요청을 보내고 첫 번째 choice의
// 원본 메시지 내용을 반환한다. jsonMode를 켜면 모델이 순수 JSON 객체로만
// 응답하도록 강제하는데(simple/detailed 필드가 필요한 대시보드 브리핑에서
// 사용), 자유 형식 텍스트만 원하는 호출부(로또 인사이트 등)는 false를
// 넘긴다.
func callGroqChat(ctx context.Context, apiKey, model string, messages []groqChatMessage, temperature float64, jsonMode bool) (string, error) {
	reqBody := groqChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
	}
	if jsonMode {
		reqBody.ResponseFormat = &groqResponseFormat{Type: "json_object"}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	recordGroqCall(model)
	log.Printf("[Groq 호출] model=%s", model)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	recordGroqRateLimitHeaders(resp.Header)

	var parsed groqChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			return "", fmt.Errorf("groq api error: %s", parsed.Error.Message)
		}
		return "", fmt.Errorf("groq api returned status %d", resp.StatusCode)
	}

	if len(parsed.Choices) == 0 {
		return "", errors.New("groq api returned no choices")
	}

	return parsed.Choices[0].Message.Content, nil
}
