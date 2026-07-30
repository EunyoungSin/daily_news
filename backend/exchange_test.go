package main

import (
	"testing"
)

func TestComputeChangePercent(t *testing.T) {
	cases := []struct {
		name             string
		oldRate, newRate float64
		want             float64
	}{
		{"rise", 1470.11, 1480.00, 0.7},
		{"fall", 1478.4, 1470.11, -0.6},
		{"zero old rate guards against divide-by-zero", 0, 1470.11, 0},
		{"no change", 1470.11, 1470.11, 0},
	}

	for _, c := range cases {
		if got := computeChangePercent(c.oldRate, c.newRate); got != c.want {
			t.Errorf("%s: computeChangePercent(%v, %v) = %v, want %v", c.name, c.oldRate, c.newRate, got, c.want)
		}
	}
}

// TestFindYesterdayRateAcrossWeekend은 기능 명세서에서 명시적으로 언급한
// 시나리오를 다룬다: (달력상) "어제"가 환율이 발표되지 않는 주말이었다면,
// Frankfurter의 range 응답에는 그 날짜가 아예 빠져 있다 — 따라서 오늘 이전의
// 가장 최근 항목이 이미 올바른 "가장 최근 영업일" 값이며, 별도의 주말 감지
// 로직이 필요하지 않다.
func TestFindYesterdayRateAcrossWeekend(t *testing.T) {
	// 오늘은 월요일 2026-07-27이다. 환율이 발표된 영업일은 목요일 07-23,
	// 금요일 07-24 — 주말(07-25, 07-26)에는 항목이 전혀 없다.
	weekly := []ExchangeRatePoint{
		{Date: "2026-07-21", Rate: 1465.0},
		{Date: "2026-07-22", Rate: 1468.0},
		{Date: "2026-07-23", Rate: 1472.0},
		{Date: "2026-07-24", Rate: 1478.4}, // 금요일 — 실제 "어제"
	}

	got := findYesterdayRate(weekly, "2026-07-27")
	if got == nil {
		t.Fatal("expected a yesterday rate, got nil")
	}
	if got.Date != "2026-07-24" || got.Rate != 1478.4 {
		t.Errorf("got %+v, want the Friday entry (2026-07-24, 1478.4) — the last business day before the weekend", *got)
	}
}

func TestFindYesterdayRateWhenTodayIsInWeekly(t *testing.T) {
	// range 응답에는 오늘 자체가 마지막 항목으로 포함될 수 있다 —
	// findYesterdayRate는 이를 건너뛰어야 하며, "어제"로 반환해서는 안 된다.
	weekly := []ExchangeRatePoint{
		{Date: "2026-07-24", Rate: 1478.4},
		{Date: "2026-07-27", Rate: 1470.11}, // today
	}

	got := findYesterdayRate(weekly, "2026-07-27")
	if got == nil || got.Date != "2026-07-24" {
		t.Errorf("expected 2026-07-24 (skipping today's own entry), got %+v", got)
	}
}

func TestFindYesterdayRateEmpty(t *testing.T) {
	if got := findYesterdayRate(nil, "2026-07-27"); got != nil {
		t.Errorf("expected nil for an empty weekly series, got %+v", got)
	}
}

func TestComputeExchangeTrendMatchesSignOfChangePercent(t *testing.T) {
	if trend := computeExchangeTrend(0.7, 1470.11, "KRW"); trend == nil || trend.Direction != "상승" || trend.Implication != "KRW 약세" {
		t.Errorf("rising rate: got %+v, want 상승/KRW 약세", trend)
	}
	if trend := computeExchangeTrend(-0.6, 1478.4, "KRW"); trend == nil || trend.Direction != "하락" || trend.Implication != "KRW 강세" {
		t.Errorf("falling rate: got %+v, want 하락/KRW 강세", trend)
	}
	if trend := computeExchangeTrend(0, 1470.11, "KRW"); trend != nil {
		t.Errorf("zero change: expected nil trend (nothing to report), got %+v", trend)
	}
	if trend := computeExchangeTrend(0.7, 0, "KRW"); trend != nil {
		t.Errorf("no week-ago rate: expected nil trend, got %+v", trend)
	}
}

