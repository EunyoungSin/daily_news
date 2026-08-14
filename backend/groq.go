package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// groqEndpoint는 var로 선언되어 있다 — 테스트가 실제 Groq API를 두드리지
// 않고 httptest 서버로 가리킬 수 있게 하기 위해서다.
var groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"

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
	Model            string              `json:"model"`
	Messages         []groqChatMessage   `json:"messages"`
	Temperature      float64             `json:"temperature"`
	MaxTokens        int                 `json:"max_tokens,omitempty"`
	FrequencyPenalty float64             `json:"frequency_penalty,omitempty"`
	ResponseFormat   *groqResponseFormat `json:"response_format,omitempty"`
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

// groqRetryAfterPattern은 Groq의 TPM(분당 토큰) rate-limit 에러 메시지에
// 실려 오는 "Please try again in 1.234s" 형태의 대기 시간 안내를 추출한다.
// (?i)로 대소문자를 가리지 않는다 — Groq 문서/실사용 사례에 "Please try
// again in"과 "please try again in"이 혼재해 관측된다.
var groqRetryAfterPattern = regexp.MustCompile(`(?i)try again in ([0-9]+(?:\.[0-9]+)?)s`)

// maxGroqRateLimitRetryWait는 rate-limit 재시도 한 번을 위해 기다려줄 수
// 있는 최대 시간이다. Groq 에러 메시지가 안내한 대기 시간이 이보다 길면
// (초과 폭이 커서 실제로 몇 분을 기다려야 하는 경우) 그 시점에서 더 이상
// 재시도하지 않고 바로 실패를 반환해 상위 호출부가 stale_fallback으로
// 넘어가게 한다 — 사용자를 무리하게 오래 기다리게 하지 않기 위해서다.
// 짧은 초과 폭(1~수 초)만 이 재시도로 복구를 시도한다. var로 선언한
// 이유는 groqCallGate의 같은 이유(테스트가 값을 줄여 빠르고 결정론적으로
// 검증할 수 있게 하기 위함)와 동일하다.
var maxGroqRateLimitRetryWait = 10 * time.Second

// maxGroqRateLimitRetries는 rate-limit 재시도의 최대 횟수(최초 시도는
// 포함하지 않음 — 총 시도 횟수는 이 값 + 1)다. 브리핑 3섹션(weather/
// exchange/news)과 뉴스 번역이 거의 동시에 Groq를 호출해 TPM 예산을 두고
// 경쟁하는 상황에서는, 재시도 시점에도 다른 호출들이 여전히 같은
// 분(minute) 버킷의 토큰을 소비하고 있어 1회 재시도만으로는 재시도마저
// 다시 rate limit에 걸릴 수 있다고 보고되었다. 재시도 횟수를 늘리되,
// 아래 maxGroqRateLimitTotalWait로 전체 대기 시간에 상한을 둬서 사용자가
// 무한정 기다리는 일은 없게 한다.
var maxGroqRateLimitRetries = 3

// maxGroqRateLimitTotalWait는 한 번의 callGroqChat 호출 안에서 모든
// rate-limit 재시도에 걸쳐 누적으로 기다려줄 수 있는 최대 시간이다. 매
// 재시도마다 maxGroqRateLimitRetryWait(10초) 이하인 대기라도, 여러 번
// 이어지면 총합이 사용자 체감상 너무 길어질 수 있어 별도로 전체 예산을
// 둔다. 다음 재시도의 대기 시간을 더하면 이 상한을 넘을 것으로 예상되면,
// 그 대기를 기다리지 않고 즉시 실패를 반환해 stale_fallback으로 넘어가게
// 한다.
var maxGroqRateLimitTotalWait = 20 * time.Second

// groqRateLimitRetryBuffer는 파싱한 대기 시간에 더하는 여유분이다 — Groq가
// 안내한 시점에 정확히 맞춰 재요청하면 타이밍 오차로 다시 거부될 수 있어
// 약간 더 기다린다.
var groqRateLimitRetryBuffer = 500 * time.Millisecond

// groqRateLimitRetryCallOverhead는 대기 후 실제로 다시 API를 호출하는 데
// 걸릴 것으로 예상하는 시간이다 — 대기 시간만 남은 ctx 예산 안에 든다고
// 재시도를 허용하면, 대기 후 실제 호출을 마치기도 전에 ctx가 만료돼
// "기다렸는데도 결국 실패"하는 낭비가 생긴다. Groq 응답은 보통 1~2초
// 이내이므로 여유를 두어 2초로 잡는다.
var groqRateLimitRetryCallOverhead = 2 * time.Second

