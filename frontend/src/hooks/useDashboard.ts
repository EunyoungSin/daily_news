import { useCallback, useEffect, useRef, useState } from 'react'
import type { DashboardResponse, NewsRegion, SectionKey } from '../types'

// 원래는 15초였다 — 실제 환율/날씨/뉴스 데이터는 그렇게 자주 폴링할
// 만큼 빠르게 바뀌지 않고, 눈에 띄는 이득도 없이 상위 API 쿼터
// (Frankfurter/KMA/NewsData.io)만 소모했다. 10분으로 늘려도 대시보드는
// 충분히 최신 상태를 유지하면서, 백엔드 자체의 응답 캐시
// (weatherFetchCacheTTL/exchangeFetchCacheTTL/newsFetchCacheTTL) 범위
// 안에 여유 있게 들어온다.
const AUTO_REFRESH_INTERVAL_MS = 10 * 60 * 1000

export interface DashboardParams {
  city: string
  from: string
  to: string
}

// 브리핑의 뉴스 문단이 맞춰야 할 뉴스 category/region. 의도적으로
// DashboardParams에는 포함하지 않았다: DashboardParams는 아래의
// "전체 다시 로드" effect를 발동시키지만, category/region 변경은
// weather/exchange는 건드리지 않고 오직 브리핑만
// (retrySection('briefing')을 통해) 새로고침해야 하기 때문이다 — App.tsx 참고.
export interface NewsContext {
  category: string
  region: NewsRegion
}

function buildURL(params: DashboardParams, newsContext: NewsContext): string {
  const search = new URLSearchParams({
    city: params.city,
    from: params.from,
    to: params.to,
    category: newsContext.category,
    region: newsContext.region,
  })
  return `/api/dashboard?${search.toString()}`
}

// NDJSON 응답 본문을 한 줄씩 읽어, 파싱된 DashboardResponse가 도착하는
// 즉시(partial, 그다음 final 순서로) onLine을 호출한다.
async function readStream(
  res: Response,
  onLine: (line: DashboardResponse) => void,
  signal: AbortSignal,
) {
  if (!res.body) {
    onLine(await res.json())
    return
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    if (signal.aborted) return
    const { done, value } = await reader.read()
    if (done) break

    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''

    for (const line of lines) {
      if (!line.trim()) continue
      onLine(JSON.parse(line))
    }
  }

  if (buffer.trim()) {
    onLine(JSON.parse(buffer))
  }
}

// 브리핑 카드 안에서 날씨/환율/뉴스 문단 각각을 개별적으로 로딩 표시할 때
// 쓰인다 — applyParams가 city만 바꿨는지, from/to만 바꿨는지에 따라
// weather/exchange 중 하나(또는 둘 다)만 true가 되고, 뉴스 카테고리/지역이
// 바뀌면 retrySection('news')가 news만 true로 만든다(App.tsx 참고). 세
// 필드 모두 서로 독립적이라, 예를 들어 도시만 바꿨다면 exchange/news는
// 건드리지 않으므로 환율/뉴스 문단은 그대로 유지된 채 보여진다.
export interface BriefingSectionPending {
  weather: boolean
  exchange: boolean
  news: boolean
}

