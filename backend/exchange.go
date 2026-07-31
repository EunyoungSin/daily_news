package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

const (
	defaultFromCurrency = "USD"
	defaultToCurrency   = "KRW"
)

type frankfurterResponse struct {
	Amount float64            `json:"amount"`
	Base   string             `json:"base"`
	Date   string             `json:"date"`
	Rates  map[string]float64 `json:"rates"`
}

// fetchFrankfurterRate는 datePath가 "latest"면 Frankfurter의 "latest"
// 엔드포인트를, 아니면 특정 과거 날짜("2006-01-02" 형식) 엔드포인트를
// 호출한다. 해당 날짜에 환율이 없으면(주말/공휴일) Frankfurter가 알아서
// 가장 가까운 이전 영업일 값으로 대체해 주므로, 호출하는 쪽에서 따로
// 신경 쓸 필요가 없다.
func fetchFrankfurterRate(ctx context.Context, datePath, from, to string) (frankfurterResponse, error) {
	endpoint := fmt.Sprintf(
		"https://api.frankfurter.app/%s?from=%s&to=%s",
		datePath, url.QueryEscape(from), url.QueryEscape(to),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return frankfurterResponse{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return frankfurterResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return frankfurterResponse{}, fmt.Errorf("frankfurter returned status %d for %s", resp.StatusCode, datePath)
	}

	var parsed frankfurterResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return frankfurterResponse{}, err
	}
	return parsed, nil
}

// frankfurterRangeResponse는 start..end 기간 조회에 대한 Frankfurter의
// 응답 형태다 — 단일 날짜/latest 엔드포인트와 달리, 여기서는 `rates`가
// 먼저 날짜로, 그다음 통화 코드로 키가 매겨진다.
type frankfurterRangeResponse struct {
	Amount    float64                       `json:"amount"`
	Base      string                        `json:"base"`
	StartDate string                        `json:"start_date"`
	EndDate   string                        `json:"end_date"`
	Rates     map[string]map[string]float64 `json:"rates"`
}

func fetchFrankfurterRange(ctx context.Context, startDate, endDate, from, to string) (frankfurterRangeResponse, error) {
	log.Printf("환율(%s->%s): 7일 추이 조회 시작 (%s..%s)", from, to, startDate, endDate)

	endpoint := fmt.Sprintf(
		"https://api.frankfurter.app/%s..%s?from=%s&to=%s",
		startDate, endDate, url.QueryEscape(from), url.QueryEscape(to),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return frankfurterRangeResponse{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("환율(%s->%s): 7일 추이 조회 실패(요청 자체가 실패): %v", from, to, err)
		return frankfurterRangeResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("환율(%s->%s): 7일 추이 조회 실패, http %d", from, to, resp.StatusCode)
		return frankfurterRangeResponse{}, fmt.Errorf("frankfurter range returned status %d for %s..%s", resp.StatusCode, startDate, endDate)
	}

	var parsed frankfurterRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		log.Printf("환율(%s->%s): 7일 추이 응답 파싱 실패: %v", from, to, err)
		return frankfurterRangeResponse{}, err
	}
	log.Printf("환율(%s->%s): 7일 추이 응답 파싱 완료: %d개 날짜", from, to, len(parsed.Rates))
	return parsed, nil
}

// computeChangePercent는 exchange 섹션에서 두 시점 간 변화율을 계산하는
// 유일한 공통 함수다 — Yesterday의 전일 대비 수치와 브리핑의 주간 추세
// (briefing.go의 computeExchangeTrend 참고) 모두 이 함수를 호출하므로,
// 두 곳에 따로 반올림 로직이 있어서 값이 어긋나는 일이 없다.
func computeChangePercent(oldRate, newRate float64) float64 {
	if oldRate == 0 {
		return 0
	}
	return math.Round((newRate-oldRate)/oldRate*1000) / 10
}

