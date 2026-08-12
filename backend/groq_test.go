package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestFrequentAndEscalationModelsAreDistinctByDefault(t *testing.T) {
	os.Unsetenv("GROQ_MODEL")
	os.Unsetenv("GROQ_ESCALATION_MODEL")

	frequent := frequentGroqModel()
	escalation := escalationGroqModel()

	if frequent == escalation {
		t.Fatalf("expected frequentGroqModel and escalationGroqModel to differ, both returned %q", frequent)
	}
	if frequent != "llama-3.1-8b-instant" {
		t.Errorf("frequentGroqModel() = %q, want the cheap/high-quota 8B model", frequent)
	}
	if escalation != "llama-3.3-70b-versatile" {
		t.Errorf("escalationGroqModel() = %q, want the higher-accuracy 70B model", escalation)
	}
}

// resetGroqUsageForTest는 이 파일 밖으로 groqUsage의 내부 상태를 노출할
// 필요 없이, 이전에(혹은 동시에) 어떤 테스트가 실행됐든 각 테스트가
// 깨끗한 상태에서 시작하도록 초기화한다.
func resetGroqUsageForTest() {
	groqUsage.mu.Lock()
	defer groqUsage.mu.Unlock()
	groqUsage.day = ""
	groqUsage.callsByModel = make(map[string]int)
	groqUsage.cacheHits = 0
	groqUsage.remainingReqs = ""
	groqUsage.remainingToks = ""
	groqUsage.lastHeaderTime = time.Time{}
}

func TestRecordGroqCallCountsPerModel(t *testing.T) {
	resetGroqUsageForTest()

	recordGroqCall("llama-3.1-8b-instant")
	recordGroqCall("llama-3.1-8b-instant")
	recordGroqCall("llama-3.3-70b-versatile")

	snapshot := getGroqUsageSnapshot()
	if snapshot.CallsByModel["llama-3.1-8b-instant"] != 2 {
		t.Errorf("8b calls = %d, want 2", snapshot.CallsByModel["llama-3.1-8b-instant"])
	}
	if snapshot.CallsByModel["llama-3.3-70b-versatile"] != 1 {
		t.Errorf("70b calls = %d, want 1", snapshot.CallsByModel["llama-3.3-70b-versatile"])
	}
	if snapshot.TotalCalls != 3 {
		t.Errorf("TotalCalls = %d, want 3", snapshot.TotalCalls)
	}
}

func TestRecordGroqCacheHitIsCountedSeparatelyFromCalls(t *testing.T) {
	resetGroqUsageForTest()

	recordGroqCall("llama-3.1-8b-instant")
	recordGroqCacheHit()
	recordGroqCacheHit()

	snapshot := getGroqUsageSnapshot()
	if snapshot.CacheHits != 2 {
		t.Errorf("CacheHits = %d, want 2", snapshot.CacheHits)
	}
	if snapshot.TotalCalls != 1 {
		t.Errorf("TotalCalls = %d, want 1 (cache hits must not inflate the real-call count)", snapshot.TotalCalls)
	}
}

func TestRecordGroqRateLimitHeadersStoresRemainingValues(t *testing.T) {
	resetGroqUsageForTest()

	header := http.Header{}
	header.Set("x-ratelimit-remaining-requests", "14399")
	header.Set("x-ratelimit-remaining-tokens", "99000")
	recordGroqRateLimitHeaders(header)

	snapshot := getGroqUsageSnapshot()
	if snapshot.RemainingRequests != "14399" {
		t.Errorf("RemainingRequests = %q, want %q", snapshot.RemainingRequests, "14399")
	}
	if snapshot.RemainingTokens != "99000" {
		t.Errorf("RemainingTokens = %q, want %q", snapshot.RemainingTokens, "99000")
	}
	if snapshot.RateLimitHeaderAsOf == "" {
		t.Error("expected RateLimitHeaderAsOf to be set once a header has been recorded")
	}
}

func TestRecordGroqRateLimitHeadersIgnoresAbsentHeaders(t *testing.T) {
	resetGroqUsageForTest()

	recordGroqRateLimitHeaders(http.Header{})

	snapshot := getGroqUsageSnapshot()
	if snapshot.RemainingRequests != "" || snapshot.RemainingTokens != "" || snapshot.RateLimitHeaderAsOf != "" {
		t.Errorf("expected no rate-limit fields to be set from an empty header, got %+v", snapshot)
	}
}

