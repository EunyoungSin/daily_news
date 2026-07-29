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

export function useDashboard(initialParams: DashboardParams, newsContext: NewsContext) {
  const [params, setParams] = useState<DashboardParams>(initialParams)
  const [data, setData] = useState<DashboardResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [briefingPending, setBriefingPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  // 요청을 만드는 시점에 읽히는, 항상 최신 값을 유지하는 category/region
  // — state가 아니라 ref로 둔 이유는 이 값을 바꿔도 아래의 메인 로드
  // effect가 발동하지 않게 하기 위해서다. 값을 바꾼 뒤 새 브리핑을
  // 원하는 호출자는 직접 retrySection('briefing')을 호출해야 한다.
  const newsContextRef = useRef(newsContext)
  newsContextRef.current = newsContext

  const load = useCallback(async (p: DashboardParams) => {
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setLoading(true)
    setBriefingPending(true)
    setError(null)

    try {
      const res = await fetch(buildURL(p, newsContextRef.current), { signal: controller.signal })
      if (!res.ok) throw new Error(`서버 오류 (status ${res.status})`)

      await readStream(
        res,
        (line) => {
          setData(line)
          setLoading(false)
          if (line.stage === 'final') setBriefingPending(false)
        },
        controller.signal,
      )
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') return
      setError(err instanceof Error ? err.message : '알 수 없는 오류가 발생했습니다')
      setLoading(false)
      setBriefingPending(false)
    }
  }, [])

  useEffect(() => {
    load(params)
    return () => abortRef.current?.abort()
  }, [params, load])

  useEffect(() => {
    if (!autoRefresh) return
    const id = setInterval(() => load(params), AUTO_REFRESH_INTERVAL_MS)
    return () => clearInterval(id)
  }, [autoRefresh, params, load])

  // 백엔드는 통합된 엔드포인트 하나만 제공하므로, 섹션 단위 재시도는
  // 대시보드 전체를 다시 가져오되 해당 섹션만 state에 병합하고 이미
  // 로드된 다른 섹션은 건드리지 않는다. 뉴스 category/region 변경이
  // 브리핑에 반영되는 경로도 바로 이것이다: App.tsx는 `params`를
  // 바꾸는 대신 retrySection('briefing')을 호출하므로, 뉴스 카드의
  // 필터가 바뀌었다고 weather/exchange까지 다시 로드되는 일은 없다.
  const retrySection = useCallback(async (section: SectionKey) => {
    // 초기 로드 때 쓰는 것과 같은 "브리핑 합성 중" 스켈레톤을 띄운다 —
    // 이게 없으면, 뉴스 category/region 변경이(바로 위 주석에서 설명한
    // 경로를 통해 브리핑에 도달할 때) 아무 표시도 없이 조용히 뉴스 문장만
    // 바꿔치기했고, 그 문단에만 잠깐 나타나는 2.5초짜리 배경 색조 변화
    // 정도가 유일한 단서였다. 명시적인 pending 상태를 두면 탭을 클릭한
    // 순간 "지금 브리핑이 다시 생성되고 있다"는 것이 확실히 드러난다.
    if (section === 'briefing') setBriefingPending(true)
    try {
      const res = await fetch(buildURL(params, newsContextRef.current))
      if (!res.ok) throw new Error(`서버 오류 (status ${res.status})`)

      await readStream(
        res,
        (line) => {
          if (section === 'briefing' && line.stage !== 'final') return
          setData((prev) => (prev ? { ...prev, [section]: line[section], totalMs: line.totalMs } : line))
        },
        new AbortController().signal,
      )
    } finally {
      if (section === 'briefing') setBriefingPending(false)
    }
  }, [params])

  return {
    data,
    loading,
    briefingPending,
    error,
    params,
    setParams,
    refresh: () => load(params),
    retrySection,
    autoRefresh,
    setAutoRefresh,
  }
}
