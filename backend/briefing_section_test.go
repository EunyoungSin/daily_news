package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFindBannedPhrase(t *testing.T) {
	if _, found := findBannedPhrase("오늘 오후 2시경 비가 올 가능성이 있어 우산을 준비하세요."); found {
		t.Error("expected no banned phrase in a normal grounded sentence")
	}
	if phrase, found := findBannedPhrase("이 주제에 대해 다양한 논의가 진행 중입니다."); !found || phrase != "다양한 논의가 진행 중입니다" {
		t.Errorf("expected banned filler phrase detected, got phrase=%q found=%v", phrase, found)
	}
	if _, found := findBannedPhrase("정말 대박이네요 ㅋㅋ"); !found {
		t.Error("expected internet slang to be detected as banned")
	}
	if _, found := findBannedPhrase("이 기술이 핵심입니다"); found {
		t.Error("expected '핵심' not to be flagged as banned (substring of removed '핵' entry)")
	}
	if _, found := findBannedPhrase("저출산으로 핵가족이 늘고 있다"); found {
		t.Error("expected '핵가족' not to be flagged as banned")
	}
	if _, found := findBannedPhrase("핵무기 실험이 감지됐다"); found {
		t.Error("expected '핵무기' not to be flagged as banned")
	}
	if _, found := findBannedPhrase("결핵 환자 수가 늘었다"); found {
		t.Error("expected '결핵' not to be flagged as banned")
	}
	if phrase, found := findBannedPhrase("완전 핵꿀잼이었다"); !found || phrase != "핵꿀잼" {
		t.Errorf("expected the specific slang compound '핵꿀잼' to be detected, got phrase=%q found=%v", phrase, found)
	}
	if _, found := findBannedPhrase("이 매장은 헐값에 물건을 판다"); found {
		t.Error("expected '헐값' not to be flagged as banned (word-boundary check should protect it)")
	}
	if _, found := findBannedPhrase("만화 주인공 짱구는 못말려"); found {
		t.Error("expected '짱구' not to be flagged as banned (word-boundary check should protect it)")
	}
	if phrase, found := findBannedPhrase("헐 진짜 그런 일이 있었어?"); !found || phrase != "헐" {
		t.Errorf("expected standalone slang '헐' to be detected, got phrase=%q found=%v", phrase, found)
	}
}

func TestFindInformalSentenceEnding(t *testing.T) {
	// 실제 보고된 사례들 — 뉴스 원문의 기사체를 그대로 따라간 반말 종결.
	informalCases := []string{
		"김정관 산업장관이 이번 조치에 반대한다고 밝혔다.",
		"이는 최근 발표된 정책과 관련이 있다.",
		"엘프 뷰티가 실적 호조에 힘입어 목표주가를 상향 조정했다.",
		"이 사실은 매우 중요하다",
		"프로젝트가 예정대로 진행 중임",
		"자료 검토가 모두 완료함",
	}
	for _, s := range informalCases {
		if _, found := findInformalSentenceEnding(s); !found {
			t.Errorf("expected informal/기사체 ending to be detected in %q", s)
		}
	}

	// 정중한 합쇼체(존댓말) 문장은 절대 걸리지 않아야 한다 — 날씨/환율
	// 문단에서 실제로 쓰이는 문장 형태 포함.
	politeCases := []string{
		"오늘 오후 2시경 비가 올 가능성이 있어 우산을 준비하세요.",
		"서울은 오늘 대체로 맑아 우산 없이 외출하기 좋은 날씨입니다. 오전 8시엔 18도, 오후 2시엔 23도이며 맑은 하늘이 이어집니다.",
		"환율은 1 USD당 1320.55 KRW입니다. 지난 7일간 환율은 1.3% 하락해 원화가 소폭 강세를 보이고 있습니다.",
		"김정관 산업장관이 이번 조치에 반대한다고 밝혔습니다.",
		"이는 최근 발표된 정책과 관련이 있습니다.",
		"엘프 뷰티가 실적 호조에 힘입어 목표주가를 상향 조정했습니다.",
		"※ 통계적 재미를 위한 분석입니다.",
	}
	for _, s := range politeCases {
		if ending, found := findInformalSentenceEnding(s); found {
			t.Errorf("expected polite 합쇼체 sentence not to be flagged, but got ending=%q in %q", ending, s)
		}
	}

	// "다양한"/"다른"처럼 단어 중간에 있는 "다"는 문장 경계가 아니므로
	// 걸리지 않아야 한다.
	if _, found := findInformalSentenceEnding("다양한 분야에서 다른 성과를 보였습니다."); found {
		t.Error("expected mid-word '다' (다양한/다른) not to be flagged")
	}

	// 실제 보고된 오탐: "바다"(sea)처럼 "다"로 끝나는 평범한 명사가 문장
	// 중간에서 공백 앞에 오면, 정규식이 문장부호나 문자열 끝을 요구하지
	// 않던 예전 버전에서는 이를 문장 종결로 오인해 반말/기사체로 잘못
	// 판정했다 — 실제 문장은 "…위협받고 있습니다"로 끝나는 정상적인
	// 합쇼체였는데도 말이다.
	politeWithMidSentenceNoun := []string{
		"일본과 중국이 영유권을 놓고 갈등을 벌이는 바다 곳곳에서 무력 충돌 위협이 커지고 있습니다.",
		"이 지역은 여러 나라가 얽힌 바다 한가운데 있어 안보가 위협받고 있습니다.",
	}
	for _, s := range politeWithMidSentenceNoun {
		if ending, found := findInformalSentenceEnding(s); found {
			t.Errorf("expected mid-sentence noun '바다' not to be misdetected as an informal ending, but got ending=%q in %q", ending, s)
		}
	}
}

func TestHashJSONDeterministic(t *testing.T) {
	a := toBriefingExchangeInput(&ExchangeData{From: "USD", To: "KRW", Current: ExchangeRatePoint{Rate: 1470.11, Date: "2026-07-27"}})
	b := toBriefingExchangeInput(&ExchangeData{From: "USD", To: "KRW", Current: ExchangeRatePoint{Rate: 1470.11, Date: "2026-07-27"}})
	c := toBriefingExchangeInput(&ExchangeData{From: "USD", To: "KRW", Current: ExchangeRatePoint{Rate: 1480.00, Date: "2026-07-27"}})

	if hashJSON(a) != hashJSON(b) {
		t.Error("expected identical inputs to produce identical hashes")
	}
	if hashJSON(a) == hashJSON(c) {
		t.Error("expected a different rate to produce a different hash")
	}
}

// TestHashNewsInputIgnoresItemOrder는 뉴스 브리핑 캐시 히트 판단에 실제로
// 쓰이는 hashNewsInput을 검증한다: NewsData.io가 같은 헤드라인 집합을 다른
// 순서로 돌려주더라도(순서를 보장한다는 문서가 없다) 콘텐츠가 동일하면
// 캐시가 불필요하게 무효화되지 않아야 한다. 반대로 콘텐츠 자체가 다르면
// (id가 다른 항목이 섞이면) 여전히 다른 해시를 내야 한다.
func TestHashNewsInputIgnoresItemOrder(t *testing.T) {
	a := &briefingNewsInput{Items: []briefingNewsItem{
		{ID: "aaa", Title: "제목A"},
		{ID: "bbb", Title: "제목B"},
		{ID: "ccc", Title: "제목C"},
	}}
	// 같은 항목, 순서만 다름(셔플).
	b := &briefingNewsInput{Items: []briefingNewsItem{
		{ID: "ccc", Title: "제목C"},
		{ID: "aaa", Title: "제목A"},
		{ID: "bbb", Title: "제목B"},
	}}
	// 실제로 내용이 다름(마지막 항목의 id가 다름).
	c := &briefingNewsInput{Items: []briefingNewsItem{
		{ID: "aaa", Title: "제목A"},
		{ID: "bbb", Title: "제목B"},
		{ID: "ddd", Title: "제목D"},
	}}

	if hashNewsInput(a) != hashNewsInput(b) {
		t.Error("expected reordered-but-identical items to produce the same hash")
	}
	if hashNewsInput(a) == hashNewsInput(c) {
		t.Error("expected genuinely different items to produce a different hash")
	}

	// 정렬은 해시 계산에만 적용되어야 한다 — 원본 Items 슬라이스의 순서
	// (실제 Groq 프롬프트에 쓰이는 순서)는 그대로 남아있어야 한다.
	if a.Items[0].ID != "aaa" || b.Items[0].ID != "ccc" {
		t.Error("hashNewsInput must not mutate the caller's original item order")
	}
}

// TestWeatherExchangeBriefingCacheKeysAreCityAndPairSpecific는 실제로 보고된
// 버그에 대한 수정을 검증한다: 도시(또는 통화쌍)를 전환해도 브리핑에는 *이전*
// 도시/통화쌍의 텍스트가 그대로 남아있던 문제였다. data_hash 비교로 변경 사항을
// 감지해 재생성을 시도하긴 했지만, 그 Groq 호출이 실패하면(예: rate-limited)
// 폴백으로 쓰이는 캐시가 "weather"/"exchange"당 하나의 행만 공유하고 있어서,
// 서울을 보고 있는데 대구의 남은 텍스트가 조용히 서빙되는 문제가 있었다.
// 도시/통화쌍별 복합 키를 사용하면 애초에 다른 도시/통화쌍의 행으로 폴백할
// 여지가 없어진다.
func TestWeatherExchangeBriefingCacheKeysAreCityAndPairSpecific(t *testing.T) {
	if weatherBriefingCacheKey("daegu") == weatherBriefingCacheKey("seoul") {
		t.Error("expected different cities to produce different cache keys")
	}
	if exchangeBriefingCacheKey("USD", "KRW") == exchangeBriefingCacheKey("EUR", "KRW") {
		t.Error("expected different currency pairs to produce different cache keys")
	}
	if weatherBriefingCacheKey("daegu") != weatherBriefingCacheKey("daegu") {
		t.Error("expected the same city to produce the same cache key")
	}
	// 빈 입력(호출부에서 WeatherData/ExchangeData가 nil인 경우)이라도
	// 실제 도시의 키와 충돌할 수 있는 비어 있거나 잘못된 키가 아니라
	// 안정적이고 유효한 키를 만들어내야 한다.
	if weatherBriefingCacheKey("") != "weather:unknown" {
		t.Errorf("expected empty city to fall back to a stable placeholder, got %q", weatherBriefingCacheKey(""))
	}
	if exchangeBriefingCacheKey("", "") != "exchange:unknown:unknown" {
		t.Errorf("expected empty pair to fall back to a stable placeholder, got %q", exchangeBriefingCacheKey("", ""))
	}
}

// TestBriefingSectionCacheNilDB는 DB가 설정되지 않은 상태로 앱이 실행될 때
// 캐시 헬퍼가 네트워크를 건드리거나 패닉을 일으키지 않아야 함을 문서화한다
// — 이 경우 브리핑은 캐싱 없이 매 요청마다 그냥 다시 생성된다.
func TestBriefingSectionCacheNilDB(t *testing.T) {
	if _, found := lookupBriefingSectionCache(context.Background(), nil, "weather"); found {
		t.Error("expected lookup against a nil db to report not-found")
	}
	if err := upsertBriefingSectionCache(context.Background(), nil, "weather", "hash", "text", time.Now(), false); err != nil {
		t.Errorf("expected upsert against a nil db to no-op without error, got %v", err)
	}
}

// openTempBriefingTestDB는 격리된 임시 SQLite/libSQL 파일 DB를 열고 전체
// 마이그레이션(migrate)을 실행한 뒤 반환한다 — briefing_section_cache의
// is_fallback 컬럼처럼 실제 DB 스키마/왕복(round-trip)을 검증해야 하는
// 테스트가, 프로덕션 Turso DB나 다른 테스트와 상태를 공유하지 않고 각자
// 독립된 DB로 실행되게 한다. t.TempDir()이 테스트 종료 시 자동으로
// 정리해주므로 별도 cleanup은 DB 커넥션을 닫는 것뿐이다.
func openTempBriefingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("libsql", "file:"+path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := migrate(conn); err != nil {
		t.Fatalf("migrate temp db: %v", err)
	}
	return conn
}

// TestBriefingSectionCacheIsFallbackRoundTrips는 is_fallback 컬럼이 실제
// DB 왕복(insert -> select)에서 정확히 보존되는지 확인한다 — 이 컬럼이
// 없던 시절에는 hallucinationFallback 결과가 다른 정상 결과와 구분 없이
// 캐싱되어, 실제 보고된 사례처럼("가장 인기 있는 뉴스: A 3.6-ton
// mirror..." 원문이 그대로 고정 재사용됨) 데이터가 안 바뀌는 동안 다시
// 정상 생성을 시도할 기회 자체가 없었다.
func TestBriefingSectionCacheIsFallbackRoundTrips(t *testing.T) {
	conn := openTempBriefingTestDB(t)
	ctx := context.Background()
	const section = "news:test:round-trip"

	if err := upsertBriefingSectionCache(ctx, conn, section, "hash1", "가장 인기 있는 뉴스: Some English Title", time.Now(), true); err != nil {
		t.Fatalf("upsert fallback row: %v", err)
	}
	row, found := lookupBriefingSectionCache(ctx, conn, section)
	if !found {
		t.Fatal("expected to find the row just inserted")
	}
	if !row.isFallback {
		t.Error("expected isFallback to round-trip as true for a fallback result")
	}

	// 같은 섹션에 정상 생성 결과로 다시 쓰면(ON CONFLICT UPDATE)
	// is_fallback도 false로 갱신되어야 한다 — 이후 재생성이 성공하면
	// 다음 요청부터는 다시 정상적으로 캐시가 재사용되어야 하기 때문이다.
	if err := upsertBriefingSectionCache(ctx, conn, section, "hash1", "정상적으로 생성된 한국어 문장입니다.", time.Now(), false); err != nil {
		t.Fatalf("upsert normal row: %v", err)
	}
	row2, found2 := lookupBriefingSectionCache(ctx, conn, section)
	if !found2 {
		t.Fatal("expected to find the row after the update")
	}
	if row2.isFallback {
		t.Error("expected isFallback to round-trip as false once a normal generation overwrites the fallback")
	}
}

