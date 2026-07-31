package main

import (
	"context"
	"testing"
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
