package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
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

// resetGroqCallGateForTest는 groqCallGate/groqCallStagger를 테스트가 원하는
// 값으로 재설정한다. 프로덕션 기본값(동시 2개 + 250ms 스태거)을 그대로
// 두면 재시도 타이밍만 검증하는 대부분의 테스트가 그 스태거 대기 때문에
// 느려지거나, 이전 테스트가 남긴 lastStart 때문에 예측 불가능한 추가
// 대기가 섞여 들어갈 수 있다 — 동시성/스태거 자체를 검증하는 테스트를
// 제외하면 보통 충분히 큰 동시성 한도(예: 8)와 스태거 0을 넘겨 이
// 로직을 사실상 무력화한 채로 재시도 로직만 독립적으로 검증한다.
func resetGroqCallGateForTest(maxConcurrent int, stagger time.Duration) {
	groqCallGate = newGroqCallGate(maxConcurrent)
	groqCallStagger = stagger
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
	resetGroqCallGateForTest(8, 0)

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
	resetGroqCallGateForTest(8, 0)

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
	resetGroqCallGateForTest(8, 0)

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

// TestCallGroqChatRetriesMultipleTimesUsingFreshWaitEachAttempt은 여러
// Groq 호출이 TPM 예산을 두고 경쟁해 1회 재시도만으로는 복구되지 않는
// 상황을 재현한다: 1차 실패는 "2초 뒤 재시도"를, 2차 실패는 "0.05초 뒤
// 재시도"를 안내하고, 3차 시도에서 성공한다. 매 재시도마다 그 시점의
// 최신 에러에서 대기 시간을 다시 파싱해야 하므로(1차 대기 2초를
// 재사용하지 않아야 하므로), 총 소요 시간이 (2+0.5)+(0.05+0.5)=3.05초
// 근처여야 한다 — 만약 첫 대기 시간(2초)을 계속 재사용하는 버그가 있다면
// 총 소요 시간이 그보다 훨씬 길어진다(약 5.5초 이상).
func TestCallGroqChatRetriesMultipleTimesUsingFreshWaitEachAttempt(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 0)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(groqRateLimitErrorBody("2")))
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(groqRateLimitErrorBody("0.05")))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(groqSuccessBody("성공")))
		}
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
		t.Fatalf("callGroqChat() error = %v, want nil (should recover after 2 retries)", err)
	}
	if content != "성공" {
		t.Errorf("content = %q, want %q", content, "성공")
	}
	if callCount != 3 {
		t.Errorf("server was called %d times, want 3 (initial + 2 retries)", callCount)
	}
	if elapsed < 2500*time.Millisecond {
		t.Errorf("elapsed = %v, too short — expected at least ~3.05s of waiting across both retries", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Errorf("elapsed = %v, too long — suggests the stale first wait (2s) was reused instead of re-parsing each attempt's latest error", elapsed)
	}
}

// TestCallGroqChatExhaustsConfiguredRetryCount은 매번 짧은(0.01초) 대기를
// 안내하는데도 계속 rate limit이 걸리는 상황에서, maxGroqRateLimitRetries
// 재시도를 모두 소진한 뒤(총 시도 = 재시도 횟수 + 1) 재시도를 멈추고
// 에러를 반환하는지 확인한다.
func TestCallGroqChatExhaustsConfiguredRetryCount(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 0)

	originalRetries := maxGroqRateLimitRetries
	originalBuffer := groqRateLimitRetryBuffer
	maxGroqRateLimitRetries = 3
	groqRateLimitRetryBuffer = 10 * time.Millisecond // 테스트를 빠르게 유지
	defer func() {
		maxGroqRateLimitRetries = originalRetries
		groqRateLimitRetryBuffer = originalBuffer
	}()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(groqRateLimitErrorBody("0.01")))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	_, err := callGroqChat(context.Background(), "test-key", "llama-3.1-8b-instant",
		[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)

	if err == nil {
		t.Fatal("callGroqChat() error = nil, want an error (rate limit persists past all retries)")
	}
	if callCount != maxGroqRateLimitRetries+1 {
		t.Errorf("server was called %d times, want %d (initial + %d retries)", callCount, maxGroqRateLimitRetries+1, maxGroqRateLimitRetries)
	}
}