// TestResolveBriefingSectionRetriesWhenCachedResultIsFallback은 이번
// 수정의 핵심 시나리오를 재현한다: data_hash가 지금 입력과 완전히 같아도,
// 캐시된 결과가 hallucinationFallback이었다면(is_fallback=true)
// resolveBriefingSection이 이를 "재사용 가능한 캐시"로 취급하지 않고
// 재생성을 시도해야 한다. GROQ_API_KEY를 비워 재생성 시도가 즉시
// errGroqKeyMissing으로 실패하게 만들면, 그 결과가 briefingStatusCached
// (캐시를 그대로 재사용)가 아니라 briefingStatusStaleFallback(재생성을
// 시도했지만 실패해 이전 캐시로 대체)로 나와야 한다 — 이 둘의 차이가
// 바로 "재생성 시도가 실제로 있었는지"를 증명한다.
func TestResolveBriefingSectionRetriesWhenCachedResultIsFallback(t *testing.T) {
	conn := openTempBriefingTestDB(t)
	originalDB := db
	db = conn
	t.Cleanup(func() { db = originalDB })

	t.Setenv("GROQ_API_KEY", "")

	const section = "news:test:fallback-retry"
	const hash = "same-hash"
	const fallbackText = "가장 인기 있는 뉴스: Old English Title"
	if err := upsertBriefingSectionCache(context.Background(), conn, section, hash, fallbackText, time.Now(), true); err != nil {
		t.Fatalf("seed fallback cache: %v", err)
	}

	out := resolveBriefingSection(context.Background(), section, "model", hash, "system", "user", nil, "", "", nil, true, "⚠️ missing")

	if out.Status != briefingStatusStaleFallback {
		t.Errorf("Status = %q, want %q — a cached fallback result must not be served as a plain cache hit even when data_hash matches", out.Status, briefingStatusStaleFallback)
	}
	if out.Text != fallbackText {
		t.Errorf("Text = %q, want the previously cached fallback %q to be served as the stale_fallback value", out.Text, fallbackText)
	}
}

// TestGenerateSectionTextReturnsIsFallbackTrueOnHallucinationFallback은
// generateSectionText가 실제로 hallucinationFallback을 반환할 때
// isFallback=true를 함께 반환하는지 확인한다 — resolveBriefingSection이
// 이 신호로 캐시 저장 여부를 결정하므로, 이 플래그 자체가 정확해야 위
// 두 테스트가 의미를 갖는다. 재현에는 실제 보고됐던 "계약 상대방 날조"
// 패턴(TestFindUngroundedProperNoun_RegressesTheReportedHallucination과
// 같은 입력)을 쓴다 — 이 실패는 항상 hardFailure=true, useFallback=true로
// 분류되어(완화 대상이 아니라) 재시도 후 결정적으로 폴백 경로를 탄다.
func TestGenerateSectionTextReturnsIsFallbackTrueOnHallucinationFallback(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 0)
	t.Setenv("GROQ_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"두산에너빌리티가 노블리스 오일 앤 가스와 계약을 체결했다고 밝혔습니다."}}]}`))
	}))
	defer server.Close()
	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	groundingText := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "두산에너빌리티, 원전·가스터빈 수주 잇따라", Description: "두산에너빌리티가 국내외에서 원전과 가스터빈 관련 수주를 잇따라 확보했다고 밝혔다."},
		},
	})
	const fallback = "가장 인기 있는 뉴스: Doosan Enerbility wins more nuclear and gas turbine contracts"

	text, isFallback, err := generateSectionText(context.Background(), "test-section", frequentGroqModel(), newsSectionSystemPrompt, "user content", nil, groundingText, fallback)
	if err != nil {
		t.Fatalf("expected nil error when a hallucinationFallback is available, got %v", err)
	}
	if !isFallback {
		t.Error("expected isFallback=true when the hallucination fallback path is taken")
	}
	if text != fallback {
		t.Errorf("text = %q, want the fallback %q", text, fallback)
	}
}

// TestGenerateSectionTextWarnsWhenUngroundedNumberChangesBetweenAttempts는
// 이번에 고친 실제 사고를 재현한다: 원문 title이 잘려 단위(million)가
// 사라지면, 모델이 재시도마다(8B -> 70B 승격) 서로 다른 크기의 숫자를
// 추측해내면서 두 시도 모두 findUngroundedNumber에 검증 실패로 걸린다.
// 이 패턴(같은 섹션에서 검증 실패 시 감지된 숫자가 재시도마다 달라짐)이
// 감지되면, 원인이 검증기나 프롬프트 자체가 아니라 원문 입력이 잘려서
// 불완전했을 가능성이 있다는 경고를 로그로 남겨야 원인 추적이 쉬워진다.
func TestGenerateSectionTextWarnsWhenUngroundedNumberChangesBetweenAttempts(t *testing.T) {
	resetGroqUsageForTest()
	resetGroqCallGateForTest(8, 0)
	t.Setenv("GROQ_API_KEY", "test-key")

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		if callCount == 1 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"한 스타트업이 1억 달러 투자를 유치했습니다."}}]}`))
		} else {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"한 스타트업이 10억 달러 투자를 유치했습니다."}}]}`))
		}
	}))
	defer server.Close()
	original := groqEndpoint
	groqEndpoint = server.URL
	defer func() { groqEndpoint = original }()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// allowedNumbers/groundingText를 모두 비워서, "$100…"으로 잘려 근거
	// 자체가 완전히 사라진 실제 상황을 재현한다 — 두 시도 모두 근거 없는
	// 숫자로 걸려야 이 테스트가 의도한 시나리오를 검증한다.
	_, _, err := generateSectionText(context.Background(), "news:test", "model", newsSectionSystemPrompt, "user content", nil, "", "")
	if err == nil {
		t.Fatal("test setup invalid: expected generateSectionText to fail validation on both attempts")
	}
	if callCount != maxSectionRegenerations+1 {
		t.Fatalf("test setup invalid: expected %d Groq calls (initial + escalation retry), got %d", maxSectionRegenerations+1, callCount)
	}

	logged := buf.String()
	if !strings.Contains(logged, "재시도마다 감지된 근거 없는 숫자가 다름") {
		t.Errorf("expected a warning log about the ungrounded number changing between attempts, got logs:\n%s", logged)
	}
	if !strings.Contains(logged, "1e+08") || !strings.Contains(logged, "1e+09") {
		t.Errorf("expected the warning to include both differing values (1e+08 -> 1e+09), got logs:\n%s", logged)
	}
}

// TestResolveBriefingSectionSkipsGroqWhenDataMissing은 실제 보고된 버그에
// 대한 수정을 검증한다: NewsData.io 조회가 실패해 news가 nil인 채로
// getBriefing에 전달되면, 예전에는 "[뉴스 데이터]: null"이라는 의미 없는
// 프롬프트를 그대로 Groq에 보냈다. hasData=false일 때 Groq를 아예
// 호출하지 않는지는, GROQ_API_KEY를 비워서 확인한다 — 만약 가드가
// 깨져서 generateSectionText까지 호출이 새어나갔다면 실패 사유가
// "missing_api_key"로 나왔을 것이고, 가드가 제대로 동작하면 Groq
// 호출 자체가 없으므로 "data_missing"으로 나와야 한다.
func TestResolveBriefingSectionSkipsGroqWhenDataMissing(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")

	dataMissingMessage := "⚠️ 뉴스 데이터를 가져오지 못해 브리핑을 생성할 수 없습니다"
	out := resolveBriefingSection(context.Background(), "test:news-missing", "some-model", "somehash", "system", "user", nil, "", "", nil, false, dataMissingMessage)

	if out.FailureReason != "data_missing" {
		t.Errorf("expected failure reason %q (proving Groq was never called), got %q", "data_missing", out.FailureReason)
	}
	if out.Status != briefingStatusFailed {
		t.Errorf("expected status %q when data is missing and no prior cache exists, got %q", briefingStatusFailed, out.Status)
	}
	if out.Text != dataMissingMessage {
		t.Errorf("expected the caller-provided dataMissingMessage as Text, got %q", out.Text)
	}

	// hasData=true인 정상 경로는 여전히 Groq를 시도해야 한다(그리고 키가
	// 없으므로 missing_api_key로 실패해야 한다) — hasData 가드가 데이터가
	// 있는 경우까지 조용히 건너뛰는 과잉 차단이 아닌지 확인한다.
	normalOut := resolveBriefingSection(context.Background(), "test:news-present", "some-model", "somehash", "system", "user", nil, "", "", nil, true, dataMissingMessage)
	if normalOut.FailureReason != "missing_api_key" {
		t.Errorf("expected hasData=true to still attempt generation (failing with missing_api_key), got reason %q", normalOut.FailureReason)
	}
}

func TestToBriefingNewsInputCapsAtHeadlineCount(t *testing.T) {
	items := make([]NewsItem, 8)
	for i := range items {
		items[i] = NewsItem{ID: strconv.Itoa(i), Title: "headline"}
	}
	input := toBriefingNewsInput(&NewsData{Items: items})
	if len(input.Items) != briefingNewsHeadlineCount {
		t.Errorf("expected %d items, got %d", briefingNewsHeadlineCount, len(input.Items))
	}
}

func TestFindForeignScript(t *testing.T) {
	if _, found := findForeignScript("대구는 오늘 대체로 맑아 우산 없이 외출하기 좋은 날씨입니다."); found {
		t.Error("expected no false positive on ordinary Hangul text")
	}
	if match, found := findForeignScript("这是中文字符가 섞인 문장입니다."); !found || match == "" {
		t.Error("expected Chinese Han characters to be detected")
	}
	if _, found := findForeignScript("これは日本語です가 섞인 문장"); !found {
		t.Error("expected Japanese kana to be detected")
	}
}

// TestFindForeignScript_DetectsNonHangulScriptsBeyondCJK는 실제 보고된
// 오탐 사례를 회귀 테스트로 고정한다: 인도 도시 "Ahmedabad"를 "아마다바드"로
// 표기하려다 힌디어 데바나가리 문자(अहमदाबाद)가 그대로 노출됐다. 국제
// 뉴스가 다룰 수 있는 다른 지역의 문자 체계(아랍어/히브리어/태국어/키릴
// 문자/그리스 문자)도 함께 잡아내는지, 그리고 로마자(영어 고유명사)
// 자체는 이 검사와 무관하게 통과하는지 확인한다 — 비한글이라고 무조건
// 막는 게 아니라, 한글이 아닌 완전히 다른 문자 체계만 막아야 한다.
func TestFindForeignScript_DetectsNonHangulScriptsBeyondCJK(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"데바나가리(힌디어)", "인도 아마다바드अहमदाबाद에서 발생한 사건입니다."},
		{"아랍 문자", "카이رو에서 회담이 열렸습니다."},
		{"히브리 문자", "예루살렘ירושלים에서 협상이 재개됐습니다."},
		{"태국 문자", "방콕ประเทศไทย에서 열린 회의입니다."},
		{"키릴 문자", "모스크바москва에서 발표했습니다."},
		{"그리스 문자", "아테네Αθήνα에서 개최됩니다."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if match, found := findForeignScript(tc.text); !found || match == "" {
				t.Errorf("expected %s to be detected in %q", tc.name, tc.text)
			}
		})
	}

	// 로마자 자체(영어 고유명사를 원문 그대로 쓰는 것)는 이 검사와
	// 무관하게 통과해야 한다 — 비한글이라고 무조건 막으면 "Ahmedabad"를
	// 그대로 쓰는 정상적인 표기까지 막히게 된다.
	if match, found := findForeignScript("인도 Ahmedabad에서 발생한 사건입니다."); found {
		t.Errorf("expected plain Latin script (romanized place name) not to be flagged, got %q", match)
	}
}

func TestFindLeakedEnglish(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantFail bool
	}{
		{"pure Korean", "90억 파라미터 오픈소스 모델이 성과를 보였습니다.", false},
		{"allowed currency code", "1 USD당 1470.11 KRW입니다.", false},
		{"allowed acronym", "이 API는 GPU 자원을 사용합니다.", false},
		{"proper noun preserved", "OpenAI와 SlopCodeBench에서 좋은 결과를 보였습니다.", false},
		{"leaked English verb", "500만 달러의 비용으로 fine-tune 한 결과입니다.", true},
		{"leaked English clause", "the model beat frontier models on catalog review 결과를 보였습니다.", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, found := findLeakedEnglish(tc.text)
			if found != tc.wantFail {
				t.Errorf("findLeakedEnglish(%q) found=%v, want %v", tc.text, found, tc.wantFail)
			}
		})
	}
}

