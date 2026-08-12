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

// 추천 방식을 바꾼 요청은 서버 캐시가 있으면(같은 사이클 안에서 이미 조회한
// 모드로 되돌아가는 경우) 왕복이 수십ms 안에 끝날 수 있다 — 스켈레톤이
// 그만큼 짧게 떴다 사라지면 로딩 표시라기보다 화면이 깜빡이는 것처럼
// 보인다. recommendationPending을 최소 이 시간만큼은 유지해서, 캐시
// 히트든 신규 생성이든 전환이 항상 눈에 보이는 로딩 단계를 거치는
// 것처럼 느껴지게 한다.
const MIN_RECOMMENDATION_PENDING_MS = 300

export function useLotto() {
  const [section, setSection] = useState<LottoSection | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // "이번 주 추천 번호"에 쓸 가중치 정책 — 기본값은 trend(핫넘버 우선)다.
  // 화면에서 사용자가 바꾸면 load가 새 mode로 다시 실행된다(아래 useEffect
  // 참고).
  const [mode, setMode] = useState<LottoRecommendationMode>('trend')
  // recommendationPending: 사용자가 추천 방식 드롭다운을 바꿔서 재조회하는
  // 동안만 true다(changeRecommendationMode 참고) — 최초 마운트 로딩이나
  // "재시도" 버튼(retry=load 직접 호출)에는 반응하지 않는다. 그 둘은 이미
  // loading && !section(카드 전체 스켈레톤) 또는 카드 전체 에러 화면으로
  // 표시되므로, 추천 영역만 따로 가릴 필요가 없다.
  const [recommendationPending, setRecommendationPending] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // 이 값이 null이 아니면 "지금 진행 중인 load()가 추천 방식 변경으로
  // 시작됐다"는 뜻이고, 값 자체는 그 요청이 시작된 시각이다 — 응답이
  // 도착했을 때 MIN_RECOMMENDATION_PENDING_MS를 채웠는지 계산하는 데 쓴다.
  const recommendationChangeStartRef = useRef<number | null>(null)

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
      // abortRef.current가 이 호출의 controller와 더 이상 같지 않다면, 그
      // 사이 mode가 다시 바뀌어 새 load() 호출이 이 요청을 이미
      // 대체(abort)한 것이다 — useNews.ts의 같은 패턴과 동일한 이유로,
      // 이 경우 loading/recommendationPending을 건드리면 안 된다. abort는
      // 비동기로 처리되므로, 새 요청이 setLoading(true)를 호출한 뒤에
      // 옛 요청의 이 finally가 실행되어 방금 켜진 로딩 상태를 도로
      // 꺼버리는 경쟁 상태를 막기 위해서다.
      if (abortRef.current === controller) {
        setLoading(false)

        const startedAt = recommendationChangeStartRef.current
        if (startedAt != null) {
          recommendationChangeStartRef.current = null
          const elapsed = Date.now() - startedAt
          const remaining = MIN_RECOMMENDATION_PENDING_MS - elapsed
          if (remaining <= 0) {
            setRecommendationPending(false)
          } else {
            setTimeout(() => {
              // 이 타임아웃이 실행되는 시점에도 여전히 "현재" 요청이어야만
              // 스켈레톤을 끈다 — 지연 도중 사용자가 다시 모드를 바꿨다면
              // 그 새 요청이 자기 자신의 타이밍을 따로 관리한다.
              if (abortRef.current === controller) setRecommendationPending(false)
            }, remaining)
          }
        }
      }
    }
  }, [mode])

  useEffect(() => {
    load()
    return () => {
      abortRef.current?.abort()
      if (pollTimerRef.current) clearTimeout(pollTimerRef.current)
    }
  }, [load])

  // 드롭다운 onChange는 setMode를 직접 쓰지 않고 이 함수를 거쳐야 한다 —
  // recommendationPending을 켜는 지점이 바로 여기이기 때문이다(최초
  // 마운트나 retry()는 이 함수를 거치지 않으므로 recommendationPending에
  // 영향을 주지 않는다).
  const changeRecommendationMode = useCallback((newMode: LottoRecommendationMode) => {
    recommendationChangeStartRef.current = Date.now()
    setRecommendationPending(true)
    setMode(newMode)
  }, [])

  return {
    section,
    loading,
    error,
    retry: load,
    mode,
    setMode: changeRecommendationMode,
    recommendationPending,
  }
}