// exchangeIsInverted는 "이 원본 rate가 그대로 신뢰/표시하기엔 너무
// 작은가"를 판단하는 유일한 공통 규칙이다: 1 미만인 rate(예:
// KRW->USD의 ~0.00069)는 소수점을 몇 자리 보여주든 "0.00"으로 보일 뿐
// 아니라 — exchangeRateCorrection의 문서 주석대로 — Frankfurter 응답
// 자체가 값의 정밀도 대부분을 반올림으로 날려버렸다는 신호이기도 하다.
// 응답을 어느 방향으로 보정/표시해야 하는지 알아야 하는 모든 곳은 이
// 조건을 직접 재구현하지 않고 반드시 이 함수를 호출하므로, 서로 다른
// 판단을 내릴 수가 없다.
func exchangeIsInverted(rate float64) bool {
	return rate > 0 && rate < 1
}

const (
	jpyCurrencyCode = "JPY"
	jpyDisplayUnits = 100
	krwCurrencyCode = "KRW"
)

// exchangeDisplayPlan은 응답 하나당 (From/To 기준으로 — planExchangeDisplay
// 참고) 딱 한 번만 결정되고, 그 응답 안의 모든 지점(current, weekly,
// yesterday)에 displayRateFor를 통해 동일하게 적용된다. 그래서 헤드라인
// 숫자, "어제", 차트가 어느 통화를 "1 단위"(JPY는 "100 단위")로 볼지에
// 대해 서로 다르게 판단하는 일이 없다.
type exchangeDisplayPlan struct {
	BaseCurrency  string
	QuoteCurrency string
	// BaseUnits는 표시 기준이 BaseCurrency 몇 단위당인지를 나타낸다 —
	// 보통은 1, JPY는 100 (국제 외환 관행: 1 JPY는 그 자체로는 의미 있는
	// "1 단위" 기준이 되기엔 너무 작은 값이라 100 단위 기준으로 표기한다).
	BaseUnits float64
	Label     string
}

// planExchangeDisplay는 표시 목적상 어느 통화를 "기준"으로 삼을지
// 결정한다. 고정된 방향이 있는 건 KRW뿐이다: KRW가 관련된 경우에는
// 사용자가 KRW를 어느 쪽에 선택했는지와 무관하게 항상 상대 통화가
// 기준이 된다("1 USD = ... KRW"이지 "1 KRW = ... USD"가 되는 일은
// 없다) — 그래서 KRW->USD 요청과 USD->KRW 요청이 동일하게 표시된다.
// KRW가 관련되지 않은 조합은 강제 역전 없이 사용자가 선택한 그대로
// 보여준다 — "1 USD = 148.32 JPY"는 그대로 "1 USD = ..."이며, JPY 가치가
// 낮다고 해서 "100 JPY = ..."로 뒤집히지 않는다. JPY의 100단위 표기
// 관행은 위 두 규칙 중 하나로 JPY가 결국 기준 통화가 될 때만
// 적용된다(사용자가 `from`으로 JPY를 선택했거나, KRW 관련 규칙에 의해
// 그 자리로 옮겨진 경우) — KRW가 없는 조합에서 JPY가 단순히 상대(quote)
// 쪽인 경우에는 절대 적용되지 않는다. rate 매개변수는 여기서 쓰이지
// 않는다(기존 호출부와의 시그니처 안정성을 위해 남겨둔 것) — 1 미만인
// 손실 있는 원본 rate가 이 함수가 호출되기 전에 어떻게 보정되는지는
// exchangeRateCorrection 참고. 그 보정은 어떤 통화를 표시 기준으로
// 삼을지와는 완전히 별개의, 직교하는 관심사다.
func planExchangeDisplay(rate float64, from, to string) exchangeDisplayPlan {
	baseCurrency, quoteCurrency := from, to
	if from == krwCurrencyCode && to != krwCurrencyCode {
		baseCurrency, quoteCurrency = to, from
	}

	baseUnits := 1.0
	if baseCurrency == jpyCurrencyCode {
		baseUnits = jpyDisplayUnits
	}

	return exchangeDisplayPlan{
		BaseCurrency:  baseCurrency,
		QuoteCurrency: quoteCurrency,
		BaseUnits:     baseUnits,
		Label:         fmt.Sprintf("%d %s = ", int(baseUnits), baseCurrency),
	}
}

