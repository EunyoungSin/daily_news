package main

import (
	"context"
	"testing"
	"time"
)

// TestNewsTranslationCacheNilDB는 TestBriefingSectionCacheNilDB와 동일한
// nil-DB 안전성 보장을 검증한다 — DB가 설정되지 않은 상태로 앱이 실행될
// 때 캐시 헬퍼가 네트워크를 건드리거나 패닉을 일으키지 않아야 한다. 이
// 경우 뉴스 번역은 캐싱 없이 매 요청마다 그냥 다시 번역된다.
func TestNewsTranslationCacheNilDB(t *testing.T) {
	if _, found := lookupNewsTranslation(context.Background(), nil, "abc123"); found {
		t.Error("expected lookup against a nil db to report not-found")
	}
	// upsertNewsTranslation은 에러를 반환하지 않으므로(로그만 남김), 여기서
	// 확인할 수 있는 건 nil db에서 패닉하지 않는다는 것뿐이다.
	upsertNewsTranslation(nil, "abc123", "번역된 제목")
}

// TestTranslationFailureCooldown은 실패 쿨다운이 의도대로 동작하는지
// 확인한다: 실패를 기록하면 쿨다운 동안은 "최근 실패"로 보고되고, 쿨다운이
// 지나면(또는 성공으로 지워지면) 다시 시도 가능한 상태로 돌아와야 한다.
func TestTranslationFailureCooldown(t *testing.T) {
	articleID := "cooldown-test-article"
	t.Cleanup(func() { clearTranslationFailure(articleID) })

	if recentlyFailedTranslation(articleID) {
		t.Fatal("실패를 기록하기 전에는 최근 실패로 보고되면 안 된다")
	}

	recordTranslationFailure(articleID)
	if !recentlyFailedTranslation(articleID) {
		t.Fatal("실패를 기록한 직후에는 쿨다운 중이라고 보고해야 한다")
	}

	// 쿨다운 만료를 시뮬레이션: 실패 시각을 쿨다운보다 더 과거로 되돌린다.
	newsTranslationFailuresMu.Lock()
	newsTranslationFailures[articleID] = time.Now().Add(-newsTranslationFailureCooldown - time.Second)
	newsTranslationFailuresMu.Unlock()

	if recentlyFailedTranslation(articleID) {
		t.Fatal("쿨다운이 지난 뒤에는 재시도를 허용해야 한다")
	}

	recordTranslationFailure(articleID)
	clearTranslationFailure(articleID)
	if recentlyFailedTranslation(articleID) {
		t.Fatal("성공(clearTranslationFailure)한 뒤에는 쿨다운이 남아있으면 안 된다")
	}
}
