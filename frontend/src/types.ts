// backend/models.go의 타입들을 그대로 반영한다 (embedded SectionMeta 필드는
// Go의 encoding/json에 의해 최상위로 승격되므로, 각 *Section 타입은
// success/durationMs/error를 자신의 `data`와 나란히 펼쳐서 갖는다).

export interface SectionMeta {
  success: boolean
  durationMs: number
  error?: string
  // 에러는 아니지만 참고할 만한 안내 메시지 (예: 오늘 NewsData.io 크레딧이
  // 거의 소진되어 캐시된 결과를 보여주고 있다는 뉴스 섹션의 경고).
  // notice가 설정되어도 success는 여전히 true다.
  notice?: string
}

export interface CurrentWeather {
  city: string
  cityLabel: string
  temperatureC: number
  windSpeedKph: number
  weatherCode: number
  description: string
  observedAt: string
  // "자세히 보기" 버튼에 쓰이는 외부 날씨 검색 링크 (국내 도시는 Naver,
  // 해외 도시는 Google).
  detailUrl: string
}

// available은 백엔드가 이 기간의 값을 가져오지 못했거나 신뢰할 수 없을 때
// (backend/weather.go의 sanityCheckDayForecast 참고) false가 된다.
// 이때 나머지 필드는 의미 없는 0 값이므로, temperatureC 등을 렌더링하기
// 전에 반드시 available을 확인해야 한다 — 0을 실제 관측값으로 취급하지 말 것.
export interface PeriodForecast {
  temperatureC: number
  weatherCode: number
  description: string
  precipProbability: number
  available: boolean
  // available이 false일 때만 채워진다(backend/weather_slot_cache.go의
  // resolveForecastSlot 참고). "not_yet_available"은 슬롯 시각이 아직
  // 지나지 않아 정상적으로 발표 전인 경우이고, "past_missing"은 슬롯
  // 시각이 이미 지났는데도 값이 없는(즉시 재조회 시도까지 실패한) 예외
  // 상황이다 — 이 둘은 서로 다른 안내 문구를 써야 한다.
  unavailableReason?: 'not_yet_available' | 'past_missing'
  // available이 true일 때만 의미가 있다. 'forecast'는 그 시각이 이미 지난
  // 뒤 기상청 단기예보를 그 시각 이전 발표분으로 소급 조회해서 복구한
  // 값이라는 뜻이다(backend/models.go의 PeriodForecast.Source 참고) —
  // WeatherCard가 이 값일 때만 "예보치" 배지를 띄운다. 'observed'이거나
  // 필드 자체가 없으면(구버전 서버 응답) 배지 없이 기존처럼 보여준다.
  source?: 'observed' | 'forecast'
}

export interface DayForecast {
  morning: PeriodForecast
  afternoon: PeriodForecast
}

export interface WeatherForecast {
  today: DayForecast
  tomorrow: DayForecast
}

// "kma" = 기상청 실측치. "open-meteo" = 해외 도시의 정상 소스(폴백 아님).
// "open-meteo-fallback" = 국내 도시인데 기상청 호출이 실패해 Open-Meteo로
// 대체된 경우 — WeatherCard가 이 값일 때만 보조 데이터 소스 배지를 띄운다.
export type WeatherDataSource = 'kma' | 'open-meteo' | 'open-meteo-fallback'

export interface WeatherData {
  current: CurrentWeather
  forecast: WeatherForecast
  dataSource: WeatherDataSource
}

export interface WeatherSection extends SectionMeta {
  data?: WeatherData
}

// rate는 Frankfurter의 원본 값 — 내부 계산의 기준값일 뿐 화면에 그대로
// 표시하지 않는다. displayRate가 실제로 UI에 보여줘야 할 값으로,
// rate >= 1이면 rate와 동일하고 rate < 1이면 그 역수다
// (backend/exchange.go의 computeExchangeDisplay 참고) — 0.00069(KRW->USD)
// 같은 값은 어떻게 반올림해도 "0.00"으로 보이므로, 익숙한
// "1 USD = 1,459.85 KRW" 형태가 되도록 뒤집어 준다.
export interface ExchangeRatePoint {
  date: string
  rate: number
  displayRate: number
}

