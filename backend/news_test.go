package main

import (
	"testing"
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

// TestNewsFetchCacheKeyIncludesPrefix는 raw_data_cache가 날씨/환율/뉴스를
// 테이블 하나에서 공유하므로, 캐시 키에 "news:" 접두사가 항상 붙어서 다른
// 데이터 종류와 절대 섞이지 않는지 확인한다 — 캐시 히트/미스 자체(DB
// 연동)는 이 프로젝트의 다른 DB 캐시들과 마찬가지로 실제 서버로 라이브
// 검증한다(raw_data_cache.go의 isRawCacheFresh 문서 주석 참고).
func TestNewsFetchCacheKeyIncludesPrefix(t *testing.T) {
	if got, want := newsFetchCacheKey("top", "domestic"), "news:domestic:top"; got != want {
		t.Errorf("newsFetchCacheKey(top, domestic) = %q, want %q", got, want)
	}
}

// TestNewsShouldServeStaleForQuota는 세 번째 안전장치의 판단 로직을
// 검증한다: 오늘 사용량이 newsDataIOQuotaThreshold 이상이면(그리고 되돌아갈
// 캐시가 있으면) 만료된 캐시라도 크레딧을 추가로 소비하는 대신 그대로
// 서빙해야 한다고 판단해야 한다.
func TestNewsShouldServeStaleForQuota(t *testing.T) {
	if newsShouldServeStaleForQuota(false, newsDataIOQuotaThreshold) {
		t.Error("should not serve stale when there is no cached entry to fall back to")
	}
	if newsShouldServeStaleForQuota(true, newsDataIOQuotaThreshold-1) {
		t.Error("should not serve stale before reaching the quota threshold")
	}
	if !newsShouldServeStaleForQuota(true, newsDataIOQuotaThreshold) {
		t.Error("should serve stale once the quota threshold is reached")
	}
}
