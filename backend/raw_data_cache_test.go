package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRawDataCacheNilDB는 DB가 설정되지 않은 상태로 앱이 실행될 때
// (다른 DB 캐시들과 마찬가지로 — TestBriefingSectionCacheNilDB 참고) 캐시
// 헬퍼가 네트워크를 건드리거나 패닉을 일으키지 않아야 함을 문서화한다.
func TestRawDataCacheNilDB(t *testing.T) {
	if _, found := lookupRawDataCache(context.Background(), nil, "weather:seoul"); found {
		t.Error("expected lookup against a nil db to report not-found")
	}
	if err := upsertRawDataCache(context.Background(), nil, "weather:seoul", "{}", time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Errorf("expected upsert against a nil db to no-op without error, got %v", err)
	}
}

func TestIsRawCacheFresh(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	fresh := rawDataCacheRow{expiresAt: now.Add(time.Minute)}
	if !isRawCacheFresh(fresh, now) {
		t.Error("expected a row expiring in the future to be fresh")
	}

	expired := rawDataCacheRow{expiresAt: now.Add(-time.Minute)}
	if isRawCacheFresh(expired, now) {
		t.Error("expected a row that already expired to be stale")
	}
}

// TestFetchWithRawCacheNilDBAlwaysCallsFetchFn은 conn이 nil이면(로컬 DB
// 없이 실행되는 환경) lookupRawDataCache가 항상 not-found를 반환하므로,
// fetchWithRawCache가 매번 fetchFn을 호출해 정상적으로 우아하게 저하되는지
// (패닉하거나 캐시 없이 죽지 않는지) 확인한다.
func TestFetchWithRawCacheNilDBAlwaysCallsFetchFn(t *testing.T) {
	calls := 0
	want := "fresh-value"

	for i := 0; i < 2; i++ {
		got, err := fetchWithRawCache(context.Background(), nil, "test:key", time.Minute, func(ctx context.Context) (*string, error) {
			calls++
			v := want
			return &v, nil
		})
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if got == nil || *got != want {
			t.Fatalf("call %d: got %v, want %q", i, got, want)
		}
	}

	if calls != 2 {
		t.Errorf("expected fetchFn to be called on every invocation with a nil db (no caching possible), got %d calls", calls)
	}
}

// TestFetchWithRawCachePropagatesFetchErrorWhenNoStaleCache는 nil db(따라서
// 대체할 캐시가 전혀 없음)일 때 fetchFn이 실패하면 그 오류가 그대로
// 전달되는지 확인한다 — 실제 만료 캐시로의 폴백은 진짜 DB가 필요해
// 라이브로 검증한다(raw_data_cache.go의 isRawCacheFresh 문서 주석 참고).
func TestFetchWithRawCachePropagatesFetchErrorWhenNoStaleCache(t *testing.T) {
	wantErr := errors.New("boom")
	_, err := fetchWithRawCache(context.Background(), nil, "test:key", time.Minute, func(ctx context.Context) (*string, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("expected the fetch error to propagate when there is no cache to fall back to, got %v", err)
	}
}
