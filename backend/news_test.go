package main

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

// TestCoalesceNewsFetchDeduplicatesConcurrentCalls는 이번에 고친 실제
// 버그를 재현한다: /api/news와 대시보드의 브리핑용 내부 조회가 거의 동시에
// 들어오면, 캐시가 비어 있는 순간에는 각자 독립적으로 NewsData.io를
// 호출해버렸다(20초 안에 같은 category+region으로 2번 호출됨). 같은 key로
// 동시에 여러 호출이 들어와도 fn은 딱 한 번만 실행되고, 나머지는 그 결과를
// 기다렸다가 공유해야 한다.
func TestCoalesceNewsFetchDeduplicatesConcurrentCalls(t *testing.T) {
	const callers = 10
	var calls int32
	release := make(chan struct{})

	fn := func() (*NewsData, string, error) {
		atomic.AddInt32(&calls, 1)
		<-release // 모든 호출자가 도착할 때까지 fn 안에 붙잡아둬서 경쟁을 강제로 만든다
		return &NewsData{Category: "top"}, "some-notice", nil
	}

	var wg sync.WaitGroup
	results := make([]*NewsData, callers)
	notices := make([]string, callers)
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer wg.Done()
			data, notice, err := coalesceNewsFetch("news:domestic:top", fn)
			if err != nil {
				t.Errorf("caller %d: unexpected error: %v", i, err)
			}
			results[i] = data
			notices[i] = notice
		}(i)
	}

	// fn이 시작되긴 했지만 아직 안 끝난 상태에서 나머지 호출자들이 전부
	// "이미 진행 중"인 코드 경로에 도달할 시간을 준다.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected fn to be called exactly once for %d concurrent callers with the same key, got %d calls", callers, got)
	}
	for i, data := range results {
		if data == nil || data.Category != "top" {
			t.Errorf("caller %d: expected the shared result, got %+v", i, data)
		}
		if notices[i] != "some-notice" {
			t.Errorf("caller %d: expected the shared notice, got %q", i, notices[i])
		}
	}
}

// TestCoalesceNewsFetchDoesNotDeduplicateDifferentKeys는
// coalesceNewsFetch가 key 단위로만 조율하는지 확인한다 — 서로 다른
// category/region 조합은 당연히 각자 독립적으로 조회돼야 한다.
func TestCoalesceNewsFetchDoesNotDeduplicateDifferentKeys(t *testing.T) {
	var calls int32
	fn := func() (*NewsData, string, error) {
		atomic.AddInt32(&calls, 1)
		return &NewsData{}, "", nil
	}

	if _, _, err := coalesceNewsFetch("news:domestic:top", fn); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coalesceNewsFetch("news:domestic:business", fn); err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected fn to be called once per distinct key, got %d calls", got)
	}
}

// TestCoalesceNewsFetchPropagatesErrorToAllWaiters는 첫 호출이 실패하면
// 대기 중이던 나머지 호출들도 같은 에러를 받는지 확인한다 — 대기자가
// 성공한 것처럼 zero-value를 받아서는 안 된다.
func TestCoalesceNewsFetchPropagatesErrorToAllWaiters(t *testing.T) {
	wantErr := errors.New("boom")
	release := make(chan struct{})
	fn := func() (*NewsData, string, error) {
		<-release
		return nil, "", wantErr
	}

	var wg sync.WaitGroup
	errs := make([]error, 3)
	wg.Add(3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			defer wg.Done()
			_, _, err := coalesceNewsFetch("news:international:science", fn)
			errs[i] = err
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, wantErr) {
			t.Errorf("caller %d: expected the shared error %v, got %v", i, wantErr, err)
		}
	}
}

// TestLogNewsDataIORateLimitHeadersLogsRealHeaderNames은 실측(curl로
// NewsData.io를 직접 호출)으로 확인한 실제 헤더 이름들을 회귀 테스트로
// 고정한다 — 문서화되어 있지 않아 이름이 바뀌면 조용히 로그가 비게 될
// 수 있으므로, 헤더 이름 자체가 맞는지 여기서 검증한다.
func TestLogNewsDataIORateLimitHeadersLogsRealHeaderNames(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	header := http.Header{}
	header.Set("X-RateLimit-Limit", "60")
	header.Set("X-RateLimit-Remaining", "59")
	header.Set("X-RateLimit-Reset", "1786427692")
	header.Set("X-API-Limit-Remaining", "194")
	header.Set("Retry-After", "900")

	logNewsDataIORateLimitHeaders(header)

	logged := buf.String()
	for _, want := range []string{"59", "60", "1786427692", "194", "900"} {
		if !strings.Contains(logged, want) {
			t.Errorf("expected log output to contain %q, got %q", want, logged)
		}
	}
}

// TestLogNewsDataIORateLimitHeadersSkipsWhenAbsent는 헤더가 전혀 없을 때
// (다른 요금제이거나 NewsData.io가 표기를 바꾼 경우) 패닉하거나 의미 없는
// 로그를 남기지 않아야 함을 확인한다.
func TestLogNewsDataIORateLimitHeadersSkipsWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	logNewsDataIORateLimitHeaders(http.Header{})

	if buf.Len() != 0 {
		t.Errorf("expected no log output when no rate-limit headers are present, got %q", buf.String())
	}
}