// groqRateLimitRetryBudgetRatio는 남은 ctx 예산 중 이 비율 이상을
// 대기 시간이 차지하면 재시도 자체를 포기하는 기준이다 — 예산을 거의 다
// 써버리는 재시도는 설령 성공하더라도 상위 호출부(resolveBriefingSection
// 등)가 결과를 처리할 시간조차 남기지 않아 결국 context deadline
// exceeded로 이어지기 쉽다.
var groqRateLimitRetryBudgetRatio = 0.8

// parseGroqRetryAfterSeconds는 Groq rate-limit 에러 메시지(예: "Rate limit
// reached for model ... Please try again in 1.2s.")에서 대기 시간을
// time.Duration으로 추출한다. 메시지 형식이 다르거나(다른 종류의 에러) 숫자를
// 파싱할 수 없으면 ok=false를 반환한다. 재시도 때마다 그 시점의 최신 에러
// 메시지로 이 함수를 다시 호출해야 한다 — 여러 Groq 호출이 TPM 예산을
// 두고 경쟁하는 상황에서는 재시도할 때마다 Groq가 안내하는 대기 시간
// 자체가 매번 달라지므로(예: 1차 실패 때는 1.2초였다가, 다른 호출이 그
// 사이 더 많은 토큰을 소비해 2차 실패 때는 4.5초로 늘어나는 식), 최초
// 대기 시간을 재사용하면 실제로는 아직 부족한 시간만큼만 기다리고 다시
// 시도하게 되어 재시도가 헛수고가 되기 쉽다.
func parseGroqRetryAfterSeconds(errMsg string) (wait time.Duration, ok bool) {
	match := groqRetryAfterPattern.FindStringSubmatch(errMsg)
	if match == nil {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

// maxConcurrentGroqCalls는 동시에 실제 HTTP로 전송될 수 있는 Groq 호출의
// 최대 개수다. 날씨/환율/뉴스 브리핑 3섹션(getBriefing이 goroutine으로
// 병렬 실행)과 뉴스 헤드라인 번역이, 도시+통화+뉴스 카테고리를 한꺼번에
// 바꾸는 등의 상황에서 거의 동시에 Groq를 호출할 수 있는데(최악의 경우
// 최대 4개), 이 호출들이 전부 같은 순간에 나가면 서로 TPM(분당 토큰)
// 예산을 두고 경쟁해 하나가 rate limit에 걸리면 나머지도 함께 걸리기
// 쉬워진다. 2로 제한한 이유는 완전히 순차(1개씩)로 만들면 브리핑 3섹션의
// 체감 응답 시간이 3배로 늘어나지만, 2개씩만 허용해도 "4개가 한 순간에
// 몰리는" 최악의 경우를 피하기에는 충분하기 때문이다. var로 선언한 이유는
// 테스트가 이 값을 낮춰(예: 1) 순서를 결정론적으로 검증할 수 있게
// 하기 위해서다 — resetGroqCallGateForTest 참고.
var maxConcurrentGroqCalls = 2

// groqCallStagger는 Groq 호출들이 세마포어 슬롯을 얻더라도 시작 시각
// 자체는 최소 이만큼 벌리기 위한 간격이다. 세마포어만으로는 슬롯이
// 비어있는 순간 두 호출이 동시에 시작되는 것을 막지 못하는데, 사용자가
// 체감하기 어려운 수준(200~300ms)으로 시작 시각을 벌리기만 해도 같은
// 분(minute) 버킷 안에서 순간적으로 몰리는 토큰 소비량의 피크를 낮출 수
// 있다. var로 선언한 이유는 groqCallStagger와 동일하다.
var groqCallStagger = 250 * time.Millisecond

// groqCallGate는 maxConcurrentGroqCalls/groqCallStagger를 실제로 강제하는
// 세마포어(+시작 시각 기록)다. acquireGroqCallSlot을 통해서만 접근한다.
type groqCallGateState struct {
	sem       chan struct{}
	mu        sync.Mutex
	lastStart time.Time
}

func newGroqCallGate(maxConcurrent int) *groqCallGateState {
	return &groqCallGateState{sem: make(chan struct{}, maxConcurrent)}
}

var groqCallGate = newGroqCallGate(maxConcurrentGroqCalls)

// acquireGroqCallSlot은 동시 실행 중인 Groq 호출이 maxConcurrentGroqCalls를
// 넘지 않도록 세마포어 슬롯을 확보하고, 슬롯을 얻은 뒤에도 직전 호출
// 시작 이후 groqCallStagger가 지나지 않았으면 그만큼 추가로 대기한다.
// ctx가 먼저 취소되면 대기를 포기하고 즉시 에러를 반환한다(세마포어
// 슬롯을 확보한 뒤 취소된 경우 슬롯도 반납한다). 반환된 release 함수는
// 성공 시 반드시 defer로 호출해야 한다.
func acquireGroqCallSlot(ctx context.Context) (release func(), err error) {
	select {
	case groqCallGate.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// 대기할 "내 순번" 시각을 락 안에서 원자적으로 예약한다 — 락 밖에서
	// "직전 시작 시각과의 간격을 확인한 뒤 따로 sleep하고 그 다음에
	// lastStart를 갱신"하는 방식은, 두 goroutine이 lastStart를 갱신하기
	// 전에 동시에 간격을 확인해버리면 결국 둘 다 거의 같은 시각에
	// 시작해버려 스태거링이 무력화되는 경쟁 상태가 생긴다. 예약 시각을
	// 먼저 확정하고 lastStart를 그 값으로 즉시 전진시켜 두면, 그 이후
	// 도착하는 goroutine은 항상 이미 예약된 시각 다음 슬롯부터 배정받는다.
	groqCallGate.mu.Lock()
	reserved := groqCallGate.lastStart.Add(groqCallStagger)
	if now := time.Now(); reserved.Before(now) {
		reserved = now
	}
	groqCallGate.lastStart = reserved
	groqCallGate.mu.Unlock()

	if wait := time.Until(reserved); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			<-groqCallGate.sem
			return nil, ctx.Err()
		}
	}

	return func() { <-groqCallGate.sem }, nil
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
//
// frequencyPenalty(0이면 미전송)는 그 반복 루프 자체(토큰 한도까지 채우는
// 것과는 별개로, completionTokens가 한도에 한참 못 미치는데도 같은 구절이
// 응답 안에서 그대로 재등장하는 현상)를 디코딩 단계에서 직접 억제한다 —
// 이미 나온 토큰의 등장 빈도에 비례해 다음 생성 시 그 토큰의 확률을
// 낮추므로, "60.42%의 지분을 보유한 60.42%의 지분을 보유한"처럼 같은
// 구절이 그대로 되풀이되는 경향을 프롬프트 수정 없이도 줄인다. 그래도
// 완전히 없앨 수는 없으므로 briefing.go의 findRepeatedPhrase가 여전히
// 최종 방어선으로 남아 있다. 반복이 보고된 적 없는 호출부(로또 인사이트,
// 뉴스 헤드라인 번역)는 0을 넘겨 기존 동작을 그대로 유지한다.
func callGroqChat(ctx context.Context, apiKey, model string, messages []groqChatMessage, temperature float64, maxTokens int, frequencyPenalty float64, jsonMode bool) (string, error) {
	reqBody := groqChatRequest{
		Model:            model,
		Messages:         messages,
		Temperature:      temperature,
		MaxTokens:        maxTokens,
		FrequencyPenalty: frequencyPenalty,
	}
	if jsonMode {
		reqBody.ResponseFormat = &groqResponseFormat{Type: "json_object"}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	// 여러 Groq 호출(브리핑 3섹션 + 뉴스 번역)이 거의 동시에 시작되면 서로
	// TPM 예산을 두고 경쟁해 rate limit이 발생하기 쉬워진다.
	// acquireGroqCallSlot이 동시 실행 개수를 제한하고 호출 시작 시각도
	// 최소 간격만큼 벌려서, 애초에 짧은 시간에 몰리는 것 자체를 완화한다
	// — groqCallGate 문서 주석 참고. 재시도까지 포함한 이 함수 전체
	// 동안 슬롯을 쥐고 있는다: 대기 중인 다른 호출이 있다면, 지금 이미
	// rate limit에 걸려 백오프 중인 이 호출이 슬롯을 놓아줄 때까지
	// 기다리는 편이 함께 더 많은 호출을 새로 쏴서 상황을 악화시키는
	// 것보다 낫다.
	release, err := acquireGroqCallSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	// rate_limit 대기 재시도: 이는 generateSectionText/fetchNewsTranslation의
	// "검증 실패 시 모델 승격" 재시도와는 완전히 별개의 로직이다 — 여기서는
	// 같은 모델로 같은 요청을 아주 짧게 기다렸다가 다시 보낼 뿐이며,
	// 모델을 바꾸지 않는다. 매 시도가 실패할 때마다 그 시점의 최신 에러
	// 메시지에서 "Please try again in {N}s" 형태의 대기 시간을 다시
	// 파싱한다 — parseGroqRetryAfterSeconds 문서 주석 참고. 대기 시간을
	// 알 수 없거나(에러 형식이 다름) 한 번의 대기가 너무 길거나
	// (maxGroqRateLimitRetryWait 초과), 누적 대기가 전체 예산
	// (maxGroqRateLimitTotalWait)을 넘을 것으로 예상되거나, 재시도
	// 횟수(maxGroqRateLimitRetries)를 모두 소진하면 그 시점에서 바로
	// 에러를 반환해 상위 호출부가 stale_fallback으로 넘어가게 한다 —
	// 사용자를 오래 기다리게 하지 않기 위해서다.
	var totalWait time.Duration
	for attempt := 0; ; attempt++ {
		content, callErr := doGroqChatRequest(ctx, apiKey, model, bodyBytes, temperature, maxTokens)
		if callErr == nil {
			return content, nil
		}

		if attempt >= maxGroqRateLimitRetries {
			if attempt > 0 {
				log.Printf("[Groq 호출] rate limit 재시도 %d회 모두 소진(model=%s, 누적 대기 %.1fs): %v", attempt, model, totalWait.Seconds(), callErr)
			}
			return "", callErr
		}

		wait, parsed := parseGroqRetryAfterSeconds(callErr.Error())
		if !parsed || wait > maxGroqRateLimitRetryWait {
			return "", callErr
		}
		wait += groqRateLimitRetryBuffer

		if totalWait+wait > maxGroqRateLimitTotalWait {
			log.Printf("[Groq 호출] rate limit 재시도 총 대기 예산(%s) 초과 예상(model=%s, 이미 %.1fs 대기 + 추가 %.1fs 필요) — 재시도 중단: %v",
				maxGroqRateLimitTotalWait, model, totalWait.Seconds(), wait.Seconds(), callErr)
			return "", callErr
		}

		// ctx의 남은 예산을 확인하지 않고 무조건 기다리면, 대기 시간이
		// 섹션 전체 타임아웃에 육박하거나 넘어서는 경우(실제 사례: 대기
		// 8.18초 vs 섹션 예산 8초) 기다리는 도중 ctx가 만료돼 재시도
		// 자체가 시도되지도 못한 채 "context deadline exceeded"로
		// 실패한다. 기다렸다가 실패하는 것보다, 애초에 예산이 부족하면
		// 즉시 실패를 반환해 상위 호출부(resolveBriefingSection)가 더
		// 빨리 stale_fallback으로 넘어가게 하는 편이 사용자 응답 속도에
		// 낫다. 대기 시간뿐 아니라 대기 후 실제 호출에 걸릴 예상 시간
		// (groqRateLimitRetryCallOverhead)까지 더해 예산 안에 들어오는지
		// 확인한다 — 대기만 겨우 맞고 호출할 시간이 없으면 결국 같은
		// 실패로 이어지기 때문이다. ctx에 데드라인이 없으면(예: 테스트가
		// context.Background()를 그대로 쓰는 경우) 이 검사를 건너뛴다.
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			needed := wait + groqRateLimitRetryCallOverhead
			budgetLimit := time.Duration(float64(remaining) * groqRateLimitRetryBudgetRatio)
			if remaining <= 0 || needed >= remaining || wait >= budgetLimit {
				log.Printf("[Groq 호출] rate limit 재시도 포기(model=%s) — 대기 시간(%.1fs)+예상 호출 시간(%.1fs)이 남은 ctx 예산(%.1fs) 대비 너무 커서, 기다리다 실패하는 대신 즉시 폴백: %v",
					model, wait.Seconds(), groqRateLimitRetryCallOverhead.Seconds(), remaining.Seconds(), callErr)
				return "", callErr
			}
		}
		totalWait += wait

		log.Printf("[Groq 호출] rate limit 감지(model=%s, 시도 %d/%d), %.1fs 대기 후 재시도(누적 대기 %.1fs): %v",
			model, attempt+1, maxGroqRateLimitRetries+1, wait.Seconds(), totalWait.Seconds(), callErr)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
}

// doGroqChatRequest는 이미 직렬화된 요청 바디로 Groq에 HTTP POST 요청 1회를
// 보내고 응답을 파싱한다 — callGroqChat이 최초 시도와 rate_limit 재시도
// 양쪽에서 재사용하는 실제 전송 로직이다.
func doGroqChatRequest(ctx context.Context, apiKey, model string, bodyBytes []byte, temperature float64, maxTokens int) (string, error) {
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