// displayRateFor는 "1 originalFrom = rateFromTo originalTo" 형태의 관측값
// 하나를 이 plan의 표시용 값으로 변환한다. originalFrom은 반드시 이 plan을
// 만들 때 쓴 `from` 통화(exchangeData.From)와 같아야 한다 — 응답 안의 모든
// 지점은 날짜만 다를 뿐 동일한 from/to 쌍을 공유하므로, 호출하는 쪽은 항상
// 응답의 고정된 From 값을 그대로 넘긴다.
func (p exchangeDisplayPlan) displayRateFor(rateFromTo float64, originalFrom string) float64 {
	if p.BaseCurrency == originalFrom {
		return rateFromTo * p.BaseUnits
	}
	if rateFromTo == 0 {
		return 0
	}
	return (1 / rateFromTo) * p.BaseUnits
}

// exchangeRateCorrection은 1 미만인 손실 있는 환율값을 정확한 값으로
// 대체하기 위해 반대 방향으로 조회한 Frankfurter 결과를 담는다.
// Frankfurter는 통화 쌍의 크기와 무관하게 응답을 일정한 소수 자릿수로
// 반올림하는 것으로 보인다 — rate가 1 이상이면 유효숫자가 충분히 남아
// 문제없지만(예: USD->KRW의 1459.45), rate가 1 미만이면 치명적이다
// (KRW->USD의 ~0.00069는 유효숫자가 2자리 정도밖에 남지 않고, 이걸 역수로
// 환산하면 직접 조회한 USD->KRW 값과 거의 1%나 차이가 난다). 이 손실된
// 작은 값을 그대로 믿는 대신, exchangeRateCorrection은 반대 방향 쌍을
// 조회해서(같은 API가 1 이상, 즉 전체 정밀도로 값을 주는 방향) 단순
// float64 나눗셈으로 작은 쪽의 rate를 역산한다 — Frankfurter가 자신의
// 1 미만 응답에 적용한 반올림보다 훨씬 정밀하고, 무엇보다 사용자가 원래
// 어느 방향으로 요청했든 항상 동일한 결과가 나온다 — 두 방향 모두 결국
// 같은 전체 정밀도의 반대 방향 관측값을 거쳐 계산되기 때문이다.
type exchangeRateCorrection struct {
	currentOK bool
	current   frankfurterResponse
	rangeOK   bool
	rangeResp frankfurterRangeResponse
}

// fetchExchangeRateCorrection은 반대 방향(to, from) 쌍을 — 기본 조회와
// 동일하게 "latest"와 7일 range를 — 동시에 가져온다. 각 조회는
// best-effort 방식이다: 실패하면 해당 OK 플래그만 false로 남고, 호출하는
// 쪽은 그 부분에 한해 원본(손실이 있을 수 있는) 값으로 대체한다.
func fetchExchangeRateCorrection(ctx context.Context, from, to string) exchangeRateCorrection {
	var corr exchangeRateCorrection

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		rc, err := fetchFrankfurterRate(ctx, "latest", to, from)
		if err == nil {
			corr.current = rc
			corr.currentOK = true
		}
	}()

	go func() {
		defer wg.Done()
		end := time.Now()
		start := end.AddDate(0, 0, -7)
		rr, err := fetchFrankfurterRange(ctx, start.Format("2006-01-02"), end.Format("2006-01-02"), to, from)
		if err == nil {
			corr.rangeResp = rr
			corr.rangeOK = true
		}
	}()

	wg.Wait()
	return corr
}

// correctedRate는 반대 방향 "latest" 조회 결과로부터 계산한 정확한
// from->to rate를 반환하며, 그 조회가 실패했거나 반대 방향 응답을 쓸 수
// 없는 경우(값이 없거나 0)에는 ok=false를 반환한다 — 이 경우 호출하는
// 쪽은 best-effort 보정 실패로 섹션 전체를 실패시키는 대신 원본 값을
// 그대로 쓴다.
func (c exchangeRateCorrection) correctedRate(from string) (rate float64, ok bool) {
	if !c.currentOK {
		return 0, false
	}
	reverse, ok := c.current.Rates[from]
	if !ok || reverse == 0 {
		return 0, false
	}
	return 1 / reverse, true
}