// TestPlanExchangeDisplayInvertsSubOneRates는 정확히 보고된 시나리오를
// 다룬다: KRW->USD의 원본 환율(약 0.00069)은 어떤 적당한 소수점 자리수로
// 표시해도 "0.00"으로 읽히므로, 역수로 뒤바꿔서 1 KRW가 아닌 1 USD(`to`
// 통화) 기준으로 표시해야 한다. 이는 KRW 전용 고정 방향 규칙이다
// (planExchangeDisplay의 문서 주석 참고) — 크기(magnitude)만으로는 더 이상
// KRW가 아닌 통화쌍의 반전 여부를 결정하지 않는다.
// TestPlanExchangeDisplayNeverInvertsNonKRWPairs 참고.
func TestPlanExchangeDisplayInvertsSubOneRates(t *testing.T) {
	rate := 0.00069
	plan := planExchangeDisplay(rate, "KRW", "USD")
	displayRate := plan.displayRateFor(rate, "KRW")
	wantRate := 1 / rate
	if !numbersMatch(displayRate, wantRate) {
		t.Errorf("displayRate = %v, want reciprocal %v", displayRate, wantRate)
	}
	if plan.Label != "1 USD = " {
		t.Errorf("Label = %q, want %q", plan.Label, "1 USD = ")
	}
	if plan.BaseCurrency != "USD" || plan.QuoteCurrency != "KRW" || plan.BaseUnits != 1 {
		t.Errorf("plan = %+v, want BaseCurrency=USD QuoteCurrency=KRW BaseUnits=1", plan)
	}
}

func TestPlanExchangeDisplayKeepsAtOrAboveOneRatesAsIs(t *testing.T) {
	plan := planExchangeDisplay(1459.45, "USD", "KRW")
	displayRate := plan.displayRateFor(1459.45, "USD")
	if displayRate != 1459.45 {
		t.Errorf("displayRate = %v, want unchanged 1459.45", displayRate)
	}
	if plan.Label != "1 USD = " {
		t.Errorf("Label = %q, want %q", plan.Label, "1 USD = ")
	}
}

// TestPlanExchangeDisplayJPYForcesBaseRegardlessOfMagnitude은 JPY-KRW
// 통화쌍의 양방향 모두에서 100-JPY 관례가 적용되는지 다룬다 — KRW가 관여된
// 통화쌍이므로, JPY->KRW는 JPY를 `from`/기준(base)으로 그대로 유지하고,
// KRW->JPY는 (KRW 관여 규칙에 따라) 서로 뒤바뀌어 이 경우에도 결국 JPY가
// 기준이 된다; 두 경우 모두 동일한 100단위 스케일의 displayRate로
// 귀결되어야 한다.
func TestPlanExchangeDisplayJPYForcesBaseRegardlessOfMagnitude(t *testing.T) {
	jpyToKrw := 9.05 // 1 JPY = 9.05 KRW
	krwToJpy := 1 / jpyToKrw

	planA := planExchangeDisplay(jpyToKrw, "JPY", "KRW")
	displayA := planA.displayRateFor(jpyToKrw, "JPY")

	planB := planExchangeDisplay(krwToJpy, "KRW", "JPY")
	displayB := planB.displayRateFor(krwToJpy, "KRW")

	for _, p := range []exchangeDisplayPlan{planA, planB} {
		if p.BaseCurrency != "JPY" || p.QuoteCurrency != "KRW" || p.BaseUnits != jpyDisplayUnits {
			t.Errorf("plan = %+v, want BaseCurrency=JPY QuoteCurrency=KRW BaseUnits=100", p)
		}
		if p.Label != "100 JPY = " {
			t.Errorf("Label = %q, want %q", p.Label, "100 JPY = ")
		}
	}
	if !numbersMatch(displayA, displayB) {
		t.Errorf("JPY->KRW display (%v) and KRW->JPY display (%v) must match — same market rate, same convention", displayA, displayB)
	}
	wantDisplay := jpyToKrw * 100
	if !numbersMatch(displayA, wantDisplay) {
		t.Errorf("displayA = %v, want %v (100 * JPY->KRW rate)", displayA, wantDisplay)
	}
}