// TestCallGroqChatStopsRetryingWhenTotalWaitBudgetExceeded은 개별 대기
// 시간은 maxGroqRateLimitRetryWait 이하라도, 누적 대기가
// maxGroqRateLimitTotalWait를 넘을 것으로 예상되면 그 시점에서 더 이상
// 기다리지 않고 즉시 실패를 반환하는지 확인한다. 총 예산을 1.5초로
// 줄이고, 1차 실패는 0.5초(+0.5초 버퍼=1.0초, 누적 1.0초 — 예산 이내라
// 대기함) 대기를 안내하고 2차 실패는 0.9초(+0.5초 버퍼=1.4초, 누적
// 2.4초 — 예산 초과)를 안내한다. 따라서 첫 재시도는 실제로 대기하며
// 서버를 2번째로 호출하지만, 그 2번째 응답이 다시 실패하면 예산 초과가
// 감지되어 3번째 호출 없이 바로 에러를 반환해야 한다.
func TestCallGroqChatStopsRetryingWhenTotalWaitBudgetExceeded(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 0)

	originalTotalWait := maxGroqRateLimitTotalWait
	maxGroqRateLimitTotalWait = 1500 * time.Millisecond
	defer func() { maxGroqRateLimitTotalWait = originalTotalWait }()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusTooManyRequests)
		if callCount == 1 {
			w.Write([]byte(groqRateLimitErrorBody("0.5")))
		} else {
			w.Write([]byte(groqRateLimitErrorBody("0.9")))
		}
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
		t.Fatal("callGroqChat() error = nil, want an error (cumulative wait should exceed the 1.5s budget)")
	}
	if callCount != 2 {
		t.Errorf("server was called %d times, want 2 (initial + 1 retry, then stopped before a 2nd retry due to the total wait budget)", callCount)
	}
	if elapsed > 1300*time.Millisecond {
		t.Errorf("elapsed = %v, want ~1.0s (only the first retry's wait) — a 2nd wait would have pushed this past 1.5s+", elapsed)
	}
}

// TestGroqCallGateLimitsConcurrentCalls는 여러 Groq 호출이 동시에 시작돼도
// 실제로 서버에 동시 도달하는 요청 수가 maxConcurrentGroqCalls를 넘지
// 않는지 확인한다. 스태거는 0으로 꺼서 동시성 제한만 독립적으로
// 검증한다.
func TestGroqCallGateLimitsConcurrentCalls(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(2, 0)

	var mu sync.Mutex
	current, maxObserved := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current++
		if current > maxObserved {
			maxObserved = current
		}
		mu.Unlock()

		time.Sleep(100 * time.Millisecond) // 여러 호출이 실제로 겹칠 시간을 벌어준다

		mu.Lock()
		current--
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(groqSuccessBody("ok")))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = callGroqChat(context.Background(), "test-key", "llama-3.1-8b-instant",
				[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxObserved > 2 {
		t.Errorf("maxObserved concurrent Groq calls = %d, want <= 2 (maxConcurrentGroqCalls)", maxObserved)
	}
	if maxObserved < 2 {
		t.Errorf("maxObserved concurrent Groq calls = %d, want exactly 2 at some point (the gate should allow up to the limit, not serialize completely)", maxObserved)
	}
}

// TestGroqCallGateStaggersCallStartTimes는 동시성 한도를 넉넉하게 풀어둔
// 채로(8), 실제 호출 시작 시각들이 groqCallStagger 이상 벌어지는지
// 확인한다.
func TestGroqCallGateStaggersCallStartTimes(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 100*time.Millisecond)

	var mu sync.Mutex
	var starts []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(groqSuccessBody("ok")))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = callGroqChat(context.Background(), "test-key", "llama-3.1-8b-instant",
				[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 {
		t.Fatalf("got %d recorded call starts, want 3", len(starts))
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	for i := 1; i < len(starts); i++ {
		gap := starts[i].Sub(starts[i-1])
		if gap < 90*time.Millisecond {
			t.Errorf("gap between call starts %d and %d = %v, want >= ~100ms (groqCallStagger)", i-1, i, gap)
		}
	}
}

// TestGroqCallGateOverheadIsNegligibleForNormalTraffic는 rate limit이 전혀
// 걸리지 않는 정상 상황에서, 여러 Groq 호출이 동시에 몰려도 세마포어+
// 스태거로 인한 추가 지연이 사용자가 체감하기 어려운 수준인지 확인한다.
// 프로덕션 기본값(동시 2개, 250ms 스태거)으로 브리핑 3섹션에 해당하는
// 3개 호출을 동시에 실행하면, 2개는 즉시 시작하고 나머지 1개만 최대
// 1회의 스태거 간격만큼 지연되므로 총 소요 시간은 넉넉히 잡아도
// 1초를 넘지 않아야 한다(순차 처리였다면 3배의 응답 시간이 걸렸을
// 것이다).
func TestGroqCallGateOverheadIsNegligibleForNormalTraffic(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(maxConcurrentGroqCalls, 250*time.Millisecond) // 프로덕션 기본값

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(groqSuccessBody("ok")))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := callGroqChat(context.Background(), "test-key", "llama-3.1-8b-instant",
				[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)
			errs[i] = err
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: unexpected error %v", i, err)
		}
	}
	if elapsed > 1*time.Second {
		t.Errorf("elapsed = %v for 3 concurrent non-rate-limited calls, want well under 1s (gate overhead should be imperceptible)", elapsed)
	}
}