// correctedRateForDate는 correctedRate의 날짜별 버전으로, 반대 방향
// range 조회 결과에서 날짜를 맞춰 값을 찾는다.
func (c exchangeRateCorrection) correctedRateForDate(date, from string) (rate float64, ok bool) {
	if !c.rangeOK {
		return 0, false
	}
	dateRates, ok := c.rangeResp.Rates[date]
	if !ok {
		return 0, false
	}
	reverse, ok := dateRates[from]
	if !ok || reverse == 0 {
		return 0, false
	}
	return 1 / reverse, true
}

// findYesterdayRate는 (날짜 오름차순으로 정렬된) weekly에서 currentDate
// 이전 날짜 중 가장 큰 값을 가진 항목, 즉 오늘 이전 가장 최근 영업일을
// 반환한다. Frankfurter의 range 응답은 주말/공휴일을 아예 빼고 내려주므로,
// "오늘 이전 마지막 항목"이 곧 "오늘 이전 가장 최근 영업일"이며, 여기서
// 따로 주말 처리 로직을 둘 필요가 없다.
func findYesterdayRate(weekly []ExchangeRatePoint, currentDate string) *ExchangeRatePoint {
	var best *ExchangeRatePoint
	for i := range weekly {
		if weekly[i].Date < currentDate && (best == nil || weekly[i].Date > best.Date) {
			p := weekly[i]
			best = &p
		}
	}
	return best
}

// exchangeRawCacheTTL은 raw_data_cache에 저장된, 현재값 중심의 exchange
// 결과("weekly"/"yesterday"는 빠진 상태 — 아래 exchangeWeeklyRawCacheTTL
// 참고)를 한동안 재사용할 수 있게 한다 — 실제 환율은 하루에 몇 번밖에
// 갱신되지 않으므로, 이 기간 안에 대시보드를 새로고침/재시도할 때마다
// Frankfurter를 다시 호출하는 건 순전한 낭비다. 환율은 날씨보다 훨씬
// 드물게 변하므로 weatherRawCacheTTL보다 길게 잡았다. 예전에는 프로세스
// 메모리에만 있었지만, 이제는 raw_data_cache 테이블(raw_data_cache.go)에
// 저장되어 서버가 재시작돼도 그대로 남아있다.
const exchangeRawCacheTTL = 30 * time.Minute

// exchangeWeeklyRawCacheTTL은 "현재값"과 별개의 TTL/캐시 키
// (exchangeWeeklyFetchCacheKey)로 7일 추이(Weekly)만 저장한다. 원래는
// fetchExchange가 반환하는 ExchangeData 전체(현재값+weekly)를 통째로 하나의
// 캐시 항목으로 저장했는데, 그 결과 현재값 조회(Frankfurter "latest")는
// 성공하고 7일 추이 조회(Frankfurter range)만 실패한 순간이 있으면, weekly가
// 빈 채로 "성공한" 응답 전체가 30분 동안 그대로 캐시에 박제되어 — 그 30분
// 내내 fetchExchange 자체가 다시 호출되지 않으니 range 조회를 재시도할
// 기회조차 없이 차트가 계속 비어 있었다. 캐시 키를 분리하면 weekly만 독립적으로
// 다시 시도할 수 있고, 이번 조회에서 range가 실패해도(빈 결과라서) 기존에
// 저장해 둔 weekly 캐시를 덮어쓰지 않는다(persistExchangeCache 참고).
const exchangeWeeklyRawCacheTTL = 30 * time.Minute

func exchangeFetchCacheKey(from, to string) string {
	return "exchange:" + from + ":" + to
}

func exchangeWeeklyFetchCacheKey(from, to string) string {
	return "exchange:weekly:" + from + ":" + to
}

