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

// maxDailyGroqEscalations는 escalationGroqModel()(70B, 하루 1,000req 한도)로의
// 승격 횟수에 대한 안전장치다. 8B 모델의 반복 생성 버그 등으로 승격이
// 예상보다 자주 발생하면 하루 한도를 금방 소진할 수 있으므로, 이 값을
// 넘으면 그날은 더 이상 승격하지 않고 (호출부가) 마지막 8B 결과를 그대로
// 쓰거나 이전 캐시로 대체한다. 근본 원인(반복 생성)은 max_tokens/temperature
// 조정으로 먼저 줄이는 게 우선이며, 이 안전장치는 그래도 예상 밖으로 승격이
// 몰릴 때 하루 쿼터 전체가 바닥나는 것을 막는 마지막 방어선일 뿐이다.
const maxDailyGroqEscalations = 50

// groqEscalationCountToday는 오늘 이미 escalationGroqModel()로 실제 호출된
// 횟수를, 모든 Groq 호출을 계측하는 단일 지점인 groqUsage에서 그대로
// 읽어온다 — 별도의 카운터를 새로 만들 필요 없이 기존 계측을 재사용한다.
func groqEscalationCountToday() int {
	groqUsage.mu.Lock()
	defer groqUsage.mu.Unlock()
	groqUsageRolloverLocked()
	return groqUsage.callsByModel[escalationGroqModel()]
}

// estimateTokenCount는 Groq(라마 계열) 모델의 실제 토크나이저 없이, 프롬프트
// 구성 요소들의 상대적인 크기를 가늠해보기 위한 대략적인 근사치일 뿐이다 —
// 진짜 사용량은 callGroqChat이 Groq 응답의 usage.prompt_tokens를 그대로
// 로그로 남기므로 그 값이 실제 기준이다. ASCII 문자는 대략 4자당 1토큰,
// 한글 등 비ASCII 문자는 바이트 단위 폴백 인코딩 특성상 그보다 토큰
// 소비가 커서 문자당 약 1.5토큰으로 근사한다.
func estimateTokenCount(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return ascii/4 + other*3/2
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
	MaxTokens      int                 `json:"max_tokens,omitempty"`
	ResponseFormat *groqResponseFormat `json:"response_format,omitempty"`
}

type groqResponseFormat struct {
	Type string `json:"type"`
}

type groqChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// groqUsageInfo는 Groq(OpenAI 호환) 응답에 실려 오는 실제 토큰 사용량이다 —
// estimateTokenCount와 달리 이 값은 근사치가 아니라 Groq 자신이 계산한
// 진짜 수치다.
type groqUsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type groqChatResponse struct {
	Choices []struct {
		Message groqChatMessage `json:"message"`
	} `json:"choices"`
	Usage *groqUsageInfo `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// callGroqChat은 Groq에 chat-completion 요청을 보내고 첫 번째 choice의
// 원본 메시지 내용을 반환한다. jsonMode를 켜면 모델이 순수 JSON 객체로만
// 응답하도록 강제하는데(simple/detailed 필드가 필요한 대시보드 브리핑에서
// 사용), 자유 형식 텍스트만 원하는 호출부(로또 인사이트 등)는 false를
// 넘긴다. maxTokens는 반드시 호출부가 출력 길이에 맞춰 넉넉하지만 유한한
// 값을 넘겨야 한다 — 이전에는 이 필드 자체가 없어서 Groq의 모델별 기본
// 상한(보통 수천 토큰)이 그대로 적용됐는데, 모델이 반복 생성 루프에 빠지면
// 이 상한에 도달할 때까지 계속 토큰을 소비하며 응답이 느려지고 TPM
// 예산까지 갉아먹는 원인이 될 수 있었다. 짧은 출력을 기대하는 호출부일수록
// 더 낮은 maxTokens로 이런 루프를 훨씬 일찍 끊어낼 수 있다.
func callGroqChat(ctx context.Context, apiKey, model string, messages []groqChatMessage, temperature float64, maxTokens int, jsonMode bool) (string, error) {
	reqBody := groqChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   maxTokens,
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
	log.Printf("[Groq 호출] model=%s maxTokens=%d temperature=%.2f", model, maxTokens, temperature)

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

	// 이 로그가 이번 요청의 실제(추정치가 아닌) 토큰 사용량에 대한 근거다 —
	// TPM 한도 초과 여부는 이 값으로 판단해야 하며, briefing.go의
	// estimateTokenCount 기반 로그는 어느 구성 요소가 큰지 사전에 가늠하는
	// 용도일 뿐이다.
	if parsed.Usage != nil {
		log.Printf("[Groq 응답] model=%s promptTokens=%d completionTokens=%d totalTokens=%d",
			model, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, parsed.Usage.TotalTokens)
	}

	return parsed.Choices[0].Message.Content, nil
}
