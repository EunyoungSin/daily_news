import { useCallback, useEffect, useRef, useState } from 'react'
import type { LottoRecommendationMode, LottoSection } from '../types'

// 로또 섹션은 의도적으로 useDashboard와 분리되어 있다: weather/exchange/news/
// briefing이 공유하는 NDJSON 스트림이 아니라 평범한 JSON GET 요청이며, 자체
// loading/error state를 가지므로 dhlottery 동기화가 느리거나 실패해도 대시보드의
// 나머지 부분을 막거나 망가뜨리지 않는다.
//
// 서버는 최초 50회 수집처럼 오래 걸리는 작업을 백그라운드에서 처리하고,
// 아직 끝나지 않았으면 section.isBackfilling=true로 응답한다. 이 훅은 그
// 동안 짧은 간격으로 재요청해서 완료 여부를 폴링한다.
const BACKFILL_POLL_INTERVAL_MS = 5000

export function useLotto() {
  const [section, setSection] = useState<LottoSection | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // "이번 주 추천 번호"에 쓸 가중치 정책 — 기본값은 trend(핫넘버 우선)다.
  // 화면에서 사용자가 바꾸면 load가 새 mode로 다시 실행된다(아래 useEffect
  // 참고).
  const [mode, setMode] = useState<LottoRecommendationMode>('trend')
  const abortRef = useRef<AbortController | null>(null)
  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const load = useCallback(async () => {
    if (pollTimerRef.current) {
      clearTimeout(pollTimerRef.current)
      pollTimerRef.current = null
    }

    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setLoading(true)
    setError(null)

    try {
      const res = await fetch(`/api/lotto?mode=${mode}`, { signal: controller.signal })
      if (!res.ok) throw new Error(`서버 오류 (status ${res.status})`)
      const data: LottoSection = await res.json()
      setSection(data)

      if (data.isBackfilling) {
        pollTimerRef.current = setTimeout(load, BACKFILL_POLL_INTERVAL_MS)
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') return
      setError(err instanceof Error ? err.message : '알 수 없는 오류가 발생했습니다')
    } finally {
      setLoading(false)
    }
  }, [mode])

  useEffect(() => {
    load()
    return () => {
      abortRef.current?.abort()
      if (pollTimerRef.current) clearTimeout(pollTimerRef.current)
    }
  }, [load])

  return { section, loading, error, retry: load, mode, setMode }
}