// combineExchangeCache는 별도로 캐싱된 현재값(row)과 7일 추이(weeklyRow)를
// 합쳐 하나의 ExchangeData로 복원한다. Yesterday는 저장해두지 않고 항상
// 여기서 weekly+current로부터 다시 계산한다 — fetchExchange가 하는 계산과
// 정확히 같은 함수(findYesterdayRate/computeChangePercent)를 쓰므로, 캐시를
// 거쳐도 두 값이 어긋나지 않는다.
func combineExchangeCache(row, weeklyRow rawDataCacheRow) (*ExchangeData, bool) {
	var data ExchangeData
	if err := json.Unmarshal([]byte(row.dataJSON), &data); err != nil {
		return nil, false
	}
	var weekly []ExchangeRatePoint
	if err := json.Unmarshal([]byte(weeklyRow.dataJSON), &weekly); err != nil {
		return nil, false
	}
	data.Weekly = weekly
	if yesterday := findYesterdayRate(weekly, data.Current.Date); yesterday != nil {
		data.Yesterday = &ExchangeYesterday{
			Rate:          yesterday.Rate,
			DisplayRate:   yesterday.DisplayRate,
			Date:          yesterday.Date,
			ChangePercent: computeChangePercent(yesterday.Rate, data.Current.Rate),
		}
	}
	return &data, true
}

// persistExchangeCache는 방금 가져온 fetched를 두 개의 독립된 캐시 항목에
// 나눠 저장한다: 현재값(weekly/yesterday를 뺀 core)은 항상 key에 저장하고,
// weekly는 이번 조회에서 실제로 값을 얻었을 때만(len > 0) weeklyKey에
// 저장한다 — range 조회가 이번에 실패해서 비어 있다면, 이전에 저장해 둔
// (아직 만료 전일 수도 있는) weekly 캐시를 빈 값으로 덮어써서 잃어버리지
// 않기 위함이다.
func persistExchangeCache(key, weeklyKey string, fetched *ExchangeData, now time.Time) {
	// rawCacheUpsertTimeout 문서 주석 참고 — 호출자의 요청 스코프 ctx가
	// 아니라 독립적인 컨텍스트를 써야, Frankfurter 호출이 요청 타임아웃
	// 예산을 거의 다 쓰고 나서야 성공한 경우에도 저장 자체가 취소되지 않는다.
	insertCtx, cancel := context.WithTimeout(context.Background(), rawCacheUpsertTimeout)
	defer cancel()

	core := *fetched
	core.Weekly = nil
	core.Yesterday = nil
	if encoded, err := json.Marshal(core); err != nil {
		log.Printf("환율(%s): 직렬화 실패, 캐시 저장 생략: %v", key, err)
	} else if err := upsertRawDataCache(insertCtx, db, key, string(encoded), now, now.Add(exchangeRawCacheTTL)); err != nil {
		log.Printf("환율(%s): DB 저장 실패: %v", key, err)
	}

	if len(fetched.Weekly) == 0 {
		log.Printf("환율(%s): 이번 조회에 7일 추이가 없어(위 로그 참고) 기존 캐시를 그대로 유지하고 저장은 생략합니다", weeklyKey)
		return
	}
	if encoded, err := json.Marshal(fetched.Weekly); err != nil {
		log.Printf("환율(%s): 직렬화 실패, 캐시 저장 생략: %v", weeklyKey, err)
	} else if err := upsertRawDataCache(insertCtx, db, weeklyKey, string(encoded), now, now.Add(exchangeWeeklyRawCacheTTL)); err != nil {
		log.Printf("환율(%s): DB 저장 실패: %v", weeklyKey, err)
	}
}

