package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"
)

const (
	newsDataIOEndpoint = "https://newsdata.io/api/1/latest"
	newsItemCount      = 5

	newsRegionDomestic      = "domestic"
	newsRegionInternational = "international"
	defaultNewsRegion       = newsRegionDomestic

	defaultNewsCategory = "top"
)

// newsCategories는 UI에 노출되는 카테고리를 해당 한국어 pill 라벨에 매핑한 것이다.
// 여기 있는 값들은 모두 NewsData.io의 /api/1/latest 카테고리 값으로 지원이
// 확인된 것들이다 (실제 API로 검증함).
var newsCategories = map[string]string{
	"top":           "주요",
	"business":      "경제",
	"technology":    "기술",
	"sports":        "스포츠",
	"entertainment": "연예",
	"health":        "건강",
	"science":       "과학",
}

var errNewsDataAPIKeyMissing = errors.New("NEWSDATA_API_KEY not set")

func normalizeNewsCategory(category string) string {
	if _, ok := newsCategories[category]; ok {
		return category
	}
	return defaultNewsCategory
}

func normalizeNewsRegion(region string) string {
	if region == newsRegionInternational {
		return newsRegionInternational
	}
	return newsRegionDomestic
}

// newsDataIOResponse는 "results" 필드의 파싱을 나중으로 미룬다. 이 필드의
// 형태가 "status" 값에 따라 달라지기 때문이다: 성공 시에는 기사 배열이지만,
// 실패 시에는 (잘못된 키, 지원하지 않는 카테고리 등) {message, code} 형태의
// 객체가 온다 — 하나의 구조체로 두 형태를 동시에 표현할 수 없다.
type newsDataIOResponse struct {
	Status  string          `json:"status"`
	Results json.RawMessage `json:"results"`
}

type newsDataIOArticle struct {
	ArticleID   string  `json:"article_id"`
	Title       string  `json:"title"`
	Link        string  `json:"link"`
	Description *string `json:"description"`
	SourceID    string  `json:"source_id"`
	SourceName  string  `json:"source_name"`
	PubDate     string  `json:"pubDate"`
}