// TestFindUngroundedProperNoun_RegressesTheReportedHallucination는 실제로
// 보고된 버그를 그대로 재현한다: 두산에너빌리티가 계약을 수주했다는 헤드라인에서,
// 모델이 원문 어디에도 없는 가공의 상대방인 "노블리스 오일 앤 가스"를
// 만들어낸 사례다.
func TestFindUngroundedProperNoun_RegressesTheReportedHallucination(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "두산에너빌리티, 원전·가스터빈 수주 잇따라", Description: "두산에너빌리티가 국내외에서 원전과 가스터빈 관련 수주를 잇따라 확보했다고 밝혔다."},
		},
	})

	hallucinated := "두산에너빌리티가 노블리스 오일 앤 가스와 계약을 체결했다고 밝혔습니다."
	if match, viaCounterparty, found := findUngroundedProperNoun(hallucinated, grounding); !found {
		t.Error("expected the fabricated '노블리스 오일 앤 가스' counterparty to be flagged as ungrounded")
	} else {
		if !viaCounterparty {
			t.Errorf("expected the fabricated counterparty to be flagged via the counterparty pattern (not eligible for the core-noun leniency), got viaCounterparty=%v", viaCounterparty)
		}
		t.Logf("correctly flagged fabricated proper noun: %q", match)
	}

	faithful := "두산에너빌리티가 국내외에서 원전과 가스터빈 수주를 잇따라 확보했다고 밝혔습니다."
	if match, _, found := findUngroundedProperNoun(faithful, grounding); found {
		t.Errorf("expected no ungrounded proper noun in a faithful sentence, got %q", match)
	}
}

// TestHasGroundedCoreProperNoun는 실제 보고된 사례를 회귀 테스트로
// 고정한다: 원문에 등장한 "Panthers"가 NFL 소속이라는 상식적인 보충
// 설명은 "Panthers" 자체가 응답에 남아있으므로 완화 대상이 되어야
// 하고, 원문 핵심 개체 자체가 통째로 다른 이름으로 대체된 경우(두산
// 에너빌리티 사례)는 완화 대상이 아니어야 한다.
func TestHasGroundedCoreProperNoun(t *testing.T) {
	panthersGrounding := "Panthers extend winning streak heading into playoffs"
	if !hasGroundedCoreProperNoun("Panthers가 NFL 소속 팀으로서 좋은 성적을 거두고 있습니다.", panthersGrounding) {
		t.Error("expected 'Panthers' (grounded in the source) to satisfy the core-noun leniency check")
	}

	doosanGrounding := "두산에너빌리티 원전·가스터빈 수주"
	if hasGroundedCoreProperNoun("완전히 새로운 회사 이야기입니다.", doosanGrounding) {
		t.Error("expected no grounded core noun when the generated text drops the source's real entity entirely")
	}
	// "두산에너빌리티" 자체는 여전히 남아있을 수 있다는 점이 중요하다 —
	// 이 함수 하나만으로는 계약 상대방 날조를 구분하지 못하므로,
	// validateSectionOutput이 lenientIfCoreNounSurvives를 계약 상대방
	// 실패에는 애초에 false로 고정해 이 leniency 자체가 적용되지 않게
	// 막는다(TestValidateSectionOutputPrecedence 참고).
	if !hasGroundedCoreProperNoun("두산에너빌리티가 노블리스 오일 앤 가스와 계약을 체결했습니다.", doosanGrounding) {
		t.Error("expected '두산에너빌리티' itself to still be found as a grounded core noun (the guard against this case lives in validateSectionOutput, not here)")
	}

	if hasGroundedCoreProperNoun("아무 관련 없는 문장입니다.", "") {
		t.Error("expected empty groundingText to never be considered grounded")
	}
}

// TestFindTopicMismatch_RegressesTheCosmeticsToAIHallucination는 실제
// 보고된 사례를 회귀 테스트로 고정한다: 청소년 화장품 압수 관련 기사가
// AI/기술 관련 문장으로 완전히 둔갑한 경우다. 이 사례에는 지어낸 고유명사나
// 숫자가 없을 수도 있어(예: "AI 모델이 벤치마크에서..."처럼 고유명사 없이도
// 완전히 다른 소재로 서술 가능) findUngroundedProperNoun/findUngroundedNumber
// 만으로는 못 잡을 수 있다 — findTopicMismatch는 원문과 생성문의 명사성
// 토큰 집합 자체가 거의 겹치지 않는다는, 훨씬 거친 신호로 이를 잡아낸다.
func TestFindTopicMismatch_RegressesTheCosmeticsToAIHallucination(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "화장품 압수당한 16살 청소년 정학 처분 놓고 논란", Description: "학교 측이 화장을 한 학생의 화장품을 압수하고 정학 처분을 내리면서 학부모들 사이에서 논란이 일고 있다."},
		},
	})

	hallucinated := "AI 모델이 벤치마크 평가에서 최고 성능을 기록했다는 소식이 전해졌습니다."
	if ratio, found := findTopicMismatch(hallucinated, grounding); !found {
		t.Errorf("expected the AI/tech hallucination to be flagged as topic mismatch, got ratio=%.2f", ratio)
	} else {
		t.Logf("correctly flagged topic mismatch: overlap ratio %.2f", ratio)
	}

	faithful := "한 학교가 화장품을 압수당한 16살 학생에게 정학 처분을 내려 논란이 일고 있습니다."
	if ratio, found := findTopicMismatch(faithful, grounding); found {
		t.Errorf("expected no topic mismatch for a faithful summary of the same story, got ratio=%.2f", ratio)
	}
}

func TestExtractTopicTokensStripsCommonParticles(t *testing.T) {
	tokens := extractTopicTokens("화장품을 압수당한 청소년이 학교에서 정학 처분을 받았다")
	for _, want := range []string{"화장품", "청소년"} {
		if !tokens[want] {
			t.Errorf("expected token %q after particle stripping, got %v", want, tokens)
		}
	}
}

// TestFindTopicMismatch_DoesNotFlagFaithfulParaphraseOfATerseHeadline은
// 라이브 테스트 중 실제로 관측된 오탐을 회귀 테스트로 고정한다: 압축된
// 증권 헤드라인("[美특징주]KLA, 1Q 실적 가이드라인 실망감 주가 8%↓")을
// 정상적으로 풀어쓴 요약조차, 분모를 생성문 토큰 수로 잡았을 때는
// 8~17%의 중복도로 나와 "원문과 무관한 주제"로 오탐되어 안전 문구로
// 대체되는 것이 실제로 확인됐다(첫 구현 버전). 분모를 원문 토큰 수로
// 바꾸고 임계값을 0.15로 낮춘 뒤에는, 아래처럼 실측된 정상 의역 두 건
// 모두 통과해야 한다 — 그러면서도 완전히 다른 주제로 둔갑한 경우(다른
// 테스트의 화장품→AI 사례, 중복도 0%)는 여전히 잡아내야 한다.
func TestFindTopicMismatch_DoesNotFlagFaithfulParaphraseOfATerseHeadline(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "[美특징주]KLA, 1Q 실적 가이드라인 실망감 주가 8%↓"},
		},
	})

	faithfulParaphrases := []string{
		"KLA는 1분기 시가총액이 8% 감소했다는 소식이 전해졌습니다.",
		"미국의 반도체 장비 업체 KLA는 1분기 실적 가이드라인에서 실망한 실적을 기록하여 주가가 8% 하락했다.",
	}
	for _, text := range faithfulParaphrases {
		if ratio, found := findTopicMismatch(text, grounding); found {
			t.Errorf("expected no topic mismatch for faithful paraphrase %q, got ratio=%.2f", text, ratio)
		}
	}
}

// TestFindTopicMismatch_SkipsNonKoreanGroundingText는 실제 라이브 테스트 중
// 확인된 두 번째 오탐을 회귀 테스트로 고정한다: 해외(international) 모드의
// 원문은 영어이고 생성문은 한국어이므로, 정확한 번역조차 원문과 문자열이
// 전혀 겹치지 않는다 — 예를 들어 "Trump to announce plans for Dulles Airport
// makeover"를 "도널드 트럼프가 워싱턴 덜레스 국제공항의 리모델링 계획을
// 발표할 예정이라고 합니다"로 정확히 번역해도, "Trump"/"Dulles"/"Airport"가
// 표기 관례에 따라 "트럼프"/"덜레스"/"공항"으로 옮겨지므로 토큰 문자열
// 자체가 달라 중복도가 0%로 나온다. 원문에 한글이 전혀 없으면(=번역이
// 필요한 해외 모드) 이 검사 자체를 건너뛰어야 한다.
func TestFindTopicMismatch_SkipsNonKoreanGroundingText(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "Trump to announce plans for Dulles Airport makeover"},
		},
	})
	faithfulTranslation := "도널드 트럼프가 워싱턴 덜레스 국제공항의 리모델링 계획을 발표할 예정이라고 합니다."

	if ratio, found := findTopicMismatch(faithfulTranslation, grounding); found {
		t.Errorf("expected non-Korean grounding text to skip the check entirely, got flagged with ratio=%.2f", ratio)
	}
}

// TestFindTopicMismatch_DoesNotFlagFaithfulSummaryOfOneOfSeveralHeadlines는
// 실제 보고된 오탐 사례를 회귀 테스트로 고정한다: 원유/엔화/CodeRabbit
// 투자유치라는 서로 무관한 헤드라인 3개가 입력됐고, 모델이 그중
// CodeRabbit 하나만 정확하게 요약했는데, 예전에는 "전체 헤드라인 토큰
// 합집합" 대비 비율을 써서 항목 수(3개)만큼 비율이 옅어져(실측 6%)
// hallucination으로 오판됐다. 헤드라인별로 개별 계산 후 최댓값을 쓰면,
// CodeRabbit 헤드라인 하나와는 높은 중복도가 나와야 정상 통과한다.
func TestFindTopicMismatch_DoesNotFlagFaithfulSummaryOfOneOfSeveralHeadlines(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "국제유가 소폭 상승, 공급 우려 완화", Description: "국제유가가 산유국들의 공급 우려가 완화되며 소폭 상승했다."},
			{ID: "2", Title: "엔화 약세 지속, 달러당 150엔대 근접", Description: "일본은행의 통화정책 완화 기조가 이어지며 엔화 약세가 지속되고 있다."},
			{ID: "3", Title: "AI 코드리뷰 스타트업 코드래빗, 대규모 투자 유치", Description: "코드래빗이 신규 투자 유치에 성공하며 기업가치를 크게 끌어올렸다."},
		},
	})

	faithfulSummaryOfOneHeadline := "AI 코드리뷰 스타트업 코드래빗이 대규모 투자를 유치하며 기업가치를 끌어올렸습니다."
	if ratio, found := findTopicMismatch(faithfulSummaryOfOneHeadline, grounding); found {
		t.Errorf("expected no topic mismatch when the summary faithfully covers one of several headlines, got ratio=%.2f", ratio)
	} else {
		t.Logf("correctly passed: overlap ratio %.2f against the matching headline", ratio)
	}
}

// TestFindTopicMismatch_StillFlagsHallucinationAmongMultipleHeadlines는
// 위 회귀 테스트의 반대 사례를 확인한다: 헤드라인이 여러 개라도, 생성문이
// 그중 어떤 헤드라인과도 무관한 완전히 다른 소재를 지어냈다면 여전히
// 잡혀야 한다 — 헤드라인별 최댓값 방식이 "여러 개 중 하나를 관대하게
// 봐주는" 것으로 진짜 hallucination까지 놓치게 되지는 않는지 검증한다.
func TestFindTopicMismatch_StillFlagsHallucinationAmongMultipleHeadlines(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "국제유가 소폭 상승, 공급 우려 완화", Description: "국제유가가 산유국들의 공급 우려가 완화되며 소폭 상승했다."},
			{ID: "2", Title: "엔화 약세 지속, 달러당 150엔대 근접", Description: "일본은행의 통화정책 완화 기조가 이어지며 엔화 약세가 지속되고 있다."},
			{ID: "3", Title: "AI 코드리뷰 스타트업 코드래빗, 대규모 투자 유치", Description: "코드래빗이 신규 투자 유치에 성공하며 기업가치를 크게 끌어올렸다."},
		},
	})

	hallucinated := "화장품을 압수당한 16살 청소년이 정학 처분을 받아 학부모들 사이에서 논란이 일고 있습니다."
	if ratio, found := findTopicMismatch(hallucinated, grounding); !found {
		t.Errorf("expected the unrelated hallucination to be flagged even with multiple headlines in the input, got ratio=%.2f", ratio)
	}
}