// Yesterday는 current.date보다 엄밀히 이전인 가장 최근 영업일이다
// — 주말/공휴일 공백이 있으면 그 공백 직전 `weekly`의 마지막 항목이며,
// 달력상의 단순 어제가 아니다 (backend/exchange.go의 findYesterdayRate
// 참고). changePercent는 항상 raw rate로 계산하며 displayRate로는
// 계산하지 않는다 — 역수 변환은 스케일에 대해 선형적이지 않기 때문이다.
export interface ExchangeYesterday {
  rate: number
  displayRate: number
  date: string
  changePercent: number
}

export interface ExchangeData {
  from: string
  to: string
  current: ExchangeRatePoint
  // rawRate/displayRate/displayLabel은 편의를 위해 current.rate/
  // current.displayRate를 최상위 레벨에 그대로 반영한 값이다. displayLabel은
  // displayRate가 어느 통화 기준("1 USD =" 등)인지 나타내는데, 1 미만인
  // rate는 어느 쪽이 "1 단위"인지가 뒤집히므로 항상 `from`은 아니다.
  rawRate: number
  displayRate: number
  displayLabel: string
  // best-effort 성격: 기간 조회가 실패하면 둘 다 비어 있을 수 있지만,
  // `current`만으로도 섹션을 렌더링하기엔 충분하다.
  yesterday?: ExchangeYesterday
  weekly?: ExchangeRatePoint[]
}

export interface ExchangeSection extends SectionMeta {
  data?: ExchangeData
}

export type NewsRegion = 'domestic' | 'international'

export interface NewsItem {
  // NewsData.io의 article_id — 작은 정수가 아니라 문자열 해시다.
  id: string
  title: string
  link: string
  sourceName: string
  pubDate: string
  description?: string
  // region === 'international'일 때만 값이 채워진다 (국내 기사는 이미
  // 한국어이므로). 비어 있으면 번역이 실패했거나 아직 끝나지 않은 것이므로,
  // "(번역 실패)" 표시와 함께 title로 대체한다.
  translatedTitle: string
}

export interface NewsData {
  items: NewsItem[]
  category: string
  region: NewsRegion
}

export interface NewsSection extends SectionMeta {
  data?: NewsData
}

// "fresh": 방금 새로 생성됨. "cached": 입력 불변이라 정상적으로 캐시
// 재사용(문제 상황 아님). "stale_fallback": 이번에 새로 생성을 시도했지만
// 실패해서(TPM 초과, 반복 감지, API 오류 등) 어쩔 수 없이 이전 캐시로
// 대체됨(사용자에게 알려야 하는 문제 상황). "failed": 생성 실패, 대체할
// 캐시도 없음. 과거 배포된 백엔드는 이 필드를 아예 보내지 않을 수 있으므로
// optional로 둔다.
export type BriefingSectionStatus = 'fresh' | 'cached' | 'stale_fallback' | 'failed'

export interface BriefingSectionMeta {
  // 이번 요청에서 Groq로 새로 생성한 것이 아니라, DB 캐시에서 재사용한
  // 텍스트일 때(입력 해시가 일치했을 때) true가 된다.
  cached: boolean
  generatedAt: string
  // 여러 섹션을 이어붙인 문자열이 아니라 이 섹션 자체의 텍스트 —
  // 각 섹션을 독립적으로 렌더링하고 강조 표시하는 데 쓰인다. 예전에는
  // simple(1문장 요약)도 함께 내려줬지만, 출력 토큰을 줄이기 위해
  // detailed 하나만 생성하도록 단순화했다.
  detailed: string
  status?: BriefingSectionStatus
  // status가 stale_fallback/failed일 때만 채워지는, 안내 문구 분기용
  // 대략적인 카테고리(예: "rate_limit") — 사용자에게 그대로 보여줄 값은
  // 아니다.
  failureReason?: string
}

