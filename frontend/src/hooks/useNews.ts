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
      // abortRef.current가 이 호출의 controller와 더 이상 같지 않다면, 그
      // 사이 category/region이 다시 바뀌어 새 load() 호출이 이 요청을
      // 이미 대체(abort)한 것이다 — 이 경우 setLoading(false)를 호출하면
      // 안 된다. abort는 비동기로(마이크로태스크 이후) 처리되므로, 새
      // 요청이 setLoading(true)를 호출한 *뒤에* 옛 요청의 이 finally가
      // 실행되어 방금 켜진 로딩 상태를 도로 꺼버리는 경쟁 상태가 실제로
      // 있었다 — 그러면 새 요청이 여전히 진행 중인데도 loading이
      // false로 잘못 표시되어, 스켈레톤도 목록도 아닌 빈 본문이 보였다.
      if (abortRef.current === controller) setLoading(false)
    }
  }, [category, region])

  useEffect(() => {
    load()
    return () => abortRef.current?.abort()
  }, [load])

  return { section, loading, error, retry: load }
}
