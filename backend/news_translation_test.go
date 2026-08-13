package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestTranslationFailureCooldownVariesByReason은 이번 기능의 핵심을
// 검증한다: rate_limit으로 기록된 실패는 짧은 쿨다운을, validation_failed로
// 기록된 실패는 기존처럼 긴(5분) 쿨다운을 받아야 하고, 두 경우 모두
// news_translation_cache에 사유/재시도 시각이 실제로 저장되어야 한다.
func TestTranslationFailureCooldownVariesByReason(t *testing.T) {
	conn := openTempBriefingTestDB(t)
	ctx := context.Background()

	rateLimitID := "rate-limit-article"
	validationID := "validation-failed-article"

	if recentlyFailedNewsTranslation(ctx, conn, rateLimitID) {
		t.Fatal("실패를 기록하기 전에는 최근 실패로 보고되면 안 된다")
	}

	recordNewsTranslationFailure(conn, rateLimitID, newsTranslationFailureReasonRateLimit)
	recordNewsTranslationFailure(conn, validationID, newsTranslationFailureReasonValidationFailed)

	if !recentlyFailedNewsTranslation(ctx, conn, rateLimitID) {
		t.Fatal("rate_limit 실패를 기록한 직후에는 쿨다운 중이라고 보고해야 한다")
	}
	if !recentlyFailedNewsTranslation(ctx, conn, validationID) {
		t.Fatal("validation_failed 실패를 기록한 직후에는 쿨다운 중이라고 보고해야 한다")
	}

	// rate_limit 쿨다운(45초)이 지난 시점을 시뮬레이션 — validation_failed
	// 쿨다운(5분)은 같은 경과 시간에도 여전히 유효해야 한다.
	pastRateLimitCooldown := time.Now().Add(-newsTranslationRateLimitCooldown - time.Second).Format(time.RFC3339)
	if _, err := conn.ExecContext(ctx,
		`UPDATE news_translation_cache SET retry_after = ? WHERE article_id = ?`,
		pastRateLimitCooldown, rateLimitID,
	); err != nil {
		t.Fatalf("rate_limit retry_after 조작 실패: %v", err)
	}

	if recentlyFailedNewsTranslation(ctx, conn, rateLimitID) {
		t.Fatal("rate_limit 쿨다운(45초)이 지난 뒤에는 재시도를 허용해야 한다")
	}
	if !recentlyFailedNewsTranslation(ctx, conn, validationID) {
		t.Fatal("validation_failed 쿨다운(5분)은 45초만 지난 시점에는 여전히 유효해야 한다")
	}

	// 저장된 failure_reason 자체도 그대로 조회 가능해야 한다.
	var reason string
	if err := conn.QueryRowContext(ctx,
		`SELECT failure_reason FROM news_translation_cache WHERE article_id = ?`, validationID,
	).Scan(&reason); err != nil {
		t.Fatalf("failure_reason 조회 실패: %v", err)
	}
	if reason != newsTranslationFailureReasonValidationFailed {
		t.Errorf("failure_reason = %q, want %q", reason, newsTranslationFailureReasonValidationFailed)
	}

	// 성공(upsertNewsTranslation)하면 실패 기록이 지워져야 한다.
	upsertNewsTranslation(conn, validationID, "번역된 제목")
	if recentlyFailedNewsTranslation(ctx, conn, validationID) {
		t.Fatal("성공(upsertNewsTranslation)한 뒤에는 실패 쿨다운이 남아있으면 안 된다")
	}
	if cached, ok := lookupNewsTranslation(ctx, conn, validationID); !ok || cached != "번역된 제목" {
		t.Errorf("lookupNewsTranslation = (%q, %v), want (\"번역된 제목\", true)", cached, ok)
	}
}