export interface BriefingSectionsMeta {
  weather: BriefingSectionMeta
  exchange: BriefingSectionMeta
  news: BriefingSectionMeta
}

export interface BriefingData {
  // 각 섹션의 (최대) 두 문장짜리 결과를 순서대로(날씨, 환율, 뉴스)
  // 이어붙인 것.
  detailed: string
  // 모든 섹션이 캐시에서 재사용되었을 때만(이번 요청에서 Groq 호출이
  // 전혀 없었을 때) true다. 섹션별 세부 내역은 briefingMeta 참고.
  cached: boolean
  // RFC3339 타임스탬프 — 세 섹션의 생성 시각 중 가장 최근 것.
  generatedAt: string
  briefingMeta: BriefingSectionsMeta
}

export interface BriefingSection extends SectionMeta {
  data?: BriefingData
}

// 백엔드는 요청 하나당 NDJSON 두 줄을 스트리밍한다: weather/exchange가
// 준비되는 즉시 보내는 "partial" 줄, 그리고 (더 느리고 순차적인) 브리핑
// 단계가 끝나면 보내는 "final" 줄. partial 줄에서는 `briefing`이 의미가
// 없으므로 stage === 'partial'을 "브리핑이 아직 진행 중"으로 취급하면 된다.
//
// 뉴스는 여기 아예 포함되지 않는다 — 뉴스 카드는 GET /api/news를 통해
// 독립적으로 데이터를 가져오므로(useNews.ts 참고), category/region이
// 바뀌어도 이 요청에는 영향을 주지 않는다. 그럼에도 백엔드가
// /api/dashboard 자체에 category/region 쿼리 파라미터를 요구하는 이유는,
// 오직 브리핑의 뉴스 문단이 뉴스 카드가 현재 보여주는 내용과 일치하도록
// 맞추기 위해서다.
export interface DashboardResponse {
  stage: 'partial' | 'final'
  city: string
  from: string
  to: string
  weather: WeatherSection
  exchange: ExchangeSection
  briefing: BriefingSection
  totalMs: number
}

export interface LottoDraw {
  drwNo: number
  drwDate: string
  numbers: number[]
  bonus: number
}

export interface LottoAIInsight {
  // GROQ_API_KEY가 없거나 Groq 호출이 실패했을 때 false가 되며, 이때
  // text에는 사용자에게 보여줄 대체 메시지가 담긴다.
  available: boolean
  text: string
  cached: boolean
  generatedAt?: string
}

// "trend"(핫넘버 우선)/"regression"(콜드넘버 우선)/"uniform"(완전 무작위,
// 기본값) — backend/lotto_recommendation_pipeline.go의 2단계 가중치 정책과
// 이름을 맞췄다.
export type LottoRecommendationMode = 'trend' | 'regression' | 'uniform'

// LottoRecommendationSet 하나가 3단계 패턴 필터를 통과한 뒤의 통계
// 요약이다 — 어떤 세트가 "더 나은" 세트라는 뜻이 아니라, 그 세트가 어떤
// 조건들을 만족했는지 사실을 보여줄 뿐이다.
export interface LottoRecommendationStats {
  oddEvenRatio: string // "홀:짝", 예: "3:3"
  sum: number
  bandDistribution: Record<string, number> // "1-9".."40-45" 5개 구간별 개수
  overlapWithPrevious: number // 직전 회차 당첨번호와 겹치는 개수(0 또는 1)
}

export interface LottoRecommendationSet {
  numbers: number[] // 6개, 오름차순 정렬
  stats: LottoRecommendationStats
}

