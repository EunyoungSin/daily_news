package main

// SectionMeta는 대시보드 응답의 모든 섹션에 임베드되어, 프론트엔드가 섹션별
// 소요 시간과 성공/실패 여부를 서로 독립적으로 표시할 수 있게 한다.
type SectionMeta struct {
	Success    bool   `json:"success"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	// Notice는 에러가 아닌, 가벼운 안내성 메시지다 — 예를 들어 오늘자
	// NewsData.io 크레딧 예산이 거의 소진되어 최신 데이터 대신 캐시된
	// (다소 오래되었을 수 있는) 결과를 보여주고 있다는 경고 등이다.
	// Error와 달리, Notice가 설정되어도 Success는 여전히 true다.
	Notice string `json:"notice,omitempty"`
}

type CurrentWeather struct {
	City         string  `json:"city"`
	CityLabel    string  `json:"cityLabel"`
	TemperatureC float64 `json:"temperatureC"`
	WindSpeedKph float64 `json:"windSpeedKph"`
	WeatherCode  int     `json:"weatherCode"`
	Description  string  `json:"description"`
	ObservedAt   string  `json:"observedAt"`
	// DetailURL은 이 도시에 대한 외부 날씨 검색 링크다 (국내 도시는 네이버,
	// 해외 도시는 구글) — weather.go의 weatherDetailURL 참고.
	DetailURL string `json:"detailUrl"`
}

// PeriodForecast는 특정 시각 하나의 예보값이다 — Morning은 08:00,
// Afternoon은 14:00 기준 (weather.go의 buildForecast, kma.go의
// buildKMAForecast 참고) — 일정 구간을 집계한 값이 아니다.
//
// 데이터 소스(기상청 getVilageFcst — base_time 이후 시점만 예보하므로
// 당일이라도 이미 지나간 시간대는 응답에 아예 없다 — 또는 Open-Meteo,
// 혹은 validateDayForecast의 유효성 검사)가 실제로 이 값을 제공하지
// 못한 경우 Available은 false가 된다. 이때 나머지 필드는 의미 없는
// 제로값일 뿐 실제 데이터가 아니다: 특히 TemperatureC:0을 "0°C"로
// 해석해서는 절대 안 된다 — 호출하는 쪽(프론트엔드, 브리핑 프롬프트)은
// 반드시 Available을 먼저 확인해서 false면 "데이터 없음"으로 처리해야
// 하며, 제로값으로 대체해서는 안 된다.
type PeriodForecast struct {
	TemperatureC      float64 `json:"temperatureC"`
	WeatherCode       int     `json:"weatherCode"`
	Description       string  `json:"description"`
	PrecipProbability int     `json:"precipProbability"`
	Available         bool    `json:"available"`
}

type DayForecast struct {
	Morning   PeriodForecast `json:"morning"`
	Afternoon PeriodForecast `json:"afternoon"`
}

type WeatherForecast struct {
	Today    DayForecast `json:"today"`
	Tomorrow DayForecast `json:"tomorrow"`
}

type WeatherData struct {
	Current  CurrentWeather  `json:"current"`
	Forecast WeatherForecast `json:"forecast"`
	// DataSource는 이 값이 실제로 어느 소스에서 왔는지 프론트엔드에
	// 알려준다: "kma"(기상청, 국내 도시), "open-meteo"(해외 도시 — 이
	// 경우 Open-Meteo가 원래부터 유일한/기본 소스), 또는
	// "open-meteo-fallback"(국내 도시인데 KMA 호출이 실패해서 Open-Meteo가
	// 대신 쓰인 경우) — 이 중 마지막 경우에만 프론트엔드가 "보조 데이터
	// 소스" 배지를 표시해야 한다.
	DataSource string `json:"dataSource"`
}

type WeatherSection struct {
	SectionMeta
	Data *WeatherData `json:"data,omitempty"`
}

// ExchangeRatePoint는 하루치 원본 환율이다 — Weekly의 각 항목뿐 아니라
// ExchangeData 안에 중첩된 Current에도 쓰인다. DisplayRate는 UI가 실제로
// 렌더링해야 할 값으로, Rate >= 1이면 Rate와 동일하고 Rate < 1이면 그
// 역수를 취한 값이다 (exchange.go의 computeExchangeDisplay 참고) —
// 0.00069(KRW->USD) 같은 값은 아무리 반올림해도 "0.00"으로 보이므로,
// 대신 익숙한 "1 USD = 1,459.85 KRW" 형태로 뒤집어 보여준다. 내부 계산
// (증감률, 추세 방향)은 전부 Rate만 쓰고 DisplayRate는 절대 쓰지 않으므로,
// 이 역수 변환이 계산 결과를 왜곡하는 일은 없다 — 어디까지나 최종 숫자를
// "어떻게 표시할지"에만 영향을 준다.
type ExchangeRatePoint struct {
	Date        string  `json:"date"`
	Rate        float64 `json:"rate"`
	DisplayRate float64 `json:"displayRate"`
}

// ExchangeYesterday는 Current.Date보다 엄밀히 이전인 가장 최근 영업일을
// 가리킨다 — 주말/공휴일로 며칠 비어 있으면 그 공백 이전 Weekly의 마지막
// 항목일 뿐, 문자 그대로의 "달력상 어제"는 아니다 (exchange.go의
// findYesterdayRate 참고). ChangePercent는 Current 대비 이 환율의
// 변화율로, computeChangePercent로 한 번만 계산되어 브리핑의 주간 추세
// 계산에도 그대로 재사용되므로 두 값이 서로 다른 반올림 로직 때문에
// 어긋나는 일이 없다. 이 계산은 항상 원본 Rate로만 하고 DisplayRate는
// 절대 쓰지 않는다 — 역수 변환은 선형적이지 않으므로, 표시용(뒤집혔을
// 수도 있는) 값으로 변화율을 계산하면 실제 환율의 변화와 일치하지 않게
// 된다.
type ExchangeYesterday struct {
	Rate          float64 `json:"rate"`
	DisplayRate   float64 `json:"displayRate"`
	Date          string  `json:"date"`
	ChangePercent float64 `json:"changePercent"`
}

type ExchangeData struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Current ExchangeRatePoint `json:"current"`
	// RawRate/DisplayRate/DisplayLabel은 Current.Rate/Current.DisplayRate를
	// 최상위 레벨에도 그대로 노출한 것으로, 프론트엔드와 AI 브리핑이 Current
	// 안까지 들어가지 않고도 "현재 환율"을 바로 읽을 수 있게 한다.
	// DisplayLabel은 DisplayRate가 어느 통화를 기준(예: "1 USD = ")으로
	// 표시되는지 나타낸다 — 1 미만 환율은 어느 쪽이 "1 단위"가 되는지를
	// 뒤집어버리므로, 이 값이 항상 From과 같지는 않다.
	RawRate      float64 `json:"rawRate"`
	DisplayRate  float64 `json:"displayRate"`
	DisplayLabel string  `json:"displayLabel"`
	// Yesterday/Weekly는 best-effort 성격이다: range 조회가 실패하면 이
	// 필드들은 nil/빈 값으로 남지만, 섹션 자체는 Current만으로도 성공 처리된다.
	Yesterday *ExchangeYesterday  `json:"yesterday,omitempty"`
	Weekly    []ExchangeRatePoint `json:"weekly,omitempty"`
}

type ExchangeSection struct {
	SectionMeta
	Data *ExchangeData `json:"data,omitempty"`
}

type NewsItem struct {
	// ID는 NewsData.io의 article_id다 — Hacker News에서 쓰던 작은 정수가
	// 아니라 문자열 해시이므로, 이 값(과 이를 키로 쓰는 번역 캐시)은 처음부터
	// 끝까지 string 타입이다.
	ID          string `json:"id"`
	Title       string `json:"title"`
	Link        string `json:"link"`
	SourceName  string `json:"sourceName"`
	PubDate     string `json:"pubDate"`
	Description string `json:"description,omitempty"`
	// TranslatedTitle은 해외(international) 모드에서만 채워진다 (국내 기사는
	// 이미 한글이라 번역 자체를 건너뛴다). 값이 비어 있으면 번역이 실패했거나
	// 아직 끝나지 않은 것이며, 이 경우 프론트엔드는 Title로 대체해 보여준다.
	TranslatedTitle string `json:"translatedTitle"`
}

type NewsData struct {
	Items []NewsItem `json:"items"`
	// Category/Region은 요청받은 값을 그대로 돌려주어, 프론트엔드가 (재시도
	// 이후 등에도) 지금 보고 있는 게 무엇인지 확인할 수 있게 한다.
	Category string `json:"category"`
	Region   string `json:"region"`
}

type NewsSection struct {
	SectionMeta
	Data *NewsData `json:"data,omitempty"`
}

// BriefingSectionMeta는 각 섹션(weather/exchange/news)별 캐시 상태를
// 개별적으로 보고한다. 이제 섹션마다 독립적으로 생성/캐시되므로, 한
// 섹션은 새로 생성되고 나머지 두 섹션은 그대로 캐시가 재사용되는 경우가
// 있을 수 있다.
type BriefingSectionMeta struct {
	Cached      bool   `json:"cached"`
	GeneratedAt string `json:"generatedAt"`
	// Detailed는 (여러 섹션을 합친 문자열이 아니라) 이 섹션 자체의 텍스트다
	// — 프론트엔드는 합쳐진 BriefingData.Detailed를 다시 섹션별로 쪼개려
	// 하는 대신, 이 값을 그대로 써서 각 섹션을 독립적으로 렌더링하고 강조
	// 표시한다. 예전에는 simple(1문장 요약)도 함께 내려줬지만, 출력 토큰을
	// 줄이기 위해 detailed 하나만 생성하도록 단순화했다.
	Detailed string `json:"detailed"`
	// Status는 이 섹션 텍스트가 어떻게 만들어졌는지를 세분화한다:
	//   - "fresh": 이번 요청에서 Groq로 방금 새로 생성됨
	//   - "cached": 입력 데이터가 그대로라 정상적으로 캐시를 재사용함 —
	//     문제 상황이 아니다
	//   - "stale_fallback": 이번에 새로 생성을 시도했지만 실패해서(TPM
	//     초과, 반복 감지, API 오류 등) 어쩔 수 없이 이전 캐시로 대체됨 —
	//     사용자에게 알려야 하는 문제 상황이다
	//   - "failed": 생성에 실패했고 대체할 캐시도 없음
	// briefing.go의 resolveBriefingSection이 채운다. 과거 배포된
	// 프론트엔드와의 호환을 위해 omitempty로 둔다.
	Status string `json:"status,omitempty"`
	// FailureReason은 Status가 stale_fallback/failed일 때만 채워지며,
	// 사용자에게 그대로 노출할 상세 사유가 아니라 프론트엔드가 안내
	// 문구를 고르는 데 참고하는 대략적인 카테고리다(예: "rate_limit",
	// "generation_error") — classifyBriefingFailureReason 참고.
	FailureReason string `json:"failureReason,omitempty"`
}

type BriefingSectionsMeta struct {
	Weather  BriefingSectionMeta `json:"weather"`
	Exchange BriefingSectionMeta `json:"exchange"`
	News     BriefingSectionMeta `json:"news"`
}

type BriefingData struct {
	// Detailed는 각 섹션의 (최대) 두 문장짜리 결과를 순서대로(weather,
	// exchange, news) 이어붙인 것이다. 예전에는 이보다 짧은 simple(각
	// 섹션 1문장씩 이어붙인) 버전도 함께 내려줬지만, 출력 토큰을 줄이기
	// 위해 detailed 하나만 생성하도록 단순화했다.
	Detailed string `json:"detailed"`
	// Cached는 모든 섹션이 캐시에서 재사용되었을 때만(이번 요청에서 Groq
	// 호출이 한 번도 없었을 때만) true다. GeneratedAt은 세 섹션의 생성
	// 시각 중 가장 최근 값이다. 섹션별 세부 내역은 BriefingMeta 참고.
	Cached       bool                 `json:"cached"`
	GeneratedAt  string               `json:"generatedAt"`
	BriefingMeta BriefingSectionsMeta `json:"briefingMeta"`
}

type BriefingSection struct {
	SectionMeta
	Data *BriefingData `json:"data,omitempty"`
}

type LottoDraw struct {
	DrwNo   int    `json:"drwNo"`
	DrwDate string `json:"drwDate"`
	Numbers []int  `json:"numbers"`
	Bonus   int    `json:"bonus"`
}

type LottoAIInsight struct {
	// Available은 GROQ_API_KEY가 없거나 Groq 호출이 실패했을 때 false가
	// 되며, 이 경우 Text에는 대신 사용자에게 보여줄 대체 메시지가 담긴다.
	Available   bool   `json:"available"`
	Text        string `json:"text"`
	Cached      bool   `json:"cached"`
	GeneratedAt string `json:"generatedAt,omitempty"`
}

// LottoRecommendationNumber는 "이번 주 추천 번호" 6개 중 하나이며, 어느
// 빈도 그룹(hot/mid/cold)에서 뽑혔는지 태그가 붙어 있다 — 어디까지나
// "빈도대 골고루 섞기"라는 다양성을 위한 장치일 뿐, 특정 그룹이
// 당첨 확률을 높여준다는 주장은 아니다. lotto_recommendation.go의
// computeRecommendationNumbers 참고.
type LottoRecommendationNumber struct {
	Number int    `json:"number"`
	Group  string `json:"group"` // "hot" | "mid" | "cold"
}

// LottoRecommendation은 현재 판매 회차에 대한 고정된 번호 세트
// (IsBlackout false)이거나, 이번 회차 판매 마감부터 다음 회차 번호가
// 나오기 전까지 추천이 잠시 중단되었다는 안내(IsBlackout true) 중
// 하나다 — lotto_recommendation.go 참고.
type LottoRecommendation struct {
	IsBlackout      bool                        `json:"isBlackout"`
	Numbers         []LottoRecommendationNumber `json:"numbers,omitempty"`
	CycleStartDate  string                      `json:"cycleStartDate,omitempty"`
	GeneratedAt     string                      `json:"generatedAt,omitempty"`
	NextAvailableAt string                      `json:"nextAvailableAt,omitempty"`
}

type LottoData struct {
	Latest LottoDraw `json:"latest"`
	// History는 최근 회차들을 최신순으로 담고 있다.
	History []LottoDraw `json:"history"`
	// Frequency는 로또 번호(1~45, JSON 객체 키의 특성상 문자열 키로 표현됨)를
	// History 전체에서 등장한 횟수에 매핑한다.
	Frequency      map[int]int         `json:"frequency"`
	RecentAppeared []int               `json:"recentAppeared"`
	AIInsight      LottoAIInsight      `json:"aiInsight"`
	Recommendation LottoRecommendation `json:"recommendation"`
}

type LottoSection struct {
	SectionMeta
	// IsBackfilling은 사용자가 로또 카드의 ON/OFF 토글로 데이터 수집을 켜서
	// 백그라운드에서 회차를 채우는 중일 때 true다. 프론트엔드는 이 경우 짧은
	// 간격으로 재요청해 진행 상황을 폴링한다 — lotto.go의
	// lottoStartCollection/lottoIsCollecting 참고.
	IsBackfilling bool       `json:"isBackfilling"`
	Data          *LottoData `json:"data,omitempty"`
}

// Stage는 핸들러가 스트리밍으로 내려보내는 두 NDJSON 라인을 구분한다:
// "partial"은 weather/exchange가 준비되는 즉시 담기고, "final"은 이
// 값들과 news(내부적으로 dashboardHandler가 가져오지만 여기엔 노출되지
// 않음)에 의존하는 브리핑 단계가 끝난 뒤 뒤따라온다. partial 라인에서
// Briefing은 제로값이다 — 프론트엔드는 stage=="partial"이면 내용과
// 무관하게 "브리핑 아직 대기 중"으로 처리한다.
//
// News는 이 응답에 아예 포함되지 않는다: 뉴스 카드는 독립된
// GET /api/news(news_handler.go 참고)에서 데이터를 받아오므로, 뉴스의
// category/region을 바꿔도 weather/exchange/briefing에는 전혀 영향이
// 없다.
type DashboardResponse struct {
	Stage    string          `json:"stage"`
	City     string          `json:"city"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	Weather  WeatherSection  `json:"weather"`
	Exchange ExchangeSection `json:"exchange"`
	Briefing BriefingSection `json:"briefing"`
	TotalMs  int64           `json:"totalMs"`
}