// getCachedOrFetchExchange는 dashboardHandler가 사용하는 진입점이다 —
// Frankfurter를 다시 호출하는 대신 최근 fetchExchange 결과를 재사용한다.
// 여기서 from/to를 정규화해두면(fetchExchange 자체의 기본값 처리와
// 동일하게) 캐시 키와 실제로 조회한 통화 쌍이 서로 어긋나는 일이 없다.
//
// 현재값과 weekly는 서로 다른 raw_data_cache 항목(key/weeklyKey)에
// 독립적으로 저장된다 — exchangeWeeklyRawCacheTTL 문서 주석 참고. 그래서
// 캐시를 그대로 쓰려면 둘 다 신선해야 한다: 하나라도 만료됐으면
// fetchExchange를 다시 호출한다(current/range를 병렬로 함께 조회하는 게
// 원래 더 저렴하기도 하고, 두 캐시를 완전히 독립적으로 갱신하려면 훨씬
// 복잡해지는 데 비해 이득이 적다) — 다만 그 결과를 저장할 때는 여전히
// 두 캐시를 독립적으로 갱신하므로, weekly만 실패하는 순간이 있어도 그
// 실패가 현재값 캐시에 30분간 함께 박제되지 않는다.
func getCachedOrFetchExchange(ctx context.Context, from, to string) (*ExchangeData, error) {
	if from == "" {
		from = defaultFromCurrency
	}
	if to == "" {
		to = defaultToCurrency
	}
	key := exchangeFetchCacheKey(from, to)
	weeklyKey := exchangeWeeklyFetchCacheKey(from, to)
	now := time.Now()

	row, found := lookupRawDataCache(ctx, db, key)
	weeklyRow, weeklyFound := lookupRawDataCache(ctx, db, weeklyKey)

	if found && isRawCacheFresh(row, now) && weeklyFound && isRawCacheFresh(weeklyRow, now) {
		if combined, ok := combineExchangeCache(row, weeklyRow); ok {
			log.Printf("환율(%s->%s): 현재값(%s까지)+7일추이(%s까지) 모두 유효 (Frankfurter 미호출)",
				from, to, row.expiresAt.Format("15:04:05"), weeklyRow.expiresAt.Format("15:04:05"))
			return combined, nil
		}
		log.Printf("환율(%s): 캐시 파싱 실패, 무시하고 새로 가져옵니다", key)
	}

	fetched, fetchErr := fetchExchange(ctx, from, to)
	if fetchErr != nil {
		if found {
			if stale, ok := combineExchangeStale(row, weeklyRow, weeklyFound); ok {
				log.Printf("환율(%s->%s): Frankfurter 호출 실패(%v) — 만료된 캐시를 잠정치로 사용", from, to, fetchErr)
				return stale, nil
			}
		}
		return nil, fetchErr
	}

	persistExchangeCache(key, weeklyKey, fetched, now)

	return fetched, nil
}

// combineExchangeStale은 fetchExchange 자체가 실패했을 때(Frankfurter
// "latest" 호출 실패 등)의 잠정치 폴백이다 — 현재값 캐시는 반드시 있어야
// 하고(row), weekly 캐시는 있으면 붙이고 없으면 weekly 없이(빈 채로)
// 반환한다 — 완전히 실패하는 것보다는 낫기 때문이다.
func combineExchangeStale(row, weeklyRow rawDataCacheRow, weeklyFound bool) (*ExchangeData, bool) {
	var stale ExchangeData
	if err := json.Unmarshal([]byte(row.dataJSON), &stale); err != nil {
		return nil, false
	}
	if !weeklyFound {
		return &stale, true
	}
	var weekly []ExchangeRatePoint
	if err := json.Unmarshal([]byte(weeklyRow.dataJSON), &weekly); err == nil {
		stale.Weekly = weekly
		if yesterday := findYesterdayRate(weekly, stale.Current.Date); yesterday != nil {
			stale.Yesterday = &ExchangeYesterday{
				Rate:          yesterday.Rate,
				DisplayRate:   yesterday.DisplayRate,
				Date:          yesterday.Date,
				ChangePercent: computeChangePercent(yesterday.Rate, stale.Current.Rate),
			}
		}
	}
	return &stale, true
}