// TestPlanExchangeDisplayJPYVsUSDStillScalesByHundred은 사용자가 직접
// JPY를 `from`으로 선택한(KRW 관여로 강제 반전된 것이 아닌) 비-KRW 통화쌍을
// 다룬다 — JPY가 선택된 기준(base) 통화이므로 100단위 관례는 여전히
// 적용되어야 한다. 반면 JPY가 비-KRW 통화쌍의 *quote* 쪽에만 있을 때
// (스케일링도, 강제 지정도 없음)는 TestPlanExchangeDisplayNeverInvertsNonKRWPairs
// 를 참고.
func TestPlanExchangeDisplayJPYVsUSDStillScalesByHundred(t *testing.T) {
	plan := planExchangeDisplay(0.0067, "JPY", "USD")
	if plan.BaseCurrency != "JPY" || plan.BaseUnits != jpyDisplayUnits {
		t.Errorf("plan = %+v, want JPY kept as the user's own chosen base at 100 units", plan)
	}
}

// TestPlanExchangeDisplayNeverInvertsNonKRWPairs는 이번 요청에서 요구한
// 핵심 동작 변경 사항이다: KRW가 전혀 관여하지 않는 통화쌍은 사용자가
// 선택한 그대로 — from을 기준(base)으로, to를 quote로, 통상적인 1단위
// 스케일로 — 표시되어야 하며, 원본 환율이 1 미만이더라도 강제 반전이
// 없어야 하고, JPY가 선택된 기준 통화 자신이 아닌 한 JPY-100 스케일링도
// 적용되지 않는다.
func TestPlanExchangeDisplayNeverInvertsNonKRWPairs(t *testing.T) {
	// USD->EUR, 환율 < 1 (EUR가 USD보다 가치가 높음) — 예전의 범용 크기 규칙
	// 아래에서는 "1 EUR = ... USD"로 반전되었겠지만, 이제는 선택된 그대로
	// 유지되어야 한다.
	plan := planExchangeDisplay(0.92, "USD", "EUR")
	if plan.BaseCurrency != "USD" || plan.QuoteCurrency != "EUR" || plan.BaseUnits != 1 {
		t.Errorf("USD->EUR plan = %+v, want BaseCurrency=USD QuoteCurrency=EUR BaseUnits=1 (no forced inversion)", plan)
	}
	if plan.Label != "1 USD = " {
		t.Errorf("Label = %q, want %q", plan.Label, "1 USD = ")
	}
	displayRate := plan.displayRateFor(0.92, "USD")
	if displayRate != 0.92 {
		t.Errorf("displayRate = %v, want the raw rate unchanged (0.92)", displayRate)
	}

	// USD->JPY: JPY는 비-KRW 통화쌍의 quote 쪽일 뿐이므로 100단위 처리를
	// 받아서는 안 된다 — "1 USD = 148.32 JPY"여야 하며, 절대
	// "100 JPY = ... USD"가 되어서는 안 된다.
	jpyPlan := planExchangeDisplay(148.32, "USD", "JPY")
	if jpyPlan.BaseCurrency != "USD" || jpyPlan.QuoteCurrency != "JPY" || jpyPlan.BaseUnits != 1 {
		t.Errorf("USD->JPY plan = %+v, want BaseCurrency=USD QuoteCurrency=JPY BaseUnits=1 (JPY not forced as base)", jpyPlan)
	}
	if jpyDisplay := jpyPlan.displayRateFor(148.32, "USD"); jpyDisplay != 148.32 {
		t.Errorf("USD->JPY displayRate = %v, want unchanged 148.32 (no /100 or *100 scaling)", jpyDisplay)
	}
}

func TestExchangeIsInvertedBoundaries(t *testing.T) {
	cases := []struct {
		rate float64
		want bool
	}{
		{0.00069, true},
		{0.999, true},
		{1, false},
		{1459.45, false},
		{0, false},
	}
	for _, c := range cases {
		if got := exchangeIsInverted(c.rate); got != c.want {
			t.Errorf("exchangeIsInverted(%v) = %v, want %v", c.rate, got, c.want)
		}
	}
}