// TestFindFabricatedPercentage_RegressesTheMercantileBankHallucination는
// 두 번째로 실제 보고된 환각(hallucination) 사례다: 원문 헤드라인("Mercantile
// Bank Corporation stock hits all-time high at 60.42 USD")에는 퍼센트가
// 전혀 등장하지 않는데도, 모델이 올바른 숫자(60.42)를 그대로 가져다가
// 지어낸 "지분 매각" 서사에 갖다 붙여, 주가(PRICE)를 지분 비율(PERCENTAGE)로
// 잘못 재해석한 것이다. findUngroundedProperNoun은 이를 잡아내지 못하고
// (새로 지어낸 회사명이 없고 "Mercantile Bank Corporation"은 실존한다),
// findUngroundedNumber도 마찬가지다(60.42라는 숫자 자체는 원문에 실제로
// 존재하고, 단지 다른 단위에 붙었을 뿐이다) — 이는 별도의 검사가 필요한
// 독립된 실패 유형이다.
func TestFindFabricatedPercentage_RegressesTheMercantileBankHallucination(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "Mercantile Bank Corporation stock hits all-time high at 60.42 USD", Description: "Mercantile Bank Corporation stock hits all-time high at 60.42 USD"},
		},
	})

	hallucinated := "Mercantile Bank Corporation은 60.42%의 지분을 보유한 회사에 대한 지분을 매각했다는 소식이 전해졌습니다."
	if match, found := findFabricatedPercentage(hallucinated, grounding); !found {
		t.Error("expected the fabricated '60.42%' stake figure to be flagged — source has no percent sign at all")
	} else {
		t.Logf("correctly flagged fabricated percentage: %q", match)
	}

	faithful := "Mercantile Bank Corporation 주가가 60.42달러로 사상 최고치를 기록했습니다."
	if match, found := findFabricatedPercentage(faithful, grounding); found {
		t.Errorf("expected no fabricated percentage in a faithful sentence with no %% sign, got %q", match)
	}

	// 원문 어딘가에 실제로 퍼센트가 언급되어 있는 경우, 단순 부분 문자열
	// 검사만으로 판단하기에는 위험 부담이 너무 크다 — findFabricatedPercentage는
	// 오탐(false positive) 위험을 감수하느니 일부러 아무 판정도 내리지 않는다.
	groundingWithPercent := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "Some Corp reports 60.42% revenue growth"},
		},
	})
	if _, found := findFabricatedPercentage("Some Corp는 60.42% 성장했다고 밝혔습니다.", groundingWithPercent); found {
		t.Error("expected findFabricatedPercentage to stay silent when the source itself mentions a percentage")
	}
}

func TestFindRepeatedPhrase_RegressesTheMercantileBankRepetitionLoop(t *testing.T) {
	looped := "Mercantile Bank Corporation은 60.42%의 지분을 보유한 60.42%의 지분을 보유한 회사에 대한 지분을 매각했다는 소식이 전해졌습니다."
	if phrase, found := findRepeatedPhrase(looped); !found {
		t.Error("expected the repeated '60.42%의 지분을 보유한' clause to be flagged")
	} else {
		t.Logf("correctly flagged repeated phrase: %q", phrase)
	}

	clean := "대구는 오늘 대체로 맑아 우산 없이 외출하기 좋은 날씨입니다."
	if phrase, found := findRepeatedPhrase(clean); found {
		t.Errorf("expected no repeat in an ordinary sentence, got %q", phrase)
	}
}

// TestFindRepeatedPhrase_AllowsNaturalCrossSentenceMention은 실제 보고된
// 오탐을 회귀 테스트로 고정한다: "Mesa Laboratories" 같은 회사명이 서로
// 다른 두 문장에서 각각 한 번씩 자연스럽게 언급된 것을, 순전히 부분
// 문자열 길이만 보던 예전 검사는 반복 루프로 오판했다. 두 등장 사이에
// 실제 내용(수십 자)이 있으면 반복 루프가 아니라 정상적인 재언급으로
// 봐야 한다.
func TestFindRepeatedPhrase_AllowsNaturalCrossSentenceMention(t *testing.T) {
	natural := "Mesa Laboratories가 3분기 매출 전망을 하향 조정했다고 밝혔습니다. Mesa Laboratories의 주가는 이 소식에 하락했습니다."
	if phrase, found := findRepeatedPhrase(natural); found {
		t.Errorf("expected a company name mentioned naturally across two different sentences not to be flagged as a repetition loop, got %q", phrase)
	}

	// 진짜 생성 루프(완전히 동일한 문장이 그대로 반복)는 여전히 걸러져야
	// 한다 — findRepeatedSentence가 이 케이스를 담당한다.
	sentenceLoop := "환율은 1 USD당 1452.35 KRW입니다. 환율은 1 USD당 1452.35 KRW입니다."
	if _, found := findRepeatedPhrase(sentenceLoop); !found {
		t.Error("expected a fully repeated sentence to still be flagged as a generation loop")
	}
}

func TestFindRepeatedSentence(t *testing.T) {
	if _, found := findRepeatedSentence("환율은 1 USD당 1452.35 KRW입니다. 환율은 1 USD당 1452.35 KRW입니다."); !found {
		t.Error("expected an identical sentence repeated verbatim to be flagged")
	}
	if _, found := findRepeatedSentence("Mesa Laboratories가 실적을 발표했습니다. 주가는 소폭 하락했습니다."); found {
		t.Error("expected two distinct sentences not to be flagged")
	}
	if _, found := findRepeatedSentence("맑습니다."); found {
		t.Error("expected a single short sentence not to be flagged")
	}
}

// TestValidateSectionOutputPerField_AvoidsSimpleDetailedOverlapFalsePositive는
// 실제 운영 중 발견된 오탐을 회귀 테스트로 고정한다: briefingCommonRules는
// detailed의 첫 문장이 simple과 동일하도록 요구하므로, 두 필드를 이어붙인
// combined 문자열에는 같은 문장이 항상 두 번 나타난다. generateSectionText는
// validateSectionOutput을 combined가 아니라 simple/detailed에 각각 따로
// 호출하므로, 이 정상적인 구조적 중복은 반복 감지에 걸리지 않아야 한다.
func TestValidateSectionOutputPerField_AvoidsSimpleDetailedOverlapFalsePositive(t *testing.T) {
	simple := "환율은 1 USD당 1452.35 KRW입니다."
	detailed := simple + " 지난 7일간 환율은 1.4% 하락해 원화가 소폭 강세를 보이고 있습니다."

	// 옛 동작(회귀 확인용): combined를 통째로 검사하면 오탐이 발생했다는
	// 사실 자체를 먼저 확인한다 — 이 sanity check가 실패하면 아래 assertion이
	// 애초에 무엇을 회귀 방지하는지 의미가 없어진다.
	combined := simple + " " + detailed
	if _, found := findRepeatedPhrase(combined); !found {
		t.Fatal("sanity check failed: expected the old combined-string check to demonstrate the false positive")
	}

	if reason, _, _, _ := validateSectionOutput(simple, []float64{1, 1452.35}, ""); reason != "" {
		t.Errorf("simple alone should not fail validation, got %q", reason)
	}
	if reason, _, _, _ := validateSectionOutput(detailed, []float64{1, 1452.35, 7, 1.4}, ""); reason != "" {
		t.Errorf("detailed alone should not fail validation, got %q", reason)
	}
}

func TestFindUngroundedProperNoun(t *testing.T) {
	grounding := newsGroundingText(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "OpenAI released a new model", Description: "The Anthropic rival shipped GPT-5 today."},
		},
	})

	cases := []struct {
		name     string
		text     string
		wantFail bool
	}{
		{"grounded English company name", "OpenAI가 새 모델을 출시했습니다.", false},
		{"grounded English company name via description", "Anthropic의 경쟁 모델이 공개됐습니다.", false},
		{"ungrounded English company name", "Nvidia가 이번 발표에 참여했다고 전해졌습니다.", true},
		{"allowed acronym not flagged", "이 모델은 AI 기술을 사용합니다.", false},
		{"pure Korean, no proper noun", "새로운 모델이 공개되어 화제가 되고 있습니다.", false},
		{"empty grounding text skips the check entirely", "Nvidia와 Noblis Oil이 계약했습니다.", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := grounding
			if tc.name == "empty grounding text skips the check entirely" {
				g = ""
			}
			_, _, found := findUngroundedProperNoun(tc.text, g)
			if found != tc.wantFail {
				t.Errorf("findUngroundedProperNoun(%q) found=%v, want %v", tc.text, found, tc.wantFail)
			}
		})
	}
}

func TestNewsHallucinationFallback(t *testing.T) {
	news := &NewsData{Items: []NewsItem{{Title: "두산에너빌리티, 원전·가스터빈 수주 잇따라"}, {Title: "다른 기사"}}}
	got := newsHallucinationFallback(news)
	want := "가장 인기 있는 뉴스: 두산에너빌리티, 원전·가스터빈 수주 잇따라"
	if got != want {
		t.Errorf("newsHallucinationFallback = %q, want %q", got, want)
	}

	if got := newsHallucinationFallback(&NewsData{}); got != "" {
		t.Errorf("expected empty fallback for no items, got %q", got)
	}
	if got := newsHallucinationFallback(nil); got != "" {
		t.Errorf("expected empty fallback for nil NewsData, got %q", got)
	}
}

// TestToBriefingNewsInputIncludesDescription는 보고된 환각의 근본 원인에
// 대한 수정 사항을 검증한다: 두 번째 사실을 뽑아낼 실제 설명(description)이
// 없으면, 모델은 "상세한" 문장이 요구하는 또 다른 구체적 사실을 채우기
// 위해 무언가를 지어낼 수밖에 없었다.
func TestToBriefingNewsInputIncludesDescription(t *testing.T) {
	news := &NewsData{Items: []NewsItem{{ID: "1", Title: "제목", Description: "이 기사는 $12M 규모의 계약을 다룹니다."}}}
	input := toBriefingNewsInput(news)
	if len(input.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(input.Items))
	}
	if input.Items[0].Description == "" {
		t.Error("expected Description to be carried through, got empty string")
	}
	if strings.Contains(input.Items[0].Description, "$12M") {
		t.Errorf("expected Description to go through annotateNumericUnits like Title does, got %q", input.Items[0].Description)
	}
}

// TestToBriefingNewsInputTruncatesLongDescriptions는 이 기능을 실제로
// 검증하는 과정에서 발견된 토큰 비용 문제에 대한 수정을 지킨다: 5개의
// 헤드라인 전부에 대해 (잘리지 않은) 전체 설명을 포함시키자, 뉴스 브리핑
// 호출 한 번의 토큰 사용량이 약 1~2천에서 약 1만 600으로 뛰어올랐었다.
func TestToBriefingNewsInputTruncatesLongDescriptions(t *testing.T) {
	long := strings.Repeat("가", briefingNewsDescriptionMaxRunes+100)
	news := &NewsData{Items: []NewsItem{{ID: "1", Title: "제목", Description: long}}}
	input := toBriefingNewsInput(news)

	got := []rune(input.Items[0].Description)
	if len(got) > briefingNewsDescriptionMaxRunes {
		t.Errorf("expected Description truncated to at most %d runes, got %d", briefingNewsDescriptionMaxRunes, len(got))
	}
}

// TestTruncateRunesIsMultiByteSafe는 잘라내기가 바이트 경계가 아니라 rune
// 경계에서 이루어지는지 확인한다 — 한글 문자는 UTF-8에서 각각 3바이트이므로,
// 단순히 string[:n]으로 바이트 슬라이싱하면 문자 하나를 깔끔하게 제거하는
// 대신 깨뜨리게 된다.
func TestTruncateRunesIsMultiByteSafe(t *testing.T) {
	s := "가나다라마"
	got := truncateRunes(s, 3)
	want := "가나다"
	if got != want {
		t.Errorf("truncateRunes(%q, 3) = %q, want %q", s, got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncateRunes produced invalid UTF-8: %q", got)
	}

	// 제한보다 짧은 경우: 변경 없이 그대로 반환된다.
	if got := truncateRunes("짧음", 10); got != "짧음" {
		t.Errorf("expected a short string to pass through unchanged, got %q", got)
	}
}

// TestTruncateForPromptCutsAtWordBoundary는 실제 보고된 hallucination
// 사례("…총으로 쏘려고 시도하여 17년에서 무기징역을" 같은 비문)에 대한
// 수정 사항을 검증한다: 단어 중간에서 뚝 끊는 대신 마지막 공백까지
// 되돌아가고, 잘렸다는 신호로 말줄임표를 남긴다.
func TestTruncateForPromptCutsAtWordBoundary(t *testing.T) {
	s := "A man attempted to shoot the driver and was sentenced to seventeen years"
	got := truncateForPrompt(s, 40)

	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected truncated text to end with an ellipsis marker, got %q", got)
	}
	if strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Errorf("expected trailing space before the ellipsis to be trimmed, got %q", got)
	}
	// 자른 지점 바로 앞이 공백이었어야 한다(원문에서 그 단어가 온전히
	// 남아있는지 확인) — 단어 중간에서 끊겼다면 원문에 그 조각이 실제
	// 단어로 나타나지 않는다.
	body := strings.TrimSuffix(got, "…")
	if body != "" && !strings.Contains(s, body+" ") && body != s {
		t.Errorf("expected cut to land on a word boundary, got body=%q", body)
	}
	if got := []rune(got); len(got) > 40 {
		t.Errorf("expected truncated result (including ellipsis) to be at most 40 runes, got %d", len(got))
	}

	// 원문이 제한보다 짧으면 손대지 않고 그대로 반환한다.
	if got := truncateForPrompt("짧은 설명", 80); got != "짧은 설명" {
		t.Errorf("expected short text to pass through unchanged, got %q", got)
	}

	// 공백이 전혀 없는 텍스트(단어 하나가 제한보다 긴 경우)는 단어 경계로
	// 되돌아갈 수 없으므로 하드컷 지점 그대로 말줄임표만 붙인다 — 그래도
	// 전체 길이가 제한을 넘어서는 안 된다.
	noSpace := strings.Repeat("가", 100)
	if got := []rune(truncateForPrompt(noSpace, 80)); len(got) > 80 {
		t.Errorf("expected no-space truncation (including ellipsis) to be at most 80 runes, got %d", len(got))
	}
}