// TestClassifyNewsTranslationFailureReason은 Groq rate-limit 에러 메시지가
// rate_limit으로, 그 외 일반 에러는 api_error로 분류되는지 확인한다 —
// 이 분류에 따라 쿨다운 길이가 완전히 달라지므로(45초 vs 5분) 잘못
// 분류되면 rate limit 실패가 5분이나 원문으로 방치되는 원래 버그가
// 그대로 재발한다.
func TestClassifyNewsTranslationFailureReason(t *testing.T) {
	rateLimitErr := errors.New("Rate limit reached for model llama... Please try again in 1.234s.")
	if got := classifyNewsTranslationFailureReason(rateLimitErr); got != newsTranslationFailureReasonRateLimit {
		t.Errorf("classifyNewsTranslationFailureReason(rate limit msg) = %q, want %q", got, newsTranslationFailureReasonRateLimit)
	}

	tpmErr := errors.New("Request too large for tokens per minute (TPM)")
	if got := classifyNewsTranslationFailureReason(tpmErr); got != newsTranslationFailureReasonRateLimit {
		t.Errorf("classifyNewsTranslationFailureReason(tpm msg) = %q, want %q", got, newsTranslationFailureReasonRateLimit)
	}

	genericErr := errors.New("unexpected status code 500")
	if got := classifyNewsTranslationFailureReason(genericErr); got != newsTranslationFailureReasonAPIError {
		t.Errorf("classifyNewsTranslationFailureReason(generic msg) = %q, want %q", got, newsTranslationFailureReasonAPIError)
	}
}

// TestTranslateNewsItemsRateLimitRecoversQuicklyAfterCooldown은 이번 기능이
// 고치려는 문제를 실제 translateNewsItems 경로로 재현한다: Groq가 rate
// limit(재시도 예산을 넘는 긴 대기)으로 실패하면 원문 폴백과 함께
// rate_limit 사유/짧은 쿨다운이 기록되고, 그 쿨다운이 지난 뒤 다시
// 요청하면(이번엔 Groq가 성공) 바로 재번역을 시도해서 성공해야 한다 —
// "그 다음 번역 요청 시점엔 rate limit이 풀려있을 가능성이 높으니
// 빠르게 재시도되도록" 확인사항에 해당한다.
func TestTranslateNewsItemsRateLimitRecoversQuicklyAfterCooldown(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 0)

	conn := openTempBriefingTestDB(t)
	originalDB := db
	db = conn
	t.Cleanup(func() { db = originalDB })

	originalKey := os.Getenv("GROQ_API_KEY")
	os.Setenv("GROQ_API_KEY", "test-key")
	t.Cleanup(func() { os.Setenv("GROQ_API_KEY", originalKey) })

	originalEndpoint := groqEndpoint
	t.Cleanup(func() { groqEndpoint = originalEndpoint })

	articleID := "rate-limited-article"

	// 1단계: rate limit(대기 30초 안내 — 재시도 예산 20초를 넘으므로
	// callGroqChat이 재시도 없이 바로 실패한다)으로 번역 요청 전체가
	// 실패하는 상황을 재현한다.
	rateLimitCalls := 0
	rateLimitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(groqRateLimitErrorBody("30")))
	}))
	defer rateLimitServer.Close()
	groqEndpoint = rateLimitServer.URL

	items := []NewsItem{{ID: articleID, Title: "Some headline"}}
	translateNewsItems(context.Background(), items)

	if items[0].TranslatedTitle != "" {
		t.Fatalf("rate limit 실패 후 TranslatedTitle = %q, want empty(원문 폴백)", items[0].TranslatedTitle)
	}
	if rateLimitCalls != 1 {
		t.Errorf("Groq 서버 호출 횟수 = %d, want 1", rateLimitCalls)
	}

	var reason string
	if err := conn.QueryRowContext(context.Background(),
		`SELECT failure_reason FROM news_translation_cache WHERE article_id = ?`, articleID,
	).Scan(&reason); err != nil {
		t.Fatalf("failure_reason 조회 실패: %v", err)
	}
	if reason != newsTranslationFailureReasonRateLimit {
		t.Fatalf("failure_reason = %q, want %q", reason, newsTranslationFailureReasonRateLimit)
	}
	if !recentlyFailedNewsTranslation(context.Background(), conn, articleID) {
		t.Fatal("rate limit 실패 직후에는 쿨다운 중이라고 보고해야 한다")
	}

	// 2단계: rate_limit 쿨다운(45초)이 지난 시점을 시뮬레이션하고, 이번엔
	// Groq가 성공 응답을 반환하도록 바꿔서 "쿨다운이 지나면 빠르게
	// 재시도해 성공하는지"를 확인한다.
	past := time.Now().Add(-newsTranslationRateLimitCooldown - time.Second).Format(time.RFC3339)
	if _, err := conn.ExecContext(context.Background(),
		`UPDATE news_translation_cache SET retry_after = ? WHERE article_id = ?`,
		past, articleID,
	); err != nil {
		t.Fatalf("retry_after 조작 실패: %v", err)
	}

	successCalls := 0
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(groqSuccessBody(`{"translations":[{"id":"rate-limited-article","translatedTitle":"번역된 제목"}]}`)))
	}))
	defer successServer.Close()
	groqEndpoint = successServer.URL

	items2 := []NewsItem{{ID: articleID, Title: "Some headline"}}
	translateNewsItems(context.Background(), items2)

	if items2[0].TranslatedTitle != "번역된 제목" {
		t.Fatalf("쿨다운 만료 후 재시도 결과 TranslatedTitle = %q, want %q", items2[0].TranslatedTitle, "번역된 제목")
	}
	if successCalls != 1 {
		t.Errorf("쿨다운 만료 후 Groq 호출 횟수 = %d, want 1(즉시 재시도)", successCalls)
	}
	if recentlyFailedNewsTranslation(context.Background(), conn, articleID) {
		t.Error("재번역에 성공한 뒤에는 쿨다운이 남아있으면 안 된다")
	}
}