export function useDashboard(initialParams: DashboardParams, newsContext: NewsContext) {
  const [params, setParams] = useState<DashboardParams>(initialParams)
  const [data, setData] = useState<DashboardResponse | null>(null)
  const [loading, setLoading] = useState(false)
  // weatherPending/exchangePending: 각 카드(WeatherCard/ExchangeCard) 자체의
  // 본문 스켈레톤을 켜고 끈다 — partial 줄이 도착하는 즉시(그 섹션의 실제
  // 데이터가 준비된 시점) false로 돌아온다.
  const [weatherPending, setWeatherPending] = useState(false)
  const [exchangePending, setExchangePending] = useState(false)
  // briefingPending: 브리핑 전체가 아직 한 번도 성공한 적이 없거나(첫 로드),
  // 완전히 실패한 뒤 재시도 중이거나, 뉴스 카테고리/지역이 바뀌어 다시
  // 합성하는 동안 카드 전체를 스켈레톤으로 덮는다 — final 줄에서 false.
  const [briefingPending, setBriefingPending] = useState(false)
  // briefingSectionPending: applyParams가 city/currency 변경으로 브리핑의
  // 날씨/환율 문단만 부분적으로 다시 합성하는 동안 쓰는, 문단 단위 로딩
  // 상태다. briefingPending과 달리 나머지 문단(그리고 카드 전체)은 그대로
  // 유지된 채 이 두 문단만 개별적으로 스켈레톤 처리된다. final 줄에서 false.
  const [briefingSectionPending, setBriefingSectionPending] = useState<BriefingSectionPending>({
    weather: false,
    exchange: false,
    news: false,
  })
  const [error, setError] = useState<string | null>(null)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  // 요청을 만드는 시점에 읽히는, 항상 최신 값을 유지하는 category/region
  // — state가 아니라 ref로 둔 이유는 이 값을 바꿔도 아래의 메인 로드
  // effect가 발동하지 않게 하기 위해서다. 값을 바꾼 뒤 새 브리핑을
  // 원하는 호출자는 직접 retrySection('briefing')을 호출해야 한다.
  const newsContextRef = useRef(newsContext)
  newsContextRef.current = newsContext

  // retrySection이 호출 시점의 최신 params를 읽는 데 쓴다 — params
  // state를 직접 클로저로 잡으면 params가 바뀔 때마다 retrySection의
  // 함수 참조 자체가 바뀌어서, App.tsx의 뉴스 category/region effect처럼
  // retrySection을 의존성 배열에 넣은 곳들이 "뉴스가 안 바뀌었는데도"
  // 재실행되어 버린다(applyParams가 params를 바꿀 때마다 브리핑 전체
  // 스켈레톤이 불필요하게 다시 뜨는 문제로 나타났다). ref로 최신 값만
  // 읽게 하면 retrySection은 항상 같은 함수 참조를 유지한다.
  const paramsRef = useRef(params)
  paramsRef.current = params

  // load는 대시보드 전체(날씨+환율+브리핑 3개 문단 모두)를 처음부터 다시
  // 가져온다 — 최초 마운트, "지금 새로고침", 자동 새로고침 타이머가
  // 쓴다. 이 셋은 전부 "모든 게 다시 로드된다"는 것을 사용자에게 그대로
  // 보여주는 게 맞으므로, 관련된 pending 플래그를 전부 한꺼번에 켠다 —
  // city/currency 중 하나만 바뀐 부분 새로고침(applyParams 참고)과 달리
  // 여기서는 일부만 켜야 할 이유가 없다.
  const load = useCallback(async (p: DashboardParams) => {
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setLoading(true)
    setWeatherPending(true)
    setExchangePending(true)
    setBriefingPending(true)
    setBriefingSectionPending({ weather: false, exchange: false, news: false })
    setError(null)

    try {
      const res = await fetch(buildURL(p, newsContextRef.current), { signal: controller.signal })
      if (!res.ok) throw new Error(`서버 오류 (status ${res.status})`)

      await readStream(
        res,
        (line) => {
          setData(line)
          setLoading(false)
          setWeatherPending(false)
          setExchangePending(false)
          if (line.stage === 'final') setBriefingPending(false)
        },
        controller.signal,
      )
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') return
      setError(err instanceof Error ? err.message : '알 수 없는 오류가 발생했습니다')
      setLoading(false)
      setWeatherPending(false)
      setExchangePending(false)
      setBriefingPending(false)
    }
  }, [])

  // 최초 마운트 시 한 번만 전체 로드한다. 이후 city/from/to가 바뀌는
  // 경로는 이 effect가 아니라 applyParams가 전담한다 — applyParams는
  // 값이 실제로 바뀐 섹션만 선택적으로 다시 가져오므로, params가 바뀔
  // 때마다 이 effect가 또 전체를 다시 로드해버리면 applyParams의 선택적
  // 갱신과 완전히 같은 요청이 중복으로 나간다.
  useEffect(() => {
    load(initialParams)
    return () => abortRef.current?.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load])

  useEffect(() => {
    if (!autoRefresh) return
    const id = setInterval(() => load(params), AUTO_REFRESH_INTERVAL_MS)
    return () => clearInterval(id)
  }, [autoRefresh, params, load])

  // 백엔드는 통합된 엔드포인트 하나만 제공하므로, 섹션 단위 재시도는
  // 대시보드 전체를 다시 가져오되 해당 섹션만 state에 병합하고 이미
  // 로드된 다른 섹션은 건드리지 않는다. 뉴스 category/region 변경이
  // 브리핑에 반영되는 경로도 바로 이것이다: App.tsx는 `params`를 바꾸는
  // 대신 retrySection('news')를 호출하므로, 뉴스 카드의 필터가
  // 바뀌었다고 weather/exchange까지 다시 로드되는 일은 없다 — 'news'는
  // applyParams의 city/currency 부분 갱신과 같은 원리로, 브리핑의 뉴스
  // 문단만 개별 병합한다(아래 참고).
  const retrySection = useCallback(async (section: SectionKey) => {
    // 초기 로드 때 쓰는 것과 같은 "브리핑 합성 중" 스켈레톤을 띄운다 —
    // BriefingCard의 "재시도" 버튼(완전 실패 후 카드 전체를 다시 가져올
    // 때)에서만 쓰인다. 뉴스 category/region 변경은 카드 전체가 아니라
    // news 문단만 개별적으로 pending 처리한다(아래 'news' 분기 참고) —
    // 날씨/환율 문단까지 스켈레톤으로 덮을 이유가 없다.
    if (section === 'briefing') setBriefingPending(true)
    // 이게 없으면, 뉴스 category/region 변경이 아무 표시도 없이 조용히
    // 뉴스 문장만 바꿔치기했고, 그 문단에만 잠깐 나타나는 2.5초짜리 배경
    // 색조 변화 정도가 유일한 단서였다. 명시적인 pending 상태를 두면
    // 탭을 클릭한 순간 "뉴스 문단이 다시 합성되고 있다"는 것이 확실히
    // 드러난다.
    if (section === 'news') setBriefingSectionPending((prev) => ({ ...prev, news: true }))

    try {
      const res = await fetch(buildURL(paramsRef.current, newsContextRef.current))
      if (!res.ok) throw new Error(`서버 오류 (status ${res.status})`)

      await readStream(
        res,
        (line) => {
          // 'briefing'/'news' 둘 다 브리핑 합성이 끝나야 의미가 있는
          // 값이므로 partial 줄은 무시한다 — weather/exchange는 partial
          // 줄에서 이미 최종값이라 이 게이트가 필요 없다.
          if ((section === 'briefing' || section === 'news') && line.stage !== 'final') return

          if (section === 'news') {
            // applyParams의 city/currency 부분 갱신과 정확히 같은
            // 패턴이다: 날씨/환율 문단(과 그 briefingMeta)은 이전 값을
            // 그대로 유지하고, 뉴스 문단만 새로 받은 값으로 교체한다.
            setData((prev) => {
              if (!prev) return line
              const prevData = prev.briefing.data
              const lineData = line.briefing.data
              return {
                ...prev,
                totalMs: line.totalMs,
                briefing: {
                  ...line.briefing,
                  data:
                    prevData && lineData
                      ? {
                          ...lineData,
                          briefingMeta: {
                            weather: prevData.briefingMeta.weather,
                            exchange: prevData.briefingMeta.exchange,
                            news: lineData.briefingMeta.news,
                          },
                        }
                      : lineData,
                },
              }
            })
            return
          }

          setData((prev) => (prev ? { ...prev, [section]: line[section], totalMs: line.totalMs } : line))
        },
        new AbortController().signal,
      )
    } finally {
      if (section === 'briefing') setBriefingPending(false)
      if (section === 'news') setBriefingSectionPending((prev) => ({ ...prev, news: false }))
    }
  }, [])

  // "조회" 버튼이 호출하는, city/currency 변경 전용 선택적 재요청이다.
  // newParams를 현재 적용된 params와 값 단위로 비교해 city가 바뀌었는지
  // (cityChanged), from/to가 바뀌었는지(currencyChanged)를 먼저 판단하고,
  // 바뀐 쪽에 해당하는 pending 플래그만 켠다 — 예를 들어 도시만 바꿨다면
  // exchangePending/briefingSectionPending.exchange는 건드리지 않으므로
  // ExchangeCard와 브리핑의 환율 문단은 그대로 유지된 채 보여진다.
  //
  // 백엔드는 요청 하나에 날씨+환율+브리핑을 함께 스트리밍하므로(스코프를
  // 좁혀 일부만 계산하게 하는 파라미터가 없다), 네트워크 요청 자체는
  // 항상 전체를 다시 계산해 돌려준다 — 다만 city/currency가 그대로인
  // 섹션은 원본 데이터 캐시(raw_data_cache)와 브리핑 문단 캐시
  // (briefing_section_cache)에 그대로 걸려 사실상 동일한 값을 그대로
  // 돌려받을 뿐이다. 이 함수는 그 응답에서 실제로 바뀐 필드만 state에
  // 반영하고 나머지는 이전 값을 그대로 유지해, 화면상으로는 바뀐 섹션만
  // 정확히 로딩되는 것처럼 보이게 한다.
  const applyParams = useCallback(async (newParams: DashboardParams) => {
    const current = paramsRef.current
    const cityChanged = newParams.city !== current.city
    const currencyChanged = newParams.from !== current.from || newParams.to !== current.to
    if (!cityChanged && !currencyChanged) return

    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setParams(newParams)
    setError(null)
    setLoading(true)
    if (cityChanged) {
      setWeatherPending(true)
      setBriefingSectionPending((prev) => ({ ...prev, weather: true }))
    }
    if (currencyChanged) {
      setExchangePending(true)
      setBriefingSectionPending((prev) => ({ ...prev, exchange: true }))
    }

    try {
      const res = await fetch(buildURL(newParams, newsContextRef.current), { signal: controller.signal })
      if (!res.ok) throw new Error(`서버 오류 (status ${res.status})`)

      await readStream(
        res,
        (line) => {
          setData((prev) => {
            if (!prev) return line
            const next: DashboardResponse = { ...prev, totalMs: line.totalMs }
            if (cityChanged) next.weather = line.weather
            if (currencyChanged) next.exchange = line.exchange

            if (line.stage === 'final') {
              const prevData = prev.briefing.data
              const lineData = line.briefing.data
              next.briefing = {
                ...line.briefing,
                data:
                  prevData && lineData
                    ? {
                        ...lineData,
                        briefingMeta: {
                          weather: cityChanged ? lineData.briefingMeta.weather : prevData.briefingMeta.weather,
                          exchange: currencyChanged ? lineData.briefingMeta.exchange : prevData.briefingMeta.exchange,
                          news: prevData.briefingMeta.news,
                        },
                      }
                    : lineData,
              }
            }

            return next
          })

          setLoading(false)
          if (cityChanged) setWeatherPending(false)
          if (currencyChanged) setExchangePending(false)
          if (line.stage === 'final') {
            setBriefingSectionPending((prev) => ({
              ...prev,
              weather: cityChanged ? false : prev.weather,
              exchange: currencyChanged ? false : prev.exchange,
            }))
          }
        },
        controller.signal,
      )
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') return
      setError(err instanceof Error ? err.message : '알 수 없는 오류가 발생했습니다')
      setLoading(false)
      if (cityChanged) {
        setWeatherPending(false)
        setBriefingSectionPending((prev) => ({ ...prev, weather: false }))
      }
      if (currencyChanged) {
        setExchangePending(false)
        setBriefingSectionPending((prev) => ({ ...prev, exchange: false }))
      }
    }
  }, [])

  // briefingInFlight: AI 브리핑 3섹션(날씨/환율/뉴스) 중 하나라도 아직
  // 생성이 끝나지 않았음을 나타내는 단일 플래그다. briefingSectionPending의
  // 세 필드뿐 아니라 briefingPending도 포함해야 한다 — 최초 로드나 "재시도"
  // 버튼(load)을 눌렀을 때는 3섹션 전체가 캐시 없이 한꺼번에 새로
  // 생성되는데, 이 경로는 briefingSectionPending의 개별 필드를 전혀
  // 건드리지 않고 briefingPending 하나로만 "지금 합성 중"임을 나타내기
  // 때문이다(load 참고). briefingPending을 빼면, 정작 3개의 Groq 호출이
  // 동시에 나가는 가장 위험한 순간(캐시 미스 상태의 최초 로드)에 이
  // 플래그가 여전히 false로 남아 뉴스 탭 잠금이 무력화된다.
  const briefingInFlight =
    briefingPending ||
    briefingSectionPending.weather ||
    briefingSectionPending.exchange ||
    briefingSectionPending.news

  return {
    data,
    loading,
    weatherPending,
    exchangePending,
    briefingPending,
    briefingSectionPending,
    briefingInFlight,
    error,
    params,
    applyParams,
    refresh: () => load(params),
    retrySection,
    autoRefresh,
    setAutoRefresh,
  }
}