// TestConcurrentGroqCallsRecoverUnderSharedTPMPressure는 이번 개선의 핵심
// 시나리오를 재현한다: 여러 Groq 호출(브리핑 3섹션 + 뉴스 번역에 해당하는
// 4개)이 짧은 시간 안에 몰려 하나의 공유된 TPM(분당 토큰) 예산을 두고
// 경쟁하는 상황. fakeTPMBudget이 실제 Groq처럼 "이 요청을 받아들이면
// 예산을 초과하니 몇 초 뒤 재시도하라"는 429를 돌려주는 서버를 흉내낸다.
// 동시성 제한(스태거 포함)이 애초에 몰리는 정도를 완화하고, 그래도 걸린
// rate limit은 다회 재시도가 매번 최신 대기시간을 반영해 복구하므로,
// 4개 호출 모두 결국 성공해야 한다.
type fakeTPMBudget struct {
	mu          sync.Mutex
	windowStart time.Time
	used        int
	limit       int
	windowLen   time.Duration
}

func (b *fakeTPMBudget) tryConsume(cost int) (ok bool, retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= b.windowLen {
		b.windowStart = now
		b.used = 0
	}
	if b.used+cost > b.limit {
		remaining := b.windowLen - now.Sub(b.windowStart)
		if remaining < 0 {
			remaining = 0
		}
		return false, remaining
	}
	b.used += cost
	return true, 0
}

func TestConcurrentGroqCallsRecoverUnderSharedTPMPressure(t *testing.T) {
	resetGroqUsageForTest()
	// 프로덕션과 같은 비율(동시 2개, 스태거 존재)을 유지하되 절대값만
	// 테스트가 빠르게 끝나도록 축소한다.
	resetGroqCallGateForTest(2, 50*time.Millisecond)

	originalRetries := maxGroqRateLimitRetries
	originalRetryWait := maxGroqRateLimitRetryWait
	originalTotalWait := maxGroqRateLimitTotalWait
	originalBuffer := groqRateLimitRetryBuffer
	maxGroqRateLimitRetries = 4
	maxGroqRateLimitRetryWait = 1 * time.Second
	maxGroqRateLimitTotalWait = 3 * time.Second
	groqRateLimitRetryBuffer = 20 * time.Millisecond
	defer func() {
		maxGroqRateLimitRetries = originalRetries
		maxGroqRateLimitRetryWait = originalRetryWait
		maxGroqRateLimitTotalWait = originalTotalWait
		groqRateLimitRetryBuffer = originalBuffer
	}()

	// 한 창(window)에 요청 1개(비용 40)만 허용하는 빠듯한 예산 — 4개
	// 호출이 거의 동시에 도착하면 최소 3개는 최초 시도에서 rate limit에
	// 걸려야 정상이다.
	budget := &fakeTPMBudget{limit: 40, windowLen: 150 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, retryAfter := budget.tryConsume(40); !ok {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(groqRateLimitErrorBody(fmt.Sprintf("%.2f", retryAfter.Seconds()))))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(groqSuccessBody("ok")))
	}))
	defer server.Close()

	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	const callers = 4 // weather + exchange + news 브리핑 3개 + 뉴스 번역 1개에 대응
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := callGroqChat(ctx, "test-key", "llama-3.1-8b-instant",
				[]groqChatMessage{{Role: "user", Content: "hi"}}, 0.3, 100, 0, false)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: expected eventual success after retries, got error: %v", i, err)
		}
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