// TestTranslateNewsItemsValidationFailureKeepsLongCooldown은 콘텐츠 검증
// 실패(한자 혼입)로 인한 실패는 rate_limit과 달리 여전히 긴(5분) 쿨다운을
// 받아야 함을 확인한다 — "콘텐츠 검증 문제로 실패했던 항목은 여전히 5분
// 정도는 원문으로 안정적으로 폴백되는지" 확인사항에 해당한다. 같은
// 콘텐츠를 당장 재시도해도 모델이 비슷하게 한자를 섞을 가능성이 있어,
// rate_limit처럼 빠르게 재시도하면 오히려 Groq 호출만 낭비하기 쉽다.
func TestTranslateNewsItemsValidationFailureKeepsLongCooldown(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 0)

	conn := openTempBriefingTestDB(t)
	originalDB := db
	db = conn
	t.Cleanup(func() { db = originalDB })

	originalKey := os.Getenv("GROQ_API_KEY")
	os.Setenv("GROQ_API_KEY", "test-key")
	t.Cleanup(func() { os.Setenv("GROQ_API_KEY", originalKey) })

	originalEndpoint := groqEndpoint
	t.Cleanup(func() { groqEndpoint = originalEndpoint })

	articleID := "hanja-mixed-article"

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		// 모델 승격 재시도까지 포함해 매번 한자(CJK)가 섞인 응답을
		// 반환해서, 검증이 끝내 통과하지 못하는 상황을 재현한다.
		w.Write([]byte(groqSuccessBody(`{"translations":[{"id":"hanja-mixed-article","translatedTitle":"腹圍 관련 소식"}]}`)))
	}))
	defer server.Close()
	groqEndpoint = server.URL

	items := []NewsItem{{ID: articleID, Title: "Some headline"}}
	translateNewsItems(context.Background(), items)

	if items[0].TranslatedTitle != "" {
		t.Fatalf("검증 실패 후 TranslatedTitle = %q, want empty(원문 폴백)", items[0].TranslatedTitle)
	}
	if callCount != maxNewsTranslationRetries+1 {
		t.Errorf("Groq 호출 횟수 = %d, want %d(최초 + 모델 승격 재시도)", callCount, maxNewsTranslationRetries+1)
	}

	var reason, retryAfterStr string
	if err := conn.QueryRowContext(context.Background(),
		`SELECT failure_reason, retry_after FROM news_translation_cache WHERE article_id = ?`, articleID,
	).Scan(&reason, &retryAfterStr); err != nil {
		t.Fatalf("failure_reason/retry_after 조회 실패: %v", err)
	}
	if reason != newsTranslationFailureReasonValidationFailed {
		t.Fatalf("failure_reason = %q, want %q", reason, newsTranslationFailureReasonValidationFailed)
	}
	retryAfter, err := time.Parse(time.RFC3339, retryAfterStr)
	if err != nil {
		t.Fatalf("retry_after 파싱 실패: %v", err)
	}
	if remaining := time.Until(retryAfter); remaining < 4*time.Minute {
		t.Errorf("validation_failed 쿨다운까지 남은 시간 = %s, want at least ~5분(rate_limit의 45초와 뚜렷이 구분되어야 함)", remaining)
	}
	if !recentlyFailedNewsTranslation(context.Background(), conn, articleID) {
		t.Error("검증 실패 직후에는 쿨다운 중이라고 보고해야 한다")
	}
}