func TestExchangeDisplayPlanGuardsDivideByZero(t *testing.T) {
	plan := planExchangeDisplay(0.00069, "KRW", "USD") // BaseCurrency = USD
	if got := plan.displayRateFor(0, "KRW"); got != 0 {
		t.Errorf("displayRateFor(0, ...) = %v, want 0 (no divide-by-zero)", got)
	}
}

// TestFetchExchangeStyleInversionAppliesConsistently는 (네트워크 호출
// 없이) fetchExchange가 처음부터 끝까지 하는 일을 그대로 재현한다:
// Current.Rate가 1 미만이 되는 순간, 모든 Weekly 포인트와 여기서 파생되는
// Yesterday 항목은 포인트마다 새로 판단하지 않고 동일한 plan을 사용해야
// 한다 — 그렇지 않으면 차트와 헤드라인 숫자가 어느 통화를 "1단위"로
// 볼지에 대해 서로 어긋날 수 있다.
func TestFetchExchangeStyleInversionAppliesConsistently(t *testing.T) {
	currentRate := 0.00069
	plan := planExchangeDisplay(currentRate, "KRW", "USD")

	weekly := []ExchangeRatePoint{
		{Date: "2026-07-20", Rate: 0.00068},
		{Date: "2026-07-27", Rate: currentRate},
	}
	for i := range weekly {
		weekly[i].DisplayRate = plan.displayRateFor(weekly[i].Rate, "KRW")
	}

	if !numbersMatch(weekly[0].DisplayRate, 1/0.00068) {
		t.Errorf("weekly[0].DisplayRate = %v, want %v", weekly[0].DisplayRate, 1/0.00068)
	}

	yesterday := findYesterdayRate(weekly, "2026-07-27")
	if yesterday == nil {
		t.Fatal("expected a yesterday entry")
	}
	if !numbersMatch(yesterday.DisplayRate, 1/0.00068) {
		t.Errorf("yesterday.DisplayRate = %v, want the same reciprocal as weekly[0] (%v)", yesterday.DisplayRate, 1/0.00068)
	}
}

// TestExchangeRateCorrectionDerivesAccurateReciprocal은 항목 2에 대한
// 회귀 테스트다: Frankfurter 자체의 KRW->USD 응답(약 0.00069)은 유효숫자가
// 약 2자리밖에 되지 않아서, 단순히 그 역수를 취하면(약 1449.28) 직접
// USD->KRW를 조회한 값(1459.45)과 거의 1%나 차이가 눈에 띄게 발생한다.
// 정밀한 역방향 관측값을 구할 수 있다면, correctedRate는 손실이 있는
// 원본 값을 그대로 신뢰하는 대신 그 값으로부터(단순 나눗셈을 통해)
// 작은 쪽의 환율을 도출해야 한다.
func TestExchangeRateCorrectionDerivesAccurateReciprocal(t *testing.T) {
	corr := exchangeRateCorrection{
		currentOK: true,
		current:   frankfurterResponse{Base: "USD", Rates: map[string]float64{"KRW": 1459.45}},
	}

	rate, ok := corr.correctedRate("KRW")
	if !ok {
		t.Fatal("expected correctedRate to succeed with a usable reverse observation")
	}
	want := 1 / 1459.45
	if rate != want {
		t.Errorf("correctedRate(KRW) = %v, want exactly %v (the precise reverse reciprocal)", rate, want)
	}

	// 이렇게 보정된 작은 쪽의 환율은 planExchangeDisplay/displayRateFor를
	// 다시 거치면 원래의 정밀한 USD->KRW 값을 그대로 재현해야 한다 — 양방향이
	// 서로 수렴함을 증명하는 것이다.
	plan := planExchangeDisplay(rate, "KRW", "USD")
	displayRate := plan.displayRateFor(rate, "KRW")
	if displayRate != 1459.45 {
		t.Errorf("round-tripped displayRate = %v, want exactly 1459.45 (matching a direct USD->KRW fetch)", displayRate)
	}
}