// 현재 판매 회차에 대한 고정된 추천 세트(isBlackout false)이거나,
// 이번 회차 판매 마감 후 다음 회차 번호가 나오기 전까지 추천이
// 일시 중단되었다는 안내(isBlackout true) 둘 중 하나다. (사이클, 모드)당
// 세트를 정확히 1개만 생성·캐싱한다 — 같은 사이클 안에서 모드를
// uniform -> trend -> uniform으로 오가도 각 모드의 세트가 각각 캐싱되어
// 있다가 그대로 재사용된다.
export interface LottoRecommendation {
  isBlackout: boolean
  mode?: LottoRecommendationMode
  set?: LottoRecommendationSet
  cycleStartDate?: string
  generatedAt?: string
  nextAvailableAt?: string
}

export interface LottoData {
  latest: LottoDraw
  // 최근 회차부터 최신순으로 정렬.
  history: LottoDraw[]
  // 로또 번호(1-45) -> history 전체에서 등장한 횟수.
  // JSON 객체 키는 항상 문자열이므로 Record<string, number>로 표현한다.
  frequency: Record<string, number>
  recentAppeared: number[]
  aiInsight: LottoAIInsight
  recommendation: LottoRecommendation
}

export interface LottoSection extends SectionMeta {
  // 사용자가 로또 카드의 ON/OFF 토글로 데이터 수집을 켜서 백그라운드에서
  // 회차를 채우는 중이면 true. true인 동안 useLotto는 짧은 간격으로 폴링한다.
  isBackfilling: boolean
  data?: LottoData
  // db가 nil이라 섹션 자체가 실패했을 때만 채워진다(백엔드 db.go 참고).
  // "turso_outage"는 Turso 인프라 자체의 장애로 보이는 경우, "connection_failed"는
  // 그 외 일반적인 연결 실패(인증/타임아웃 등)다. 정상일 때는 필드가 아예 없다.
  dbErrorType?: 'turso_outage' | 'connection_failed'
}

// GET /api/lotto/collection/status의 응답 — 로또 카드의 "매주 자동
// 업데이트" 토글이 상태를 보여주는 데 쓴다. 예전에는 초기 50회를 한꺼번에
// 채우는 배치 진행률("42/50 회차 수집됨")이었지만, 이제 자동 수집은 매주
// 최대 1개 회차만 확인하므로 목표치라는 개념이 없다 — 대신 다음 정기
// 점검이 언제고 마지막으로 언제 성공했는지를 보여준다.
export interface LottoCollectionStatus {
  running: boolean
  lastCollectedAt?: string
  lastCheckedAt?: string
  nextCheckAt?: string
  savedCount: number
}

// 'news'는 뉴스 카테고리/지역이 바뀌어 브리핑의 뉴스 문단만 개별적으로
// 다시 합성할 때 쓰인다(useDashboard.ts의 retrySection 참고) — 'briefing'과
// 달리 날씨/환율 문단과 카드 전체 스켈레톤은 건드리지 않는다.
export type SectionKey = 'weather' | 'exchange' | 'briefing' | 'news'

// backend/news.go의 newsCategories와 정확히 일치시킨 것.
export const NEWS_CATEGORY_OPTIONS: { value: string; label: string }[] = [
  { value: 'top', label: '주요' },
  { value: 'business', label: '경제' },
  { value: 'technology', label: '기술' },
  { value: 'sports', label: '스포츠' },
  { value: 'entertainment', label: '연예' },
  { value: 'health', label: '건강' },
  { value: 'science', label: '과학' },
]

export const CITY_OPTIONS: { value: string; label: string }[] = [
  { value: 'seoul', label: '서울' },
  { value: 'daegu', label: '대구' },
  { value: 'busan', label: '부산' },
  { value: 'incheon', label: '인천' },
  { value: 'tokyo', label: '도쿄' },
  { value: 'newyork', label: '뉴욕' },
  { value: 'london', label: '런던' },
]

export const CURRENCY_OPTIONS: string[] = [
  'USD',
  'KRW',
  'EUR',
  'JPY',
  'GBP',
  'CNY',
]