func fetchExchange(ctx context.Context, from, to string) (*ExchangeData, error) {
	if from == "" {
		from = defaultFromCurrency
	}
	if to == "" {
		to = defaultToCurrency
	}

	var current frankfurterResponse
	var rangeResp frankfurterRangeResponse
	var currentErr, rangeErr error

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		current, currentErr = fetchFrankfurterRate(ctx, "latest", from, to)
	}()

	go func() {
		defer wg.Done()
		end := time.Now()
		start := end.AddDate(0, 0, -7)
		rangeResp, rangeErr = fetchFrankfurterRange(ctx, start.Format("2006-01-02"), end.Format("2006-01-02"), from, to)
	}()

	wg.Wait()

	if currentErr != nil {
		return nil, currentErr
	}

	rate, ok := current.Rates[to]
	if !ok {
		return nil, fmt.Errorf("rate for %s not found in response", to)
	}
	log.Printf("환율(%s->%s): Frankfurter 원본 rate=%v (날짜=%s)", from, to, rate, current.Date)

	// exchangeRateCorrection의 문서 주석 참고: 1 미만인 rate는 Frankfurter
	// 응답이 정밀도를 잃었다는 신호이므로, 반대 방향 쌍을 조회해서 그 (정확한)
	// 역수를 대신 사용한다 — 이 보정은 여기서 한 번만 결정되어 아래 모든
	// 지점(current, weekly, yesterday)에 적용되므로, KRW->USD 요청과
	// USD->KRW 요청이 ~1% 차이 없이 정확히 같은 표시 값으로 수렴한다.
	var correction exchangeRateCorrection
	if exchangeIsInverted(rate) {
		correction = fetchExchangeRateCorrection(ctx, from, to)
		if corrected, ok := correction.correctedRate(from); ok {
			log.Printf("환율(%s->%s): Frankfurter 원본 rate=%v 정밀도 부족 의심 — 역방향(%s->%s) 기준으로 보정된 rate=%v 사용", from, to, rate, to, from, corrected)
			rate = corrected
		} else {
			log.Printf("환율(%s->%s): 정밀도 보정용 역방향(%s->%s) 조회 실패, 원본 rate=%v 그대로 사용", from, to, to, from, rate)
		}
	}

	plan := planExchangeDisplay(rate, from, to)
	displayRate := plan.displayRateFor(rate, from)

	data := &ExchangeData{
		From:         from,
		To:           to,
		Current:      ExchangeRatePoint{Date: current.Date, Rate: rate, DisplayRate: displayRate},
		RawRate:      rate,
		DisplayRate:  displayRate,
		DisplayLabel: plan.Label,
	}

	// weekly 시리즈(그리고 여기서 파생되는 yesterday/추세 수치)는 차트와
	// AI 브리핑을 위한 부가 요소일 뿐, exchange 섹션 자체가 이것에 의존하지는
	// 않는다 — range 조회가 실패해도 이 필드들만 비워두고 섹션 전체가
	// 실패하지는 않는다.
	if rangeErr == nil {
		weekly := make([]ExchangeRatePoint, 0, len(rangeResp.Rates))
		for date, rates := range rangeResp.Rates {
			r, ok := rates[to]
			if !ok {
				continue
			}
			if corrected, ok := correction.correctedRateForDate(date, from); ok {
				r = corrected
			}
			weekly = append(weekly, ExchangeRatePoint{Date: date, Rate: r, DisplayRate: plan.displayRateFor(r, from)})
		}
		sort.Slice(weekly, func(i, j int) bool { return weekly[i].Date < weekly[j].Date })
		data.Weekly = weekly
		log.Printf("환율(%s->%s): 7일 추이 %d개 포인트 준비 완료", from, to, len(weekly))

		if yesterday := findYesterdayRate(weekly, data.Current.Date); yesterday != nil {
			data.Yesterday = &ExchangeYesterday{
				Rate:          yesterday.Rate,
				DisplayRate:   yesterday.DisplayRate,
				Date:          yesterday.Date,
				ChangePercent: computeChangePercent(yesterday.Rate, rate),
			}
		}
	} else {
		log.Printf("환율(%s->%s): 7일 추이 조회 실패로 이번 응답에는 weekly 없이 진행: %v", from, to, rangeErr)
	}

	return data, nil
}