// TestTruncateForPromptDoesNotDropAWordThatAlreadyFitsExactly는 실제 보고된
// 재발 사례를 회귀 테스트로 고정한다: 하드컷 지점이 우연히 이미 완전한
// 단어 경계(잘린 지점 바로 다음 글자가 공백)와 일치하면, 그 마지막 단어를
// 불필요하게 잘라내면 안 된다. 실제 사례: NewsData.io 헤드라인 description
// "...a record $540.2 million grant..."가 briefingNewsDescriptionMaxRunes
// (80)에서 공교롭게도 "million" 바로 뒤에서 깔끔하게 잘렸는데도, 예전
// 로직은 무조건 마지막 공백까지 되돌아가 이미 온전했던 "million"이라는
// 단어 전체를 잘라내 "...a record $540.2…"만 남겼다. 그러면
// annotateNumericUnits가 매칭할 단위(million)가 사라져 "$540.2"가
// 변환되지 않은 채 프롬프트에 그대로 남았고, 모델이 단위 없는 이 숫자를
// 스스로 어림잡다 "5억"(정답 5.4억과 약 7.4% 차이)을 만들어내
// findUngroundedNumber에 근거 없는 숫자로 걸렸다.
func TestTruncateForPromptDoesNotDropAWordThatAlreadyFitsExactly(t *testing.T) {
	desc := "Gates Foundation awards University of Washington's IHME a record $540.2 million grant to expand global health data and disease tracking."
	got := truncateForPrompt(desc, briefingNewsDescriptionMaxRunes)

	if !strings.Contains(got, "million") {
		t.Errorf("expected the word \"million\" (which fit exactly at the truncation boundary) to be preserved, got %q", got)
	}
	if got := []rune(got); len(got) > briefingNewsDescriptionMaxRunes {
		t.Errorf("expected truncated result (including ellipsis) to be at most %d runes, got %d", briefingNewsDescriptionMaxRunes, len(got))
	}

	// annotateNumericUnits가 실제로 이 단위를 인식해 변환할 수 있어야
	// 한다 — "million"이 잘렸다면 이 변환 자체가 조용히 실패한다.
	annotated := annotateNumericUnits(got)
	if annotated == got {
		t.Error("expected annotateNumericUnits to convert the $540.2 million amount once \"million\" is preserved, but the text was left unchanged")
	}
	if !strings.Contains(annotated, "억") {
		t.Errorf("expected the amount to be converted to a 억 단위 Korean amount, got %q", annotated)
	}
}

// TestTruncateForPromptPreservesNumericUnitSpanningTheCutPoint는 실제
// 보고된 새로운 재발 사례를 회귀 테스트로 고정한다: 위
// TestTruncateForPromptDoesNotDropAWordThatAlreadyFitsExactly가 고친
// 사례는 하드컷 지점이 "million" 바로 뒤(단어 전체가 이미 끝난 지점)였지만,
// 이번 사례는 하드컷 지점이 "$100"과 " million" *사이의 공백*에 걸린다 —
// 그 지점은 여전히 "이미 깔끔한 단어 경계"로 보이므로(다음 글자가 공백)
// 기존 보정 로직은 아예 손대지 않고 그대로 "A firm announces $100…"를
// 반환해, "million"이라는 단위 전체가 통째로 사라지는 사고로
// 이어졌었다. extendCutToPreserveNumericToken이 이 경우를 잡아 cutIdx를
// "million" 끝까지 늘려야 한다.
func TestTruncateForPromptPreservesNumericUnitSpanningTheCutPoint(t *testing.T) {
	title := "A firm announces $100 million in new funding for its expansion plans"
	// limit(=maxRunes-1)이 정확히 "$100"과 " million" 사이의 공백에 오도록
	// maxRunes를 골랐다 — 아래 assert가 이 전제 자체를 검증한다.
	const maxRunes = 22
	runes := []rune(title)
	if runes[maxRunes-1] != ' ' {
		t.Fatalf("test setup invalid: rune at index %d is not a space, this case does not exercise the reported scenario", maxRunes-1)
	}

	got := truncateForPrompt(title, maxRunes)

	if !strings.Contains(got, "$100 million") {
		t.Fatalf("expected the full numeric unit expression \"$100 million\" to be preserved even though it extends past maxRunes, got %q", got)
	}
	if strings.HasSuffix(got, "$100…") {
		t.Fatal("regression: the unit word \"million\" was dropped, leaving a bare \"$100…\" that annotateNumericUnits cannot convert")
	}

	// 잘린 결과라도 annotateNumericUnits가 여전히 이 금액을 인식해
	// 변환할 수 있어야 한다 — 이게 실제 버그의 핵심이었다: 단위가
	// 사라지면 변환 자체가 조용히 실패해 모델이 단위 없는 숫자를 스스로
	// 어림잡다 검증에 반복 실패했다.
	annotated := annotateNumericUnits(got)
	if !strings.Contains(annotated, "1억") {
		t.Errorf("expected \"$100 million\" to convert to \"1억 달러\", got %q (from %q)", annotated, got)
	}
}

// TestTruncateForPromptStillBacksUpOnAGenuineMidWordCut은 위 수정이 원래
// 목적(TestTruncateForPromptCutsAtWordBoundary가 고정한, 실제로 단어
// 중간에서 잘리는 경우)을 여전히 올바르게 처리하는지 확인한다 — 하드컷
// 지점이 진짜로 단어 한가운데라면(다음 글자가 공백이 아니면) 여전히
// 마지막 공백까지 되돌아가야 하고, 잘린 단어 조각이 결과에 그대로
// 남아있으면 안 된다.
func TestTruncateForPromptStillBacksUpOnAGenuineMidWordCut(t *testing.T) {
	s := "The quick brown fox jumps over the lazy dog while researchers watch closely"
	const limit = 30
	// 하드컷 지점(29번째 rune, ellipsis 한 글자를 위해 -1)이 실제로 단어
	// 중간인지 먼저 확인한다 — 테스트 자체가 의도한 시나리오를 검증하지
	// 못하는 것을 막기 위한 안전장치다.
	if runes := []rune(s); runes[limit-1] == ' ' {
		t.Fatalf("test setup invalid: rune at index %d is a space, this case does not exercise a genuine mid-word cut", limit-1)
	}

	got := truncateForPrompt(s, limit)
	body := strings.TrimSuffix(got, "…")

	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected truncated text to end with an ellipsis marker, got %q", got)
	}
	if body != "" && !strings.Contains(s, body+" ") {
		t.Errorf("expected the cut to land on a word boundary from the original string, got body=%q", body)
	}
	if got := []rune(got); len(got) > limit {
		t.Errorf("expected truncated result (including ellipsis) to be at most %d runes, got %d", limit, len(got))
	}
}

// TestPickNewsItemToExclude는 8B/70B 모두 실패한 뉴스 생성 결과가 주어졌을
// 때, generateNewsSectionText가 어떤 항목을 제외 대상으로 고르는지
// 검증한다.
// TestToBriefingNewsInputDoesNotTruncateRealisticTitles는 실제 보고된
// 사고를 toBriefingNewsInput 수준(truncateForPrompt를 직접 부르는 것이
// 아니라 실제 파이프라인 진입점)에서 재현한다: "...announces $100
// million..." 형태의 제목이 title 상한(옛 80자)에 걸려 "$100…"으로
// 잘려나가면서 단위(million)가 소실됐다 — title은 description과 달리
// "정상적으로 자주 잘리는" 필드가 아니어야 하므로(briefingNewsTitleMaxRunes
// 문서 참고), 실측 최장 헤드라인 수준의 제목까지는 전혀 잘리지 않아야
// 한다.
func TestToBriefingNewsInputDoesNotTruncateRealisticTitles(t *testing.T) {
	// 실제 관측된 장황한 헤드라인(약 118자)과, 버그가 보고된 통화+단위
	// 헤드라인 둘 다 검증한다.
	longRealisticTitle := "Ontario woman who went missing from Shambhala Music Festival in B.C. posts thank you video to rescuers, shares details"
	moneyTitle := "Startup announces $100 million in new funding to accelerate international expansion plans"

	news := &NewsData{Items: []NewsItem{
		{ID: "1", Title: longRealisticTitle},
		{ID: "2", Title: moneyTitle},
	}}

	input := toBriefingNewsInput(news)

	if strings.HasSuffix(input.Items[0].Title, "…") {
		t.Errorf("expected a realistic-length real-world headline (%d runes) not to be truncated, got %q", len([]rune(longRealisticTitle)), input.Items[0].Title)
	}
	if !strings.Contains(input.Items[1].Title, "1억") {
		t.Errorf("expected \"$100 million\" to survive intact and convert to \"1억 달러\", got %q", input.Items[1].Title)
	}
	if strings.Contains(input.Items[1].Title, "$100…") || strings.Contains(input.Items[1].Title, "$100 …") {
		t.Errorf("regression: the unit word was dropped mid-title, got %q", input.Items[1].Title)
	}
}

func TestPickNewsItemToExclude(t *testing.T) {
	items := []briefingNewsItem{
		{ID: "1", Title: "한 스타트업이 5000만 달러 투자를 유치했다", Description: ""},
		{ID: "2", Title: "한 남성이 17년형을 선고받았다", Description: "무기징역으로 변경됨"},
		{ID: "3", Title: "환경 규제가 강화됐다", Description: ""},
	}

	// 실패한 생성문에 등장한 숫자(17)가 items[1]의 숫자와 겹치므로 그
	// 항목을 제외 대상으로 골라야 한다.
	if idx := pickNewsItemToExclude("운전자를 위협하여 17년에서 무기징역을 선고받았다", items); idx != 1 {
		t.Errorf("expected item index 1 (matching number 17) to be picked, got %d", idx)
	}

	// 겹치는 숫자가 전혀 없으면(판별 불가) 우선순위가 가장 낮은 마지막
	// 항목을 기본값으로 제외한다.
	if idx := pickNewsItemToExclude("아무 숫자도 없는 이상한 문장입니다", items); idx != len(items)-1 {
		t.Errorf("expected fallback to the last (lowest-priority) item when no number overlaps, got %d", idx)
	}
}

func TestAllowedNewsNumbersIncludesDescriptionNumbers(t *testing.T) {
	input := toBriefingNewsInput(&NewsData{Items: []NewsItem{{ID: "1", Title: "제목엔 숫자 없음", Description: "설명에는 90억 달러가 있습니다."}}})
	nums := allowedNewsNumbers(input)

	found := false
	for _, n := range nums {
		if n == 9e9 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a number appearing only in Description to be in the allowed set, got %v", nums)
	}
}