func TestParseGroqRetryAfterSeconds(t *testing.T) {
	cases := []struct {
		name    string
		errMsg  string
		wantOK  bool
		wantSec float64
	}{
		{
			name:    "표준 Groq TPM 메시지",
			errMsg:  "groq api error: Rate limit reached for model `llama-3.1-8b-instant` in organization ... Limit 6000, Used 6042. Please try again in 1.234s.",
			wantOK:  true,
			wantSec: 1.234,
		},
		{
			name:    "소문자 please",
			errMsg:  "groq api error: rate limit reached, please try again in 0.5s",
			wantOK:  true,
			wantSec: 0.5,
		},
		{
			name:    "초과 폭이 커서 대기 시간이 긴 경우",
			errMsg:  "groq api error: Rate limit reached ... Please try again in 245.7s.",
			wantOK:  true,
			wantSec: 245.7,
		},
		{
			name:   "rate limit과 무관한 에러",
			errMsg: "groq api returned status 500",
			wantOK: false,
		},
		{
			name:   "빈 문자열",
			errMsg: "",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, ok := parseGroqRetryAfterSeconds(tc.errMsg)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (wait=%v)", ok, tc.wantOK, wait)
			}
			if !tc.wantOK {
				return
			}
			wantWait := time.Duration(tc.wantSec * float64(time.Second))
			if wait != wantWait {
				t.Errorf("wait = %v, want %v", wait, wantWait)
			}
		})
	}
}

// groqRateLimitErrorBody는 Groq의 429 rate-limit 응답 바디를 흉내낸다.
func groqRateLimitErrorBody(retryAfter string) string {
	return fmt.Sprintf(`{"error":{"message":"Rate limit reached for model. Please try again in %ss."}}`, retryAfter)
}

func groqSuccessBody(content string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q}}]}`, content)
}

// TestCallGroqChatRetriesAndRecoversOnSmallRateLimitExcess는 실제 문제
// 상황(초과 폭이 작은 TPM rate limit)을 재현한다: 첫 호출은 429 +
// "Please try again in 0.05s"를 반환하고, 두 번째(재시도) 호출은 성공
// 응답을 반환한다. callGroqChat이 대기 후 자동으로 재시도해서 정상
// 텍스트를 반환하는지 확인한다.
func TestCallGroqChatRetriesAndRecoversOnSmallRateLimitExcess(t *testing.T) {
	resetGroqUsageForTest()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(groqRateLimitErrorBody("0.05")))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(groqSuccessBody("브리핑 텍스트")))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	start := time.Now()
	content, err := callGroqChat(context.Background(), "test-key", "llama-3.1-8b-instant",
		[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("callGroqChat() error = %v, want nil (should recover via retry)", err)
	}
	if content != "브리핑 텍스트" {
		t.Errorf("content = %q, want %q", content, "브리핑 텍스트")
	}
	if callCount != 2 {
		t.Errorf("server was called %d times, want 2 (initial + 1 retry)", callCount)
	}
	// 0.05s + 0.5s 버퍼만큼은 최소한 기다렸어야 한다.
	if elapsed < 500*time.Millisecond {
		t.Errorf("elapsed = %v, expected at least the retry-after + buffer wait", elapsed)
	}
}

// TestCallGroqChatDoesNotRetryWhenWaitTooLong은 초과 폭이 커서 대기 시간이
// 긴 경우(예: 30초) 무리하게 기다리지 않고 즉시 에러를 반환하는지
// 확인한다 — 상위 호출부(generateSectionText 등)는 이 에러를 받아 바로
// stale_fallback으로 넘어간다.
func TestCallGroqChatDoesNotRetryWhenWaitTooLong(t *testing.T) {
	resetGroqUsageForTest()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(groqRateLimitErrorBody("30")))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	start := time.Now()
	_, err := callGroqChat(context.Background(), "test-key", "llama-3.1-8b-instant",
		[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("callGroqChat() error = nil, want an error (30s wait exceeds the retry budget)")
	}
	if callCount != 1 {
		t.Errorf("server was called %d times, want 1 (no retry should be attempted)", callCount)
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed = %v, expected callGroqChat to fail fast without waiting 30s", elapsed)
	}
}

// TestCallGroqChatDoesNotRetryOnUnparseableRateLimitMessage는 에러 메시지
// 형식이 달라 대기 시간을 파싱할 수 없는 경우, 재시도 없이 바로 에러를
// 반환하는지 확인한다.
func TestCallGroqChatDoesNotRetryOnUnparseableRateLimitMessage(t *testing.T) {
	resetGroqUsageForTest()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit reached, contact support."}}`))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	_, err := callGroqChat(context.Background(), "test-key", "llama-3.1-8b-instant",
		[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)

	if err == nil {
		t.Fatal("callGroqChat() error = nil, want an error (retry-after could not be parsed)")
	}
	if callCount != 1 {
		t.Errorf("server was called %d times, want 1 (no retry should be attempted)", callCount)
	}
}

func TestGroqUsageRolloverClearsCountsOnNewDay(t *testing.T) {
	resetGroqUsageForTest()

	recordGroqCall("llama-3.1-8b-instant")
	recordGroqCacheHit()

	groqUsage.mu.Lock()
	groqUsage.day = "2000-01-01" // 날짜를 오래된 값으로 만들어 다음 기록에서 롤오버가 일어나게 함
	groqUsage.mu.Unlock()

	recordGroqCall("llama-3.1-8b-instant")

	snapshot := getGroqUsageSnapshot()
	if snapshot.TotalCalls != 1 {
		t.Errorf("TotalCalls after rollover = %d, want 1 (yesterday's call must not carry over)", snapshot.TotalCalls)
	}
	if snapshot.CacheHits != 0 {
		t.Errorf("CacheHits after rollover = %d, want 0", snapshot.CacheHits)
	}
}
