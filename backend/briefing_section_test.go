package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	if err := upsertBriefingSectionCache(context.Background(), nil, "weather", "hash", "text", time.Now()); err != nil {
		t.Errorf("expected upsert against a nil db to no-op without error, got %v", err)
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

func TestFindForeignCJK(t *testing.T) {
	if _, found := findForeignCJK("대구는 오늘 대체로 맑아 우산 없이 외출하기 좋은 날씨입니다."); found {
		t.Error("expected no false positive on ordinary Hangul text")
	}
	if match, found := findForeignCJK("这是中文字符가 섞인 문장입니다."); !found || match == "" {
		t.Error("expected Chinese Han characters to be detected")
	}
	if _, found := findForeignCJK("これは日本語です가 섞인 문장"); !found {
		t.Error("expected Japanese kana to be detected")
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
	if match, found := findUngroundedProperNoun(hallucinated, grounding); !found {
		t.Error("expected the fabricated '노블리스 오일 앤 가스' counterparty to be flagged as ungrounded")
	} else {
		t.Logf("correctly flagged fabricated proper noun: %q", match)
	}

	faithful := "두산에너빌리티가 국내외에서 원전과 가스터빈 수주를 잇따라 확보했다고 밝혔습니다."
	if match, found := findUngroundedProperNoun(faithful, grounding); found {
		t.Errorf("expected no ungrounded proper noun in a faithful sentence, got %q", match)
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

	if reason, _, _ := validateSectionOutput(simple, []float64{1, 1452.35}, ""); reason != "" {
		t.Errorf("simple alone should not fail validation, got %q", reason)
	}
	if reason, _, _ := validateSectionOutput(detailed, []float64{1, 1452.35, 7, 1.4}, ""); reason != "" {
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
			_, found := findUngroundedProperNoun(tc.text, g)
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
	if _, found := findUngroundedNumber(hallucinated, allowed); !found {
		t.Error("expected the hallucinated 1200만 달러 figure to be flagged as ungrounded")
	}

	faithful := "90억 오픈소스 모델에 500달러를 투입해 파인튜닝한 결과, 최상위 모델을 제쳤습니다."
	if num, found := findUngroundedNumber(faithful, allowed); found {
		t.Errorf("expected no ungrounded number in a faithful sentence, got %v", num)
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
	if num, found := findUngroundedNumber(sentence, allowedWeatherNumbers(input)); found {
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
	if num, found := findUngroundedNumber(sentence, allowedExchangeNumbers(input)); found {
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
			name:         "ungrounded proper noun is hard with fallback",
			text:         "두산에너빌리티가 노블리스 오일 앤 가스와 계약을 체결했다는 소식이 전해졌습니다.",
			allowed:      []float64{},
			grounding:    "두산에너빌리티 원전·가스터빈 수주",
			wantFailure:  true,
			wantHard:     true,
			wantFallback: true,
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
			reason, hard, fallback := validateSectionOutput(tc.text, tc.allowed, tc.grounding)
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
		})
	}
}

// briefingNewsPromptTokenBudget는 뉴스 브리핑 프롬프트(system+user) 총합이
// 절대 넘지 말아야 할 근사 토큰 예산이다. 240자 description일 때 실측
// 6,148토큰, 그 뒤 존댓말 강화/환각 방지 지침이 하나둘 늘어난 뒤 다시
// 2,464토큰까지 커져 llama-3.1-8b-instant의 분당 한도(6,000 TPM)를 단일
// 요청만으로 위협하는 문제가 반복적으로 발생했다 — 이 값은 "다음에 규칙을
// 하나 더 추가했을 때 예산을 넘었는지"를 사람이 매번 로그를 보고 계산하지
// 않고도 CI/로컬 테스트에서 바로 잡아내기 위한 것이다.
const briefingNewsPromptTokenBudget = 1500

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