type newsDataIOErrorBody struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// fetchNewsDataIO는 하나의 카테고리와 하나의 region에 대해 NewsData.io의
// /latest 엔드포인트를 호출한다. 국내(domestic) 요청은 country=kr&language=ko로
// 한국어 기사를 직접 요청하고, 해외(international) 요청은 country 필터 없이
// 영어 기사를 요청한다 — NewsData.io 무료 요금제는 여러 국가를 동시에 지정하는
// 것을 지원하지 않을 수 있어서, 어떤 국가가 허용되는지 추측하는 것보다 country
// 필터를 아예 걸지 않는 쪽이 폭넓은 "international" 피드를 얻는 더 안전한
// 방법이다.
func fetchNewsDataIO(ctx context.Context, category, region string) (*NewsData, error) {
	apiKey := os.Getenv("NEWSDATA_API_KEY")
	if apiKey == "" {
		return nil, errNewsDataAPIKeyMissing
	}

	params := url.Values{}
	params.Set("apikey", apiKey)
	params.Set("category", category)
	if region == newsRegionInternational {
		params.Set("language", "en")
	} else {
		params.Set("country", "kr")
		params.Set("language", "ko")
	}

	endpoint := newsDataIOEndpoint + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	recordNewsDataIOCall()
	log.Printf("[NewsData.io 호출] category=%s region=%s (오늘 %d회째)", category, region, newsDataIOUsageCount())

	callStart := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// resp가 nil이라 응답 헤더 자체가 없다 — 크레딧/한도 문제라면 보통
		// 즉시 4xx로 거절되지, 이렇게 응답 자체를 못 받고 "context deadline
		// exceeded"로 실패하지는 않는다. 걸린 시간을 함께 남겨서, 실제로
		// sectionTimeout(호출부 참고) 한도까지 꽉 채우고 끊긴 것인지(=
		// NewsData.io가 느렸거나 네트워크 경로가 막혔던 것) 아니면 그보다
		// 훨씬 이르게 실패했는지(연결 자체가 즉시 거부된 것) 구분할 수 있게
		// 한다.
		log.Printf("[NewsData.io 호출] category=%s region=%s 실패(%s 경과): %v", category, region, time.Since(callStart).Round(time.Millisecond), err)
		return nil, err
	}
	defer resp.Body.Close()
	logNewsDataIORateLimitHeaders(resp.Header)

	var parsed newsDataIOResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to parse newsdata.io response (http %d): %w", resp.StatusCode, err)
	}

	if parsed.Status != "success" {
		var apiErr newsDataIOErrorBody
		if jsonErr := json.Unmarshal(parsed.Results, &apiErr); jsonErr == nil && apiErr.Message != "" {
			return nil, fmt.Errorf("newsdata.io error (http %d): %s", resp.StatusCode, apiErr.Message)
		}
		return nil, fmt.Errorf("newsdata.io returned status %q (http %d)", parsed.Status, resp.StatusCode)
	}

	var articles []newsDataIOArticle
	if err := json.Unmarshal(parsed.Results, &articles); err != nil {
		return nil, fmt.Errorf("failed to parse newsdata.io results: %w", err)
	}

	if len(articles) > newsItemCount {
		articles = articles[:newsItemCount]
	}

	items := make([]NewsItem, len(articles))
	for i, a := range articles {
		// source_name은 사람이 읽기 좋은 매체명이고 (예: "Investing",
		// "뉴스1"), source_id는 URL에 쓸 수 있는 슬러그 형태다
		// (예: "investing_kr") — source_name이 비어있을 때만
		// 대체용으로 사용한다.
		sourceName := a.SourceName
		if sourceName == "" {
			sourceName = a.SourceID
		}
		var description string
		if a.Description != nil {
			description = *a.Description
		}
		items[i] = NewsItem{
			ID: a.ArticleID,
			// NewsData.io가 한글 텍스트를 완성형(NFC)이 아니라 조합형
			// (NFD, 예: "계룡건설" 대신 낱자모로 분해된 형태)으로 내려주는
			// 기사가 실제로 관측됐다 — [가-힣] 범위만 매칭하는
			// findForeignCJK/findUngroundedProperNoun/findTopicMismatch
			// 등 한글 전용 정규식들이 그런 텍스트에서는 한글을 아예
			// "한글이 아닌 것"처럼 인식하지 못한다(예: "1000","14","CEO",
			// "SR" 같은 비한글 토큰만 남고 회사명/사건명이 통째로 사라짐).
			// 여기서 한 번만 NFC로 정규화해두면, 이후 모든 소비처(브리핑
			// 프롬프트, 번역, hallucination 검사, 뉴스 카드 표시)가 이
			// 문제를 신경 쓸 필요가 없다.
			Title:       norm.NFC.String(a.Title),
			Link:        a.Link,
			SourceName:  norm.NFC.String(sourceName),
			PubDate:     a.PubDate,
			Description: norm.NFC.String(description),
		}
	}

	data := &NewsData{Items: items, Category: category, Region: region}

	if region == newsRegionInternational {
		// 번역은 best-effort로 처리한다: 번역에 실패해도 뉴스 섹션 전체를
		// 실패시키지 않고 해당 항목의 TranslatedTitle만 빈 값으로 남겨둔다 —
		// 국내 기사는 이미 한국어라 이 과정을 아예 건너뛴다.
		translateNewsItems(ctx, data.Items)
	}

	return data, nil
}

// newsRawCacheTTL은 동일한 category+region 조합의 raw_data_cache 항목(과 그
// 번역)을 일정 시간 동안 재사용할 수 있게 한다. 이렇게 하는 이유는
// GET /api/news와 AI 브리핑 내부의 뉴스 fetch(dashboardHandler — 브리핑은
// 기사 내용이 필요하지만 이를 직접 노출하지는 않는다)가 사용자가 페이지를
// 로드할 때마다 동일한 요청에 대해 각각 NewsData.io credit을 소모하지
// 않도록 하기 위해서다: NewsData.io 무료 요금제는 하루 200 credit뿐이다.
// 30분(초기값 5분에서 늘림)으로 정한 것은 뉴스 헤드라인이 그보다 자주
// 확인해야 할 만큼 빠르게 바뀌지 않기 때문이며, 이렇게 하면 실제로 서로
// 다른 category/region 조합을 위해 쓸 수 있는 하루 200-credit 예산을 훨씬
// 더 넉넉하게 남겨둘 수 있다. 예전에는 프로세스 메모리에만 있었지만,
// 이제는 raw_data_cache 테이블(raw_data_cache.go)에 저장되어 서버가
// 재시작돼도 그대로 남아있다.
const newsRawCacheTTL = 30 * time.Minute

