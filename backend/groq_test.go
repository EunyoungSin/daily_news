package main

import (
	"net/http"
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