func TestExtractNumbers(t *testing.T) {
	cases := []struct {
		text string
		want []float64
	}{
		{"90억 파라미터 모델", []float64{9e9}},
		{"1200만 달러를 투입", []float64{1.2e7}},
		{"1 USD당 1470.11 KRW입니다", []float64{1, 1470.11}},
		{"지난 7일간 0.6% 하락", []float64{7, 0.6}},
		{"숫자가 없는 문장", nil},
		{"$3,500 상당의 계약", []float64{3500}},
		{"1,000.5 처럼 소수점과 쉼표가 함께 있는 경우", []float64{1000.5}},
		{"12,345,678원을 기록했습니다", []float64{12345678}},
	}

	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			got := extractNumbers(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("extractNumbers(%q) = %v, want %v", tc.text, got, tc.want)
			}
			for i := range got {
				if !numbersMatch(got[i], tc.want[i]) {
					t.Errorf("extractNumbers(%q)[%d] = %v, want %v", tc.text, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestNumbersMatch(t *testing.T) {
	cases := []struct {
		a, b float64
		want bool
	}{
		{9e9, 9e9, true},
		{9e9, 9.01e9, true}, // "약 90억" 표현을 위한 약간의 반올림 허용 오차
		{26.75, 26.8, true}, // 소수점 반올림 허용 오차
		{500, 1.2e7, false}, // 실제로 관측된 환각 사례
		{0.6, 0.6, true},
		{7, 8, false},
	}

	for _, tc := range cases {
		if got := numbersMatch(tc.a, tc.b); got != tc.want {
			t.Errorf("numbersMatch(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestFindUngroundedNumber_RegressesTheObservedHallucination는 이 검증
// 로직을 만들게 된 계기가 된 정확한 실패 사례다: 헤드라인에는 그냥 "$500"라고
// 쓰여 있을 뿐인데(K/M/B 같은 축약 표기를 잘못 해석할 여지조차 없음), 그런데도
// 모델이 종종 "1200만 달러"(12,000,000)라고 써냈다 — 무려 24,000배나 큰
// 값이며, 입력에는 그 정도 규모에 근접한 숫자조차 없었다.
func TestFindUngroundedNumber_RegressesTheObservedHallucination(t *testing.T) {
	allowed := allowedNewsNumbers(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "A $500 RL fine-tune of a 90억 open model beat frontier models on catalog review"},
		},
	})

	hallucinated := "90억 오픈소스 모델에 1200만 달러를 투입해 파인튜닝한 결과, 최상위 모델을 제쳤습니다."
	if _, found := findUngroundedNumber(hallucinated, "", allowed); !found {
		t.Error("expected the hallucinated 1200만 달러 figure to be flagged as ungrounded")
	}

	faithful := "90억 오픈소스 모델에 500달러를 투입해 파인튜닝한 결과, 최상위 모델을 제쳤습니다."
	if num, found := findUngroundedNumber(faithful, "", allowed); found {
		t.Errorf("expected no ungrounded number in a faithful sentence, got %v", num)
	}
}

// TestFindUngroundedNumberAllowsSportsRoundTranslation은 실제 보고된
// 사례를 회귀 테스트로 고정한다: 영어 원문 "reach Montreal quarters"에는
// literal한 숫자가 없지만, "몬트리올 8강에 진출"은 정확한 번역이므로
// 근거 없는 숫자로 오탐해서는 안 된다.
func TestFindUngroundedNumberAllowsSportsRoundTranslation(t *testing.T) {
	groundingText := "Shelton sweeps Fonseca to reach Montreal quarters"

	cases := []struct {
		generated     string
		groundingText string
	}{
		{"셸턴이 폰세카를 완파하고 몬트리올 8강에 진출했습니다.", groundingText},
		{"두 선수 모두 승리해 4강에 진출했습니다.", "Both players advanced to the semifinals"},
		{"이변 없이 16강에 진출했습니다.", "The defending champion advanced to the round of 16"},
	}
	for _, c := range cases {
		if num, found := findUngroundedNumber(c.generated, c.groundingText, nil); found {
			t.Errorf("expected sports round number in %q to be grounded by %q, got flagged number %v", c.generated, c.groundingText, num)
		}
	}

	// 원문에 해당 라운드 용어가 전혀 없으면(예: round of 16 언급이 없는데
	// "16강"이라고 지어낸 경우), 숫자가 8/4/16/32와 우연히 같더라도 그
	// 라운드 예외를 적용해서는 안 된다 — 실제로 그 대회 라운드를 가리키는
	// 근거가 원문에 있는지가 핵심이다.
	unrelatedGrounding := "The company announced quarterly earnings today"
	if num, found := findUngroundedNumber("이 팀은 16강에 진출했습니다.", unrelatedGrounding, nil); !found {
		t.Errorf("expected '16강' without a matching round-of-16 mention in the source to still be flagged, got num=%v found=%v", num, found)
	}

	// 목록에 없는 진짜 근거 없는 숫자(원문에 없는 임의의 수치)는 여전히
	// 걸러져야 한다 — 스포츠 표현이 아닌 지어낸 숫자에는 예외가 적용되지
	// 않는지 확인한다.
	fabricated := []string{
		"셸턴이 3연승을 거두며 몬트리올 8강에 진출했습니다.",
		"셸턴이 개인 통산 5번째 우승을 차지했습니다.",
	}
	for _, s := range fabricated {
		if _, found := findUngroundedNumber(s, groundingText, nil); !found {
			t.Errorf("expected fabricated number in %q not covered by sportsRoundExceptions to still be flagged as ungrounded", s)
		}
	}
}

// TestFindUngroundedNumberAllowsCurrencyUnitTranslation은 실제 보고된
// 사례를 회귀 테스트로 고정한다: 정상적인 흐름에서는 annotateNumericUnits가
// "£25bn"을 이미 "250억 파운드"로 바꿔둔 뒤라 groundingText/allowedNumbers
// 양쪽 다 "250억"을 알고 있어야 하지만(첫 번째 케이스), description이
// 잘리며 단위 글자가 통째로 날아가는 등 어떤 이유로 원문 그대로("£25bn")
// groundingText에 남아있더라도(두 번째 케이스), 생성문의 "250억"을 여전히
// 근거 있는 값으로 인정해야 한다 — extractEnglishUnitNumbers가 이 이중
// 방어선 역할을 한다.
func TestFindUngroundedNumberAllowsCurrencyUnitTranslation(t *testing.T) {
	annotatedInput := toBriefingNewsInput(&NewsData{Items: []NewsItem{
		{ID: "1", Title: "UK unveils £25bn infrastructure plan"},
	}})
	annotatedGrounding := newsGroundingText(annotatedInput)
	allowed := allowedNewsNumbers(annotatedInput)
	if num, found := findUngroundedNumber("영국이 250억 파운드 규모의 인프라 계획을 발표했습니다.", annotatedGrounding, allowed); found {
		t.Errorf("expected the correctly converted 250억 to be grounded via the pre-annotated title, got flagged number %v", num)
	}

	rawGrounding := "UK unveils £25bn infrastructure plan"
	if num, found := findUngroundedNumber("영국이 250억 파운드 규모의 인프라 계획을 발표했습니다.", rawGrounding, nil); found {
		t.Errorf("expected 250억 to be grounded by the raw £25bn left in groundingText, got flagged number %v", num)
	}

	// 잘못 계산된 값(10배 과다, "2500억")은 여전히 걸러져야 한다 —
	// 단위 예외가 아무 숫자에나 면죄부를 주는 게 아니라, 실제로 원문의
	// £25bn을 정확히 환산한 값(250억)만 인정해야 한다.
	if _, found := findUngroundedNumber("영국이 2500억 파운드 규모의 인프라 계획을 발표했습니다.", rawGrounding, nil); !found {
		t.Error("expected the miscalculated 2500억(10x too large) to still be flagged as ungrounded")
	}

	// 목록에 없는 진짜 근거 없는 숫자는 여전히 걸러져야 한다.
	if _, found := findUngroundedNumber("영국이 90억 파운드를 추가로 지원하기로 했습니다.", rawGrounding, nil); !found {
		t.Error("expected a fabricated amount unrelated to £25bn to still be flagged as ungrounded")
	}
}

// TestFindUngroundedNumberAllowsMillionUnitTranslation은 실제 재발한
// 사례를 회귀 테스트로 고정한다: bn(billion) 단위는 예외 처리했지만
// m(million) 단위를 빠뜨려서 "£16m → 1600만"처럼 정확히 환산된 값까지
// 다시 근거 없는 숫자로 오탐했다. numericUnitPattern/parseNumericUnitMatch
// 하나로 k/m/b를 함께 관리하도록 정리한 뒤로는 재발하지 않아야 한다.
func TestFindUngroundedNumberAllowsMillionUnitTranslation(t *testing.T) {
	annotatedInput := toBriefingNewsInput(&NewsData{Items: []NewsItem{
		{ID: "1", Title: "UK invests £16m in the scheme"},
	}})
	annotatedGrounding := newsGroundingText(annotatedInput)
	allowed := allowedNewsNumbers(annotatedInput)
	if num, found := findUngroundedNumber("영국이 이 사업에 1600만 파운드를 투자했습니다.", annotatedGrounding, allowed); found {
		t.Errorf("expected the correctly converted 1600만 to be grounded via the pre-annotated title, got flagged number %v", num)
	}

	rawGrounding := "UK invests £16m in the scheme"
	if num, found := findUngroundedNumber("영국이 이 사업에 1600만 파운드를 투자했습니다.", rawGrounding, nil); found {
		t.Errorf("expected 1600만 to be grounded by the raw £16m left in groundingText, got flagged number %v", num)
	}

	// 잘못 계산된 값이나 무관한 금액은 여전히 걸러져야 한다.
	if _, found := findUngroundedNumber("영국이 이 사업에 1억6000만 파운드를 투자했습니다.", rawGrounding, nil); !found {
		t.Error("expected the miscalculated amount to still be flagged as ungrounded")
	}
}

// TestFindUngroundedNumber_DoesNotFlagCommaFormattedAmount는 실제 보고된
// 오탐 사례를 회귀 테스트로 고정한다: 원문 헤드라인의 "$3,500"(천 단위
// 쉼표 표기)과 생성문의 "3500"(쉼표 없는 표기)은 같은 값인데,
// extractNumbers가 쉼표를 숫자 구분자로 처리하지 않으면 "$3,500"이 3과
// 500이라는 서로 무관한 두 숫자로 쪼개져, 생성문의 3500이 그 어느 쪽과도
// 매칭되지 않아 근거 없는 숫자로 오탐됐다.
func TestFindUngroundedNumber_DoesNotFlagCommaFormattedAmount(t *testing.T) {
	allowed := allowedNewsNumbers(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "Startup secures $3,500 seed grant"},
		},
	})

	if num, found := findUngroundedNumber("한 스타트업이 3500달러 규모의 시드 지원금을 확보했습니다.", "", allowed); found {
		t.Errorf("expected 3500 to be grounded by the comma-formatted $3,500 in the source, got flagged number %v", num)
	}

	// 쉼표+소수점이 함께 있는 표기("1,000.5")도 쉼표 없는 표기("1000.5")와
	// 같은 값으로 인식되어야 한다.
	decimalAllowed := allowedNewsNumbers(&briefingNewsInput{
		Items: []briefingNewsItem{
			{ID: "1", Title: "환율이 1,000.5원을 기록했다"},
		},
	})
	if num, found := findUngroundedNumber("환율이 1000.5원을 기록했습니다.", "", decimalAllowed); found {
		t.Errorf("expected 1000.5 to be grounded by the comma-formatted 1,000.5 in the source, got flagged number %v", num)
	}

	// 목록에 없는 진짜 근거 없는 숫자는 쉼표 정규화와 무관하게 여전히
	// 걸러져야 한다.
	if _, found := findUngroundedNumber("한 스타트업이 35000달러 규모의 시드 지원금을 확보했습니다.", "", allowed); !found {
		t.Error("expected a fabricated amount unrelated to $3,500 to still be flagged as ungrounded")
	}
}

// TestFindUngroundedNumber_CommaNormalizationCoexistsWithUnitConversion은
// 이번에 고친 쉼표 정규화가, 이전에 고친 bn/m 단위 환산 검증과 함께
// 정상 동작하는지(서로 간섭하지 않는지) 확인한다 — 같은 헤드라인에 단위
// 축약형이 있는 금액(£25bn)과 쉼표로만 구분된 금액(3,500달러)이 함께
// 있어도 둘 다 각자의 방식으로 근거 있는 값으로 인식되어야 한다.
// annotateNumericUnits는 단위 축약형(£25bn)만 한글로 치환하고 단위 없는
// 쉼표 표기($3,500)는 원문 그대로 두므로, toBriefingNewsInput을 거친
// 정상적인 파이프라인으로 입력을 구성해야 두 메커니즘이 함께 있는
// 실제 상황을 재현할 수 있다.
func TestFindUngroundedNumber_CommaNormalizationCoexistsWithUnitConversion(t *testing.T) {
	annotatedInput := toBriefingNewsInput(&NewsData{Items: []NewsItem{
		{ID: "1", Title: "UK unveils £25bn infrastructure plan, with an initial $3,500 pilot budget"},
	}})
	annotatedGrounding := newsGroundingText(annotatedInput)
	allowed := allowedNewsNumbers(annotatedInput)

	generated := "영국이 250억 파운드 규모의 인프라 계획을 발표했으며, 초기 시범 예산은 3500달러입니다."
	if num, found := findUngroundedNumber(generated, annotatedGrounding, allowed); found {
		t.Errorf("expected both the £25bn->250억 conversion and the comma-formatted $3,500 to be grounded, got flagged number %v", num)
	}
}

func TestAllowedWeatherNumbersIncludesFixedHourLabels(t *testing.T) {
	input := &briefingWeatherInput{
		Current: briefingCurrentWeather{TemperatureC: 33.2},
		Today: briefingDayForecast{
			Morning:   &briefingPeriodForecast{TemperatureC: 26.8},
			Afternoon: &briefingPeriodForecast{TemperatureC: 32.4},
		},
	}

	sentence := "오전 8시엔 26.8도, 오후 2시엔 32.4도이며 맑은 하늘이 이어집니다."
	if num, found := findUngroundedNumber(sentence, "", allowedWeatherNumbers(input)); found {
		t.Errorf("expected the fixed '8시'/'2시' hour labels to be pre-allowed, got flagged number %v", num)
	}
}

func TestAllowedExchangeNumbersIncludesFixedSevenDays(t *testing.T) {
	input := toBriefingExchangeInput(&ExchangeData{
		From: "USD", To: "KRW",
		Current: ExchangeRatePoint{Rate: 1470.11, Date: "2026-07-27"},
		Weekly:  []ExchangeRatePoint{{Date: "2026-07-20", Rate: 1478.4}},
	})

	sentence := "1 USD당 1470.11 KRW입니다. 지난 7일간 0.6% 하락해 KRW 강세를 보이고 있습니다."
	if num, found := findUngroundedNumber(sentence, "", allowedExchangeNumbers(input)); found {
		t.Errorf("expected rate/changePercent/fixed '7일' to be allowed, got flagged number %v", num)
	}
}

// TestToBriefingExchangeInputInvertsSubOneRate는 보고된 KRW->USD 시나리오를
// 다룬다: Current.Rate가 약 0.00069인데, 이 값이 그대로 LLM 프롬프트에
// 전달되어서는 안 된다(문장에서 "0.00"으로 읽히게 된다) — 대신
// BaseCurrency/QuoteCurrency를 "USD"/"KRW"로 뒤바꾸고 Rate는 역수를
// 취해서, 반전되지 않은 경우와 동일한 문구 템플릿으로 "환율은 1 USD당
// 1,449.28 KRW입니다"라고 브리핑할 수 있게 한다.
func TestToBriefingExchangeInputInvertsSubOneRate(t *testing.T) {
	exchange := &ExchangeData{
		From: "KRW", To: "USD",
		Current: ExchangeRatePoint{Date: "2026-07-28", Rate: 0.00069},
		Weekly: []ExchangeRatePoint{
			{Date: "2026-07-21", Rate: 0.00068},
			{Date: "2026-07-28", Rate: 0.00069},
		},
	}

	input := toBriefingExchangeInput(exchange)

	if input.BaseCurrency != "USD" || input.QuoteCurrency != "KRW" {
		t.Errorf("BaseCurrency/QuoteCurrency = %s/%s, want USD/KRW (swapped from From/To)", input.BaseCurrency, input.QuoteCurrency)
	}
	if input.BaseUnits != 1 {
		t.Errorf("BaseUnits = %v, want 1 (non-JPY pair)", input.BaseUnits)
	}
	wantRate := 1 / 0.00069
	if !numbersMatch(input.Rate, wantRate) {
		t.Errorf("Rate = %v, want the reciprocal %v, not the raw sub-1 rate", input.Rate, wantRate)
	}
	wantSevenDaysAgo := 1 / 0.00068
	if !numbersMatch(input.SevenDaysAgoRate, wantSevenDaysAgo) {
		t.Errorf("SevenDaysAgoRate = %v, want the reciprocal %v", input.SevenDaysAgoRate, wantSevenDaysAgo)
	}
	// 추세(trend)의 함의는 표시 방향이 뒤바뀌더라도 여전히 원래의 To 통화
	// (exchange.To인 USD)를 설명해야 한다 — computeExchangeTrend는
	// BaseCurrency/QuoteCurrency에 대해 전혀 알지 못한다.
	if input.Trend == nil || input.Trend.Implication != "USD 약세" {
		t.Errorf("Trend = %+v, want implication describing USD (exchange.To), unaffected by the base/quote swap", input.Trend)
	}
}

// TestToBriefingExchangeInputJPYUsesHundredUnitConvention은 항목 3을
// 다룬다: JPY가 포함된 통화쌍은 from/to 중 어느 쪽에 있었든, 원래 환율이
// 1 이상이었든 상관없이 항상 BaseUnits=100과 BaseCurrency="JPY"를
// 보고해서 프롬프트가 "100 JPY당"으로 렌더링되도록 해야 한다.
func TestToBriefingExchangeInputJPYUsesHundredUnitConvention(t *testing.T) {
	exchange := &ExchangeData{
		From: "KRW", To: "JPY",
		Current: ExchangeRatePoint{Date: "2026-07-28", Rate: 0.1105}, // 1 KRW = 0.1105 JPY
	}

	input := toBriefingExchangeInput(exchange)

	if input.BaseCurrency != "JPY" || input.QuoteCurrency != "KRW" {
		t.Errorf("BaseCurrency/QuoteCurrency = %s/%s, want JPY/KRW", input.BaseCurrency, input.QuoteCurrency)
	}
	if input.BaseUnits != jpyDisplayUnits {
		t.Errorf("BaseUnits = %v, want %v", input.BaseUnits, jpyDisplayUnits)
	}
	wantRate := (1 / 0.1105) * jpyDisplayUnits
	if !numbersMatch(input.Rate, wantRate) {
		t.Errorf("Rate = %v, want %v (100 JPY worth of KRW)", input.Rate, wantRate)
	}
}

// TestValidateSectionOutputPrecedence는 generateSectionText의 단일 통합
// 검증 단계가 의존하는 고정된 검사 순서(CJK -> 영어 유출 -> 반복 구문 ->
// 근거 없는 숫자 -> 조작된 퍼센트 -> 근거 없는 고유명사 -> 반말/기사체
// 어미 -> 금칙 문구)를 고정해두고, 각 실패 유형에 대해 hardFailure/
// useFallback이 올바르게 설정되는지 확인한다 — 이 플래그들은 재시도를 모두
// 소진했을 때 에러(및 stale_fallback)로 처리할지, 환각 폴백 문자열로
// 대체할지, 아니면 조용히 "마지막 시도 결과를 그대로 내보낼지"를 결정한다.
func TestValidateSectionOutputPrecedence(t *testing.T) {
	cases := []struct {
		name         string
		text         string
		allowed      []float64
		grounding    string
		wantFailure  bool
		wantHard     bool
		wantFallback bool
		wantLenient  bool
	}{
		{
			name:        "clean text passes",
			text:        "환율은 1 USD당 1459.45 KRW입니다.",
			allowed:     []float64{1, 1459.45},
			wantFailure: false,
		},
		{
			name:        "CJK contamination is hard",
			text:        "환율은 1 USD当 1459.45 KRW입니다.",
			allowed:     []float64{1, 1459.45},
			wantFailure: true,
			wantHard:    true,
		},
		{
			name:        "leaked English is hard",
			text:        "The exchange rate is 1459.45 KRW today.",
			allowed:     []float64{1459.45},
			wantFailure: true,
			wantHard:    true,
		},
		{
			name:        "repeated phrase is hard",
			text:        "60.42%의 지분을 보유한 60.42%의 지분을 보유한 회사에 대한 지분을 매각했습니다.",
			allowed:     []float64{60.42},
			wantFailure: true,
			wantHard:    true,
		},
		{
			name:        "ungrounded number is hard",
			text:        "환율은 1 USD당 9999.99 KRW입니다.",
			allowed:     []float64{1, 1459.45},
			wantFailure: true,
			wantHard:    true,
		},
		{
			name:         "fabricated percentage is hard with fallback",
			text:         "Mercantile Bank Corporation은 60.42%의 지분을 보유한 회사라는 소식이 전해졌습니다.",
			allowed:      []float64{60.42},
			grounding:    "Mercantile Bank Corporation stock hits all-time high at 60.42 USD",
			wantFailure:  true,
			wantHard:     true,
			wantFallback: true,
		},
		{
			// 계약 상대방 날조는 lenientIfCoreNounSurvives가 항상 false다 —
			// "두산에너빌리티"라는 원문 핵심 개체 자체는 응답에 남아있지만,
			// 그와 거래했다는 "노블리스 오일 앤 가스"는 완전히 지어낸
			// 별개의 회사이므로 hasGroundedCoreProperNoun 완화 대상이 되면
			// 안 된다.
			name:         "fabricated contract counterparty is hard with fallback, not lenient",
			text:         "두산에너빌리티가 노블리스 오일 앤 가스와 계약을 체결했다는 소식이 전해졌습니다.",
			allowed:      []float64{},
			grounding:    "두산에너빌리티 원전·가스터빈 수주",
			wantFailure:  true,
			wantHard:     true,
			wantFallback: true,
			wantLenient:  false,
		},
		{
			// 실제 보고된 사례: 원문에 등장한 "Panthers"가 NFL 소속이라는
			// 것은 상식적인 보충 설명이지 지어낸 사실이 아닌데도, 원문에
			// literal한 "NFL"이 없다는 이유만으로 hallucination 취급됐다.
			// 계약 상대방 패턴이 아니라 일반 고유명사 루프에서 걸리므로
			// lenientIfCoreNounSurvives는 true여야 한다 — 실제 완화 여부는
			// hasGroundedCoreProperNoun(생성문에 "Panthers"가 남아있는지)이
			// generateSectionText 단계에서 추가로 판단한다.
			name:         "ungrounded league affiliation is hard with fallback, but lenient",
			text:         "Panthers가 NFL 소속 팀으로서 이번 시즌 좋은 성적을 거두고 있다는 소식이 전해졌습니다.",
			allowed:      []float64{},
			grounding:    "Panthers extend winning streak heading into playoffs",
			wantFailure:  true,
			wantHard:     true,
			wantFallback: true,
			wantLenient:  true,
		},
		{
			name:        "informal/기사체 sentence ending is hard",
			text:        "김정관 산업장관이 이번 조치에 반대한다고 밝혔다.",
			allowed:     []float64{},
			wantFailure: true,
			wantHard:    true,
		},
		{
			name:        "banned phrase is soft",
			text:        "이 주제에 대해 다양한 논의가 진행 중입니다.",
			allowed:     []float64{},
			wantFailure: true,
			wantHard:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, hard, fallback, lenient := validateSectionOutput(tc.text, tc.allowed, tc.grounding)
			if (reason != "") != tc.wantFailure {
				t.Fatalf("failure = %v (reason=%q), want %v", reason != "", reason, tc.wantFailure)
			}
			if !tc.wantFailure {
				return
			}
			if hard != tc.wantHard {
				t.Errorf("hardFailure = %v, want %v", hard, tc.wantHard)
			}
			if fallback != tc.wantFallback {
				t.Errorf("useFallback = %v, want %v", fallback, tc.wantFallback)
			}
			if lenient != tc.wantLenient {
				t.Errorf("lenientIfCoreNounSurvives = %v, want %v", lenient, tc.wantLenient)
			}
		})
	}
}

// briefingWeatherPromptTokenBudget/briefingExchangePromptTokenBudget은
// 날씨/환율 프롬프트(system+user)에 대한 같은 종류의 예산이다 — 뉴스처럼
// 요청마다 크기가 늘어나는 입력(헤드라인)은 없지만, 언젠가 누군가
// weatherSectionSystemPrompt/exchangeSectionSystemPrompt에 규칙을 하나 더
// 추가하는 순간 이 두 섹션도 똑같이 TPM 한도를 위협할 수 있다. 실측
// 총합(929/853토큰)에 여유를 둔 값이다.
const briefingWeatherPromptTokenBudget = 1200
const briefingExchangePromptTokenBudget = 1100

// TestWeatherBriefingPromptFitsWithinTokenBudget/
// TestExchangeBriefingPromptFitsWithinTokenBudget은 getBriefing이 실제로
// 만드는 것과 같은 형태의 대표 입력(오전/오후 예보, JPY 환율처럼 필드가
// 가장 많이 채워지는 경우)으로 프롬프트를 구성해 예산 내에 있는지 검증한다.
func TestWeatherBriefingPromptFitsWithinTokenBudget(t *testing.T) {
	input := &briefingWeatherInput{
		Current: briefingCurrentWeather{City: "seoul", CityLabel: "서울", TemperatureC: 23.4, WindSpeedKph: 12.3, WeatherCode: 1, Description: "맑음", ObservedAt: "2026-08-10T09:00:00+09:00"},
		Today: briefingDayForecast{
			Morning:   &briefingPeriodForecast{TemperatureC: 18.2, WeatherCode: 1, Description: "맑음", PrecipProbability: 10},
			Afternoon: &briefingPeriodForecast{TemperatureC: 27.9, WeatherCode: 2, Description: "구름 조금", PrecipProbability: 20},
		},
	}
	weatherJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal weather input: %v", err)
	}
	userContent := fmt.Sprintf("[날씨 데이터]: %s\n\n위 데이터를 바탕으로 한국어 날씨 브리핑 문장을 작성하세요.", weatherJSON)

	total := estimateTokenCount(weatherSectionSystemPrompt) + estimateTokenCount(userContent)
	if total > briefingWeatherPromptTokenBudget {
		t.Errorf("날씨 브리핑 프롬프트 추정 토큰 수 %d가 예산 %d를 초과했습니다 — weatherSectionSystemPrompt를 더 줄여야 합니다", total, briefingWeatherPromptTokenBudget)
	}
}