func TestExchangeRateCorrectionFailsGracefullyWhenUnusable(t *testing.T) {
	cases := []struct {
		name string
		corr exchangeRateCorrection
	}{
		{"fetch failed", exchangeRateCorrection{currentOK: false}},
		{"currency missing from response", exchangeRateCorrection{currentOK: true, current: frankfurterResponse{Rates: map[string]float64{}}}},
		{"zero rate", exchangeRateCorrection{currentOK: true, current: frankfurterResponse{Rates: map[string]float64{"KRW": 0}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := c.corr.correctedRate("KRW"); ok {
				t.Error("expected correctedRate to report ok=false rather than return a bogus value")
			}
		})
	}
}

func TestExchangeRateCorrectionForDateMatchesByDate(t *testing.T) {
	corr := exchangeRateCorrection{
		rangeOK: true,
		rangeResp: frankfurterRangeResponse{
			Rates: map[string]map[string]float64{
				"2026-07-20": {"KRW": 1478.4},
				"2026-07-27": {"KRW": 1459.45},
			},
		},
	}

	rate, ok := corr.correctedRateForDate("2026-07-20", "KRW")
	if !ok || rate != 1/1478.4 {
		t.Errorf("correctedRateForDate(2026-07-20) = %v, %v, want %v, true", rate, ok, 1/1478.4)
	}

	if _, ok := corr.correctedRateForDate("2026-01-01", "KRW"); ok {
		t.Error("expected ok=false for a date absent from the reverse range response")
	}
}

// TestToBriefingExchangeInputTrendMatchesWeeklyArray는 기능 명세서에서
// 요구한 특정 일관성 검사를 다룬다: 브리핑의 "지난 7일간 X% 하락/상승"
// 수치는, 서로 몰래 어긋날 수 있는 별도로 조회한 숫자가 아니라 프론트엔드
// 차트가 렌더링하는 것과 동일한 Weekly 배열(가장 오래된 항목인 Weekly[0])에서
// 도출되어야 한다.
func TestToBriefingExchangeInputTrendMatchesWeeklyArray(t *testing.T) {
	exchange := &ExchangeData{
		From: "USD", To: "KRW",
		Current: ExchangeRatePoint{Date: "2026-07-27", Rate: 1470.11},
		Weekly: []ExchangeRatePoint{
			{Date: "2026-07-20", Rate: 1478.4}, // 가장 오래된 항목 — "7일 전" 수치
			{Date: "2026-07-24", Rate: 1475.0},
			{Date: "2026-07-27", Rate: 1470.11},
		},
	}

	input := toBriefingExchangeInput(exchange)
	wantChange := computeChangePercent(1478.4, 1470.11)

	if input.SevenDaysAgoRate != 1478.4 {
		t.Errorf("SevenDaysAgoRate = %v, want 1478.4 (Weekly[0])", input.SevenDaysAgoRate)
	}
	if input.ChangePercent != wantChange {
		t.Errorf("ChangePercent = %v, want %v (computeChangePercent(Weekly[0], Current) — the same function Yesterday uses)", input.ChangePercent, wantChange)
	}
	if input.Trend == nil || input.Trend.Direction != "하락" {
		t.Errorf("expected a 하락 trend for a falling rate, got %+v", input.Trend)
	}
}

// TestExchangeCacheKeyIncludesPrefix는 raw_data_cache가 날씨/환율/뉴스를
// 테이블 하나에서 공유하므로, 캐시 키에 "exchange:" 접두사가 항상 붙어서
// 다른 데이터 종류와 절대 섞이지 않는지 확인한다 — 캐시 히트/미스
// 자체(DB 연동)는 이 프로젝트의 다른 DB 캐시들과 마찬가지로 실제 서버로
// 라이브 검증한다(raw_data_cache.go의 isRawCacheFresh 문서 주석 참고).
func TestExchangeCacheKeyIncludesPrefix(t *testing.T) {
	if got, want := exchangeFetchCacheKey("USD", "KRW"), "exchange:USD:KRW"; got != want {
		t.Errorf("exchangeFetchCacheKey(USD, KRW) = %q, want %q", got, want)
	}
}