func newsFetchCacheKey(category, region string) string {
	return "news:" + region + ":" + category
}

// newsDataIOQuotaThreshold는 newsDataIOUsage가 무료 요금제의 하루 200-credit
// 한도에 얼마나 가까워지면 getCachedOrFetchNews가 새로 credit을 쓰는 것을
// 멈추고 (아무리 오래됐더라도) 캐시된 값을 대신 서빙하기 시작할지를 정한다 —
// 그날 남은 시간 동안 피할 수 없는 호출(오늘 아직 한 번도 조회되지 않은
// 카테고리, 아직 채워지지 않은 캐시 등)을 위해 20 credit의 여유를 남겨두려는
// 것이지, 계정의 credit을 완전히 바닥낼 생각은 없다.
const newsDataIOQuotaThreshold = 180

// newsDataIOUsage는 현재 KST 기준 하루 동안의 실제 NewsData.io 호출 수를
// 추적한다 — groq.go의 groqUsage와 동일한 방식(날짜가 바뀌면 리셋하는
// day-keyed 방식)을 그대로 따르되, 자체적인 daily quota를 가진 다른
// 업스트림 API를 대상으로 한다는 점만 다르다.
var newsDataIOUsage = struct {
	mu    sync.Mutex
	day   string
	count int
}{}

func recordNewsDataIOCall() {
	newsDataIOUsage.mu.Lock()
	defer newsDataIOUsage.mu.Unlock()
	today := time.Now().In(kst).Format("2006-01-02")
	if newsDataIOUsage.day != today {
		newsDataIOUsage.day = today
		newsDataIOUsage.count = 0
	}
	newsDataIOUsage.count++
}

// logNewsDataIORateLimitHeaders는 NewsData.io 응답에 실제로 담겨 오는
// 한도 관련 헤더를 로그로 남긴다 — 실측(curl로 직접 호출) 결과 확인된
// 헤더 이름을 그대로 사용한다: X-RateLimit-Limit/-Remaining/-Reset은
// 15분 단위 요청 속도 제한이고, X-API-Limit-Remaining은 하루 200-credit
// 요금제 기준 오늘 남은 크레딧이다(newsDataIOUsage의 자체 카운터는 이
// 프로세스가 직접 호출한 횟수만 세므로, 여러 인스턴스가 같은 키를
// 공유하거나 프로세스가 재시작되면 실제 값과 어긋날 수 있다 — 이 헤더가
// NewsData.io 스스로 집계한 진짜 값이라 교차 검증에 쓸 수 있다).
// Retry-After는 문서상 429(Rate Limit Exceeded) 응답에 실린다고 되어
// 있지만 실측 결과 200 응답에도 항상 실려 있었다(다음 15분 창이 초기화될
// 때까지 남은 초) — 값이 비어있지 않으면 그대로 남긴다. 어느 헤더든
// 없으면(다른 요금제거나 NewsData.io가 표기를 바꾸면) 빈 문자열이
// 반환되므로 orDash로 "-"를 대신 찍는다.
func logNewsDataIORateLimitHeaders(header http.Header) {
	rateLimit := header.Get("X-RateLimit-Limit")
	rateRemaining := header.Get("X-RateLimit-Remaining")
	rateReset := header.Get("X-RateLimit-Reset")
	dailyRemaining := header.Get("X-API-Limit-Remaining")
	retryAfter := header.Get("Retry-After")

	if rateLimit == "" && rateRemaining == "" && dailyRemaining == "" {
		return
	}

	log.Printf("[NewsData.io 호출] 남은 한도 — 15분당 요청: %s/%s(reset=%s), 오늘 남은 크레딧: %s, Retry-After: %s",
		orDash(rateRemaining), orDash(rateLimit), orDash(rateReset), orDash(dailyRemaining), orDash(retryAfter))
}

func newsDataIOUsageCount() int {
	newsDataIOUsage.mu.Lock()
	defer newsDataIOUsage.mu.Unlock()
	today := time.Now().In(kst).Format("2006-01-02")
	if newsDataIOUsage.day != today {
		return 0
	}
	return newsDataIOUsage.count
}