func TestExchangeBriefingPromptFitsWithinTokenBudget(t *testing.T) {
	input := &briefingExchangeInput{
		BaseCurrency: "JPY", QuoteCurrency: "KRW", BaseUnits: jpyDisplayUnits, Rate: 905.12, Date: "2026-08-10",
		SevenDaysAgoRate: 900.55, SevenDaysAgoDate: "2026-08-03", ChangePercent: 0.5,
		Trend: &exchangeTrend{Direction: "상승", Implication: "KRW 약세"},
	}
	exchangeJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal exchange input: %v", err)
	}
	userContent := fmt.Sprintf("[환율 데이터]: %s\n\n위 데이터를 바탕으로 한국어 환율 브리핑 문장을 작성하세요.", exchangeJSON)

	total := estimateTokenCount(exchangeSectionSystemPrompt) + estimateTokenCount(userContent)
	if total > briefingExchangePromptTokenBudget {
		t.Errorf("환율 브리핑 프롬프트 추정 토큰 수 %d가 예산 %d를 초과했습니다 — exchangeSectionSystemPrompt를 더 줄여야 합니다", total, briefingExchangePromptTokenBudget)
	}
}

// briefingNewsPromptTokenBudget는 뉴스 브리핑 프롬프트(system+user) 총합이
// 절대 넘지 말아야 할 근사 토큰 예산이다. 240자 description일 때 실측
// 6,148토큰, 그 뒤 존댓말 강화/환각 방지 지침이 하나둘 늘어난 뒤 다시
// 2,464토큰까지 커져 llama-3.1-8b-instant의 분당 한도(6,000 TPM)를 단일
// 요청만으로 위협하는 문제가 반복적으로 발생했다 — 이 값은 "다음에 규칙을
// 하나 더 추가했을 때 예산을 넘었는지"를 사람이 매번 로그를 보고 계산하지
// 않고도 CI/로컬 테스트에서 바로 잡아내기 위한 것이다. 의학/과학/법률
// 전문 용어의 한자 혼입을 막는 규칙(newsSectionSystemPrompt 5번, "belly
// size" → "배圍" 실제 사례)을 추가하며 1500에서 1650으로 올렸다.
//
// 그 뒤 실제 헤드라인 title이 "$100 million"의 " million" 부분만 잘려
// "$100…"으로 남는 사고(briefingNewsTitleMaxRunes 문서 참고)가 보고되어
// title 상한을 80에서 120으로 올리고(이 테스트의 인위적 최악 시나리오
// 기준 실측 1,793토큰), 원문이 말줄임표로 끝나 불완전할 때 숫자를
// 추측하지 말라는 규칙 6번을 newsSectionSystemPrompt에 추가하며(실측
// 1,902토큰) 1650에서 1950으로 함께 올렸다.
//
// 그 뒤 인도 지명("Ahmedabad")을 표기하려다 힌디어 데바나가리 문자가
// 그대로 노출된 사례가 보고되어, 인도·중동·동남아·러시아 등 비영어권
// 지명·인명에 그 지역 고유 문자를 섞지 말라는 규칙 7번을 추가하며(실측
// 2,113토큰) 1950에서 2150으로 함께 올렸다. 늘어난 뒤에도 6,000 TPM
// 한도까지는 여전히 3,800토큰 이상의 여유가 있다. 다음에 규칙이나 상한을
// 추가할 때는 이 값을 또 올리기보다, 정말 필요한지부터 검토해야 한다.
const briefingNewsPromptTokenBudget = 2150

