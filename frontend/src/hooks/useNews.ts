import { useCallback, useEffect, useRef, useState } from 'react'
import type { NewsRegion, NewsSection } from '../types'

// 뉴스 섹션은 의도적으로 useDashboard와 분리되어 있다: 자체적인 GET /api/news
// 요청과 자체 loading/error state를 가지므로 category나 region을 바꿔도
// weather/exchange/briefing에는 전혀 영향을 주지 않는다. 다른 섹션들과 달리
// 여기에는 주기적인 자동 새로고침이 없는데, NewsData.io 무료 요금제가 하루
// 200 크레딧뿐이라 실제로 category/region이 바뀌거나(또는 명시적으로 재시도할
// 때만) 다시 요청하도록 의도적으로 설계한 것이지 빠뜨린 게 아니다.
export function useNews(category: string, region: NewsRegion) {
  const [section, setSection] = useState<NewsSection | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const load = useCallback(async () => {
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setLoading(true)
    setError(null)

    try {
      const search = new URLSearchParams({ category, region })
      const res = await fetch(`/api/news?${search.toString()}`, { signal: controller.signal })
      if (!res.ok) throw new Error(`서버 오류 (status ${res.status})`)
      const data: NewsSection = await res.json()
      setSection(data)
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') return
      setError(err instanceof Error ? err.message : '알 수 없는 오류가 발생했습니다')
    } finally {
      setLoading(false)
    }
  }, [category, region])

  useEffect(() => {
    load()
    return () => abortRef.current?.abort()
  }, [load])

  return { section, loading, error, retry: load }
}