// newsDataIOQuotaNotice는 getCachedOrFetchNews가 캐시가 아직 fresh해서가
// 아니라 하루 credit 예산을 보호하기 위해 일부러 오래된 캐시를 서빙했을 때
// 반환하는 안내 문구다 — 프론트엔드는 이를 작은 안내 배지로 보여줘서, 왜
// 갱신되지 않는지 설명 없이 오래된 뉴스를 조용히 보여주는 일이 없게 한다.
const newsDataIOQuotaNotice = "오늘 뉴스 조회 한도에 근접해 갱신을 잠시 제한합니다"

// newsShouldServeStaleForQuota는 오늘 NewsData.io 사용량이 한도에
// 근접했을 때, 캐시가 만료됐더라도 재조회를 건너뛰고 그 캐시를 그대로
// 서빙해야 하는지를 판단하는 순수 함수다 — DB 접근이 없어 단위 테스트가
// 쉽다. found가 false면(대체할 캐시 자체가 없으면) 쿼터와 무관하게 항상
// false다 — 되돌아갈 곳이 없다면 어차피 새로 조회하는 수밖에 없다.
func newsShouldServeStaleForQuota(found bool, todayCount int) bool {
	return found && todayCount >= newsDataIOQuotaThreshold
}

// newsFetchCall은 coalesceNewsFetch가 key(category+region)별로 관리하는
// 진행 중인 조회 하나를 나타낸다 — done이 닫히면 result가 확정된 것이다.
type newsFetchCall struct {
	done   chan struct{}
	result struct {
		data   *NewsData
		notice string
		err    error
	}
}

// newsFetchCoordinator는 key별로 진행 중인 getCachedOrFetchNews 호출을
// 추적한다 — coalesceNewsFetch의 문서 주석 참고.
var newsFetchCoordinator = struct {
	mu       sync.Mutex
	inFlight map[string]*newsFetchCall
}{inFlight: make(map[string]*newsFetchCall)}

// coalesceNewsFetch는 같은 key(category+region)에 대한 동시 호출들을
// 하나로 합친다. /api/news(news_handler.go)와 대시보드의 브리핑용 내부
// 조회(handler.go)는 서로 완전히 독립된 HTTP 요청이라서, 프론트엔드가
// 페이지를 열 때 둘 다 거의 동시에 들어온다 — raw_data_cache에 아직
// 신선한 값이 없는 순간(예: 캐시가 막 만료된 직후)에 둘 다 도착하면,
// 각자 독립적으로 lookupRawDataCache에서 "없음"을 보고 NewsData.io를
// 중복 호출해버리는 경쟁 상태(cache stampede)가 실제로 있었다 — 같은
// category+region 조합이 30분 TTL 안인데도 20초 사이에 두 번 호출되는
// 형태로 관측됨. 같은 key로 먼저 들어온 호출이 아직 진행 중이면, 뒤이어
// 들어온 호출은 새로 조회를 시작하는 대신 그 결과를 기다렸다가 그대로
// 공유한다.
func coalesceNewsFetch(key string, fn func() (*NewsData, string, error)) (*NewsData, string, error) {
	newsFetchCoordinator.mu.Lock()
	if call, ok := newsFetchCoordinator.inFlight[key]; ok {
		newsFetchCoordinator.mu.Unlock()
		log.Printf("뉴스(%s): 동시에 들어온 다른 요청이 이미 조회 중 — 그 결과를 기다립니다 (중복 호출 방지)", key)
		<-call.done
		return call.result.data, call.result.notice, call.result.err
	}

	call := &newsFetchCall{done: make(chan struct{})}
	newsFetchCoordinator.inFlight[key] = call
	newsFetchCoordinator.mu.Unlock()

	defer func() {
		newsFetchCoordinator.mu.Lock()
		delete(newsFetchCoordinator.inFlight, key)
		newsFetchCoordinator.mu.Unlock()
		close(call.done)
	}()

	call.result.data, call.result.notice, call.result.err = fn()
	return call.result.data, call.result.notice, call.result.err
}