// TestAnnotateNewsTranslationFailureReasons는 응답 직전 주석 단계
// (news_handler.go가 호출하는 annotateNewsTranslationFailureReasons)가
// 번역이 비어있는 항목에만, 그리고 실제로 실패 기록이 있는 항목에만
// TranslationFailureReason을 채우는지 확인한다 — 프론트엔드가 이 값으로
// 사유별 콘솔 로그를 남기므로, 성공한 항목이나 애초에 시도된 적 없는
// 항목까지 잘못 채워지면 로그가 오해를 부른다.
func TestAnnotateNewsTranslationFailureReasons(t *testing.T) {
	conn := openTempBriefingTestDB(t)
	ctx := context.Background()

	recordNewsTranslationFailure(conn, "rate-limited-id", newsTranslationFailureReasonRateLimit)
	upsertNewsTranslation(conn, "succeeded-id", "번역된 제목")

	items := []NewsItem{
		{ID: "rate-limited-id", Title: "Original headline"},
		{ID: "succeeded-id", Title: "Another headline", TranslatedTitle: "번역된 제목"},
		{ID: "never-attempted-id", Title: "Untouched headline"},
	}

	annotateNewsTranslationFailureReasons(ctx, conn, items)

	if items[0].TranslationFailureReason != newsTranslationFailureReasonRateLimit {
		t.Errorf("items[0].TranslationFailureReason = %q, want %q", items[0].TranslationFailureReason, newsTranslationFailureReasonRateLimit)
	}
	if items[1].TranslationFailureReason != "" {
		t.Errorf("items[1](성공한 항목).TranslationFailureReason = %q, want empty", items[1].TranslationFailureReason)
	}
	if items[2].TranslationFailureReason != "" {
		t.Errorf("items[2](시도된 적 없는 항목).TranslationFailureReason = %q, want empty", items[2].TranslationFailureReason)
	}
}

// TestAnnotateNewsTranslationFailureReasonsNilDB는 nil DB에서 패닉하지
// 않는지 확인한다(다른 nil-DB 안전성 테스트들과 같은 이유).
func TestAnnotateNewsTranslationFailureReasonsNilDB(t *testing.T) {
	items := []NewsItem{{ID: "abc", Title: "headline"}}
	annotateNewsTranslationFailureReasons(context.Background(), nil, items)
	if items[0].TranslationFailureReason != "" {
		t.Error("expected TranslationFailureReason to remain empty when db is nil")
	}
}

// TestNewsTranslationSystemPromptCoversTechnicalTermHanjaMixing은
// briefing.go의 TestNewsSectionSystemPromptCoversTechnicalTermHanjaMixing과
// 같은 이유로 존재한다: 이 번역 프롬프트는 newsSectionSystemPrompt와
// 상수를 공유하지 않는 완전히 독립된 문자열이라, 같은 종류의 규칙("belly
// size" → "배圍" 같은 전문 용어 한자 혼입 방지)을 한쪽에만 추가하고
// 다른 쪽에 반영하는 것을 잊어버리는 회귀가 있을 수 있다.
func TestNewsTranslationSystemPromptCoversTechnicalTermHanjaMixing(t *testing.T) {
	if !strings.Contains(newsTranslationSystemPrompt, "전문 용어") {
		t.Fatal("expected newsTranslationSystemPrompt to contain guidance about technical/professional terminology")
	}
	if !strings.Contains(newsTranslationSystemPrompt, "belly size") {
		t.Error("expected the concrete regressed example (\"belly size\") to remain in the prompt as a guiding example")
	}
	if !strings.Contains(newsTranslationSystemPrompt, "쉬운 말로 풀어") {
		t.Error("expected guidance to paraphrase into simpler wording when a term is hard to render in pure Hangul, not just a bare CJK prohibition")
	}
}