// TestNewsBriefingPromptFitsWithinTokenBudget은 뉴스 브리핑 프롬프트가
// briefingNewsPromptTokenBudget을 넘지 않는지 검증한다. 헤드라인 3개
// 모두를 실제 자연스러운 한국어 문장(공백·문장부호 포함)을 반복해 만든
// 뒤 title/description 상한(briefingNewsTitleMaxRunes/
// briefingNewsDescriptionMaxRunes)에 맞춰 잘라 넣는다 — 모든 문자를
// 공백 없는 한글로만 채우는 인위적인 최악의 경우보다는, 실제로 들어올 수
// 있는 "상한까지 채워진 자연스러운 헤드라인 3개"에 가까운 시나리오를
// 기준으로 삼는다.
func TestNewsBriefingPromptFitsWithinTokenBudget(t *testing.T) {
	longSentence := strings.Repeat("이번 조사에 따르면 관련 업계 전반에 걸쳐 변화가 있었던 것으로 나타났다. ", 5)
	news := &NewsData{}
	for i := 0; i < briefingNewsHeadlineCount; i++ {
		news.Items = append(news.Items, NewsItem{ID: fmt.Sprintf("id%d", i), Title: longSentence, Description: longSentence})
	}
	newsInput := toBriefingNewsInput(news)
	newsJSON, err := json.Marshal(newsInput)
	if err != nil {
		t.Fatalf("marshal news input: %v", err)
	}
	userContent := fmt.Sprintf("[뉴스 데이터]: %s\n\n위 데이터를 바탕으로 한국어 뉴스 브리핑 문장을 작성하세요.", newsJSON)

	total := estimateTokenCount(newsSectionSystemPrompt) + estimateTokenCount(userContent)
	if total > briefingNewsPromptTokenBudget {
		t.Errorf("뉴스 브리핑 프롬프트 추정 토큰 수 %d가 예산 %d를 초과했습니다 — newsSectionSystemPrompt나 briefingNewsDescriptionMaxRunes/briefingNewsTitleMaxRunes를 더 줄여야 합니다", total, briefingNewsPromptTokenBudget)
	}
}

// TestNewsSectionSystemPromptBansCJKAsTopPriorityRule은 실제 보고된
// 재발 사례를 회귀 테스트로 고정한다: "domestic/international이 같은
// briefingCommonRules를 공유하니 CJK 금지 지침이 이미 적용돼 있다"고
// 판단했던 것과 별개로, 리팩터링 과정에서 이 지침 자체가 조용히
// 빠지거나 순서가 밀려날 위험은 항상 남아있다. 이 테스트는 (1) 지침이
// 실제로 존재하는지, (2) 다른 규칙보다 먼저(최우선 순위로) 오는지를
// 직접 문자열로 검증한다 — international 전용 프롬프트가 따로 없고
// newsSectionSystemPrompt 하나를 domestic/international이 그대로
// 공유하므로, 이 테스트 하나로 두 경로 모두를 커버한다.
func TestNewsSectionSystemPromptBansCJKAsTopPriorityRule(t *testing.T) {
	cjkRuleIdx := strings.Index(newsSectionSystemPrompt, "한자·중국어·일본어")
	if cjkRuleIdx == -1 {
		t.Fatal("expected newsSectionSystemPrompt to contain an explicit CJK-ban rule, found none")
	}

	informalToneRuleIdx := strings.Index(newsSectionSystemPrompt, "합니다체(존댓말)로")
	if informalToneRuleIdx == -1 {
		t.Fatal("sanity check failed: expected to find the honorific-tone rule for position comparison")
	}
	if cjkRuleIdx >= informalToneRuleIdx {
		t.Errorf("expected the CJK-ban rule to appear before other common rules (highest priority), but it came after the tone rule (cjk idx=%d, tone idx=%d)", cjkRuleIdx, informalToneRuleIdx)
	}

	// 입력(원문 헤드라인)에 영어와 이미 계산된 한글 숫자 표기가 섞여
	// 있어도 순수 한국어로 재구성하라는 지침이 명시적으로 있어야 한다 —
	// annotateNumericUnits가 "revenue of 6010만 달러 misses"처럼 원문
	// 자체를 의도적으로 영어+한글 혼종으로 만들어두기 때문에, 모델이 이
	// 혼종 입력을 그대로 베끼거나 착각해 한자로 새는 것을 막기 위함이다.
	if !strings.Contains(newsSectionSystemPrompt, "영어 문장과 이미 계산된 한글 숫자 표기가 섞여") {
		t.Error("expected an explicit instruction addressing mixed English/Korean-numeral input")
	}

	// domestic/international이 이 지침을 정말로 공유하는지 — 별도의
	// region 전용 프롬프트 상수가 있다면 이 회귀 자체가 재발할 수 있다.
	if weatherSectionSystemPrompt == newsSectionSystemPrompt || exchangeSectionSystemPrompt == newsSectionSystemPrompt {
		t.Fatal("sanity check failed: section prompts should be distinct from each other")
	}
	if !strings.HasPrefix(newsSectionSystemPrompt, briefingCommonRules) {
		t.Error("expected newsSectionSystemPrompt to start with the shared briefingCommonRules (the single source of the CJK-ban rule for both domestic and international)")
	}
}

// TestNewsSectionSystemPromptCoversTechnicalTermHanjaMixing은 실제 보고된
// 사례를 회귀 테스트로 고정한다: "belly size beats BMI at predicting heart
// attacks" 헤드라인을 다루다가 "배圍"(한글 "배" + 한자 "圍"가 섞인, 어느
// 언어에도 존재하지 않는 표현)가 생성됐다. 기존의 "일본어·중국어식 음차
// 금지" 규칙(4번)은 회사명 등 고유명사를 소리 나는 대로 옮기다 가나/한자가
// 섞이는 것을 막기 위한 규칙이라 이 실패를 커버하지 못했다 — 이번 실패는
// 고유명사가 아니라 "배 둘레"의 한자어 표현인 腹圍(복위)처럼, 흔히
// 한자로도 표기되는 일반/전문 용어를 무리하게 정확히 옮기려다 생긴
// 것이기 때문이다. findForeignScript가 사후에 이미 이런 출력을 걸러내고
// 있었지만(validateSectionOutput), 생성 단계에서부터 이런 시도 자체를
// 막고 실패 시 쉬운 말로 풀어 쓰도록 유도하는 전용 규칙이 있어야
// 재시도 후에도 같은 헤드라인이 다시 선택될 때 같은 실패가 반복되는
// 것을 줄일 수 있다.
func TestNewsSectionSystemPromptCoversTechnicalTermHanjaMixing(t *testing.T) {
	if !strings.Contains(newsSectionSystemPrompt, "전문 용어") {
		t.Fatal("expected newsSectionSystemPrompt to contain guidance about technical/professional terminology")
	}
	if !strings.Contains(newsSectionSystemPrompt, "belly size") {
		t.Error("expected the concrete regressed example (\"belly size\") to remain in the prompt as a guiding example")
	}
	if !strings.Contains(newsSectionSystemPrompt, "쉬운 말로 풀어") {
		t.Error("expected guidance to paraphrase into simpler wording when a term is hard to render in pure Hangul, not just a bare CJK prohibition")
	}
}

// TestNewsSectionSystemPromptCoversNonHangulScriptPlaceNames는 실제 보고된
// 사례를 회귀 테스트로 고정한다: 인도 도시 "Ahmedabad"를 "아마다바드"로
// 표기하려다 힌디어 데바나가리 문자(अहमदाबाद)가 그대로 노출됐다.
// findForeignScript가 사후에 이런 출력을 걸러내지만(검증만으로는 재시도
// 후에도 같은 헤드라인이 다시 선택되면 같은 실패가 반복될 수 있다),
// 생성 단계에서부터 인도·중동·동남아·러시아 등 비영어권 지명·인명에
// 그 지역 고유 문자를 섞지 말라는 전용 규칙이 있어야 한다.
func TestNewsSectionSystemPromptCoversNonHangulScriptPlaceNames(t *testing.T) {
	if !strings.Contains(newsSectionSystemPrompt, "데바나가리") {
		t.Fatal("expected newsSectionSystemPrompt to contain guidance about Devanagari/Hindi script")
	}
	if !strings.Contains(newsSectionSystemPrompt, "Ahmedabad") {
		t.Error("expected the concrete regressed example (\"Ahmedabad\") to remain in the prompt as a guiding example")
	}
	if !strings.Contains(newsSectionSystemPrompt, "अहमदाबाद") {
		t.Error("expected the concrete Devanagari counter-example to remain in the prompt so the model sees what NOT to output")
	}
}

// TestNewsSectionSystemPromptForbidsRedecomposingConvertedAmounts는 실제
// 보고된 사례를 회귀 테스트로 고정한다: annotateNumericUnits가 이미
// "5.4억 달러"로 정확히 환산해 넘겨줬는데도, 모델이 이를 "5억 400만
// 달러"(정답은 "5억 4000만"이어야 함)처럼 억/만 단위로 다시 쪼개
// 표현하려다 자릿수를 틀려 findUngroundedNumber에 근거 없는 숫자로
// 걸렸다("근거 없는 숫자 감지(5e+08)"). 실제 헤드라인으로 재현해보면
// (TestTruncateForPromptDoesNotDropAWordThatAlreadyFitsExactly가 고친
// 원인과는 별개로) 이 재분해 시도 자체가 여전히 발생할 수 있어, "주어진
// 표기를 그대로 쓰고 다시 쪼개 계산하지 말라"는 지침을 프롬프트에
// 명시적으로 추가했다.
func TestNewsSectionSystemPromptForbidsRedecomposingConvertedAmounts(t *testing.T) {
	if !strings.Contains(newsSectionSystemPrompt, "쪼개") {
		t.Error("expected newsSectionSystemPrompt to explicitly forbid re-decomposing an already-converted 억/만 amount")
	}
}

// TestNewsSectionSystemPromptForbidsGuessingIncompleteTruncatedInput은
// 새로 추가된 규칙 6번을 회귀 테스트로 고정한다: title/description이
// 말줄임표(…)로 끝나 불완전할 때, 모델이 그 안의 숫자·세부 정보를
// 추측해서 채우면 안 된다는 지침이 프롬프트에 명시적으로 있어야 한다 —
// truncateForPrompt의 extendCutToPreserveNumericToken이 숫자 표현
// 자체는 최대한 보존하더라도, description처럼 원래도 잘리는 것을
// 전제로 하는 필드는 여전히 문장 중간에서 끊길 수 있으므로, 검증기
// (findUngroundedNumber)만으로는 못 막는 다른 종류의 추측(숫자가 아닌
// 세부 사실)까지 이 지침이 예방한다.
func TestNewsSectionSystemPromptForbidsGuessingIncompleteTruncatedInput(t *testing.T) {
	if !strings.Contains(newsSectionSystemPrompt, "말줄임표") {
		t.Fatal("expected newsSectionSystemPrompt to mention the ellipsis marker used by truncateForPrompt")
	}
	if !strings.Contains(newsSectionSystemPrompt, "추측") {
		t.Error("expected newsSectionSystemPrompt to explicitly forbid guessing at incomplete/truncated details")
	}
}