// getCachedOrFetchNews는 /api/news와 브리핑이 공통으로 호출해야 하는
// 단일 진입점이다 — 최근에 가져온 결과를 투명하게 재사용해서 NewsData.io를
// 다시 호출하지 않도록 한다. 오늘 사용량이 무료 요금제의 credit 한도에
// 가까워지면, 정상적인 TTL 범위를 벗어났더라도 credit을 추가로 쓰는 대신
// (존재한다면) 오래된 캐시 항목을 그대로 서빙한다.
//
// weather.go/exchange.go의 getCachedOrFetchWeather/Exchange와 달리
// fetchWithRawCache(raw_data_cache.go)를 그대로 재사용하지 않고 캐시
// 조회/저장 로직을 직접 푼 이유는, 뉴스에만 있는 "오늘 크레딧 사용량이
// 한도에 근접하면 만료된 캐시라도 재조회 자체를 시도하지 않고 미리
// 서빙한다"는 세 번째 분기 때문이다 — 이 분기는 fetchFn을 호출하기도
// 전에 끼어들어야 해서, 공통 헬퍼의 "fetchFn 실패 시에만 캐시로 폴백"
// 흐름에 깔끔하게 들어맞지 않는다.
func getCachedOrFetchNews(ctx context.Context, category, region string) (data *NewsData, notice string, err error) {
	key := newsFetchCacheKey(category, region)
	return coalesceNewsFetch(key, func() (*NewsData, string, error) {
		return fetchNewsWithCache(ctx, key, category, region)
	})
}

func fetchNewsWithCache(ctx context.Context, key, category, region string) (data *NewsData, notice string, err error) {
	row, found := lookupRawDataCache(ctx, db, key)
	now := time.Now()

	if found && isRawCacheFresh(row, now) {
		var cached NewsData
		if jsonErr := json.Unmarshal([]byte(row.dataJSON), &cached); jsonErr == nil {
			log.Printf("뉴스(%s): %s까지 유효 (NewsData.io 미호출)", key, row.expiresAt.Format("15:04:05"))
			return &cached, "", nil
		}
		log.Printf("뉴스(%s): 저장된 JSON 파싱 실패, 무시하고 새로 가져옵니다", key)
		found = false
	}

	if newsShouldServeStaleForQuota(found, newsDataIOUsageCount()) {
		var stale NewsData
		if jsonErr := json.Unmarshal([]byte(row.dataJSON), &stale); jsonErr == nil {
			log.Printf("뉴스(%s): 오늘 NewsData.io 사용량(%d)이 한도에 근접, 만료된 캐시를 그대로 서빙 (신규 호출 생략)", key, newsDataIOUsageCount())
			return &stale, newsDataIOQuotaNotice, nil
		}
	}

	fetched, fetchErr := fetchNewsDataIO(ctx, category, region)
	if fetchErr != nil {
		if found {
			var stale NewsData
			if jsonErr := json.Unmarshal([]byte(row.dataJSON), &stale); jsonErr == nil {
				log.Printf("뉴스(%s): NewsData.io 호출 실패(%v) — %s에 가져온 만료 캐시를 잠정치로 사용", key, fetchErr, row.fetchedAt.Format("2006-01-02 15:04:05"))
				return &stale, "", nil
			}
		}
		return nil, "", fetchErr
	}

	encoded, marshalErr := json.Marshal(fetched)
	if marshalErr != nil {
		log.Printf("뉴스(%s): 직렬화 실패, 캐시 저장은 생략하고 방금 가져온 값을 그대로 반환합니다: %v", key, marshalErr)
		return fetched, "", nil
	}
	// rawCacheUpsertTimeout 문서 주석 참고 — 호출자의 요청 스코프 ctx가 아니라
	// 독립적인 컨텍스트를 써야, NewsData.io 호출이 요청 타임아웃 예산을 거의
	// 다 쓰고 나서야 성공한 경우에도 저장 자체가 취소되지 않는다.
	insertCtx, cancel := context.WithTimeout(context.Background(), rawCacheUpsertTimeout)
	defer cancel()
	if upsertErr := upsertRawDataCache(insertCtx, db, key, string(encoded), now, now.Add(newsRawCacheTTL)); upsertErr != nil {
		log.Printf("뉴스(%s): DB 저장 실패: %v", key, upsertErr)
	}

	return fetched, "", nil
}
