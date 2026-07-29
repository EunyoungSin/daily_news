package main

import (
	"context"
	"testing"
	"time"
)

// resetNewsDataIOUsageForTest는 이전에(혹은 동시에) 어떤 테스트가 실행됐든
// 상관없이 각 테스트가 깨끗한 상태에서 시작하도록 초기화한다.
func resetNewsDataIOUsageForTest() {
	newsDataIOUsage.mu.Lock()
	defer newsDataIOUsage.mu.Unlock()
	newsDataIOUsage.day = ""
	newsDataIOUsage.count = 0
}

func TestRecordNewsDataIOCallCounts(t *testing.T) {
	resetNewsDataIOUsageForTest()

	recordNewsDataIOCall()
	recordNewsDataIOCall()
	recordNewsDataIOCall()

	if got := newsDataIOUsageCount(); got != 3 {
		t.Errorf("newsDataIOUsageCount() = %d, want 3", got)
	}
}

func TestNewsDataIOUsageRolloverClearsCountOnNewDay(t *testing.T) {
	resetNewsDataIOUsageForTest()

	recordNewsDataIOCall()
	recordNewsDataIOCall()

	newsDataIOUsage.mu.Lock()
	newsDataIOUsage.day = "2000-01-01" // 날짜를 오래된 값으로 만들어 다음 기록에서 롤오버가 일어나게 함
	newsDataIOUsage.mu.Unlock()

	recordNewsDataIOCall()

	if got := newsDataIOUsageCount(); got != 1 {
		t.Errorf("newsDataIOUsageCount() after rollover = %d, want 1 (yesterday's calls must not carry over)", got)
	}
}

// TestGetCachedOrFetchNewsReusesFreshEntry는 실제로 NewsData.io를 호출하는
// 대신 캐시에 직접 값을 심어서, 이 테스트가 네트워크 접근이나 API 키
// 설정 여부에 의존하지 않도록 한다.
func TestGetCachedOrFetchNewsReusesFreshEntry(t *testing.T) {
	resetNewsDataIOUsageForTest()
	key := newsFetchCacheKey("technology", "domestic")
	seeded := &NewsData{Category: "technology", Region: "domestic"}

	newsFetchCache.mu.Lock()
	newsFetchCache.items[key] = newsFetchCacheEntry{data: seeded, fetchedAt: time.Now()}
	newsFetchCache.mu.Unlock()
	t.Cleanup(func() {
		newsFetchCache.mu.Lock()
		delete(newsFetchCache.items, key)
		newsFetchCache.mu.Unlock()
	})

	data, notice, err := getCachedOrFetchNews(context.Background(), "technology", "domestic")
	if err != nil {
		t.Fatalf("expected the fresh cache entry to be served without error, got %v", err)
	}
	if data != seeded {
		t.Errorf("expected the exact cached *NewsData back, got a different value: %+v", data)
	}
	if notice != "" {
		t.Errorf("notice = %q, want empty (fresh cache hit, no quota concern)", notice)
	}
}

// TestGetCachedOrFetchNewsServesStaleCacheNearQuota는 세 번째 안전장치를
// 검증한다: 오늘 사용량이 newsDataIOQuotaThreshold 이상이 되면, 만료된
// (TTL 지난) 캐시 항목이라도 크레딧을 추가로 소비하는 대신 쿼터 안내
// 문구와 함께 그대로 제공되어야 한다. NEWSDATA_API_KEY는 일부러
// 환경에 있는 그대로 두었다(반드시 설정할 필요 없음): 만약 쿼터
// 가드가 fetchNewsDataIO 호출 전에 제대로 작동하지 않는다면, 키가
// 없을 때 에러가 표면화될 것이므로 이 테스트는 엉뚱한 이유로 조용히
// 통과하는 대신 확실하게 실패하게 된다.
func TestGetCachedOrFetchNewsServesStaleCacheNearQuota(t *testing.T) {
	resetNewsDataIOUsageForTest()
	key := newsFetchCacheKey("top", "domestic")
	stale := &NewsData{Category: "top", Region: "domestic"}

	newsFetchCache.mu.Lock()
	newsFetchCache.items[key] = newsFetchCacheEntry{data: stale, fetchedAt: time.Now().Add(-newsFetchCacheTTL - time.Minute)}
	newsFetchCache.mu.Unlock()
	t.Cleanup(func() {
		newsFetchCache.mu.Lock()
		delete(newsFetchCache.items, key)
		newsFetchCache.mu.Unlock()
	})

	newsDataIOUsage.mu.Lock()
	newsDataIOUsage.day = time.Now().In(kst).Format("2006-01-02")
	newsDataIOUsage.count = newsDataIOQuotaThreshold
	newsDataIOUsage.mu.Unlock()
	t.Cleanup(resetNewsDataIOUsageForTest)

	data, notice, err := getCachedOrFetchNews(context.Background(), "top", "domestic")
	if err != nil {
		t.Fatalf("expected quota-guard to serve the stale cache without error, got %v", err)
	}
	if data != stale {
		t.Errorf("expected the exact stale cached *NewsData back, got a different value: %+v", data)
	}
	if notice != newsDataIOQuotaNotice {
		t.Errorf("notice = %q, want %q", notice, newsDataIOQuotaNotice)
	}
}
