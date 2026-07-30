import { useCallback, useEffect, useRef, useState } from 'react'
import type { LottoCollectionStatus } from '../types'

// 로또 카드의 ON/OFF 토글이 사용하는 훅 — 실행 상태를 가져오고, ON인 동안
// 5초 간격으로 진행 상황("42/50 회차 수집됨")을 폴링하며, start/stop
// 액션을 제공한다. 실제 데이터(useLotto)와는 별개의 관심사다: 이 훅은
// "지금 수집이 돌고 있는가"만 알면 되고, 회차 데이터 자체는 다루지 않는다.
const STATUS_POLL_INTERVAL_MS = 5000

export function useLottoCollection() {
  const [status, setStatus] = useState<LottoCollectionStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const pollTimerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const fetchStatus = useCallback(async () => {
    try {
      const res = await fetch('/api/lotto/collection/status')
      if (!res.ok) return
      const data: LottoCollectionStatus = await res.json()
      setStatus(data)
    } catch {
      // 상태 폴링 실패는 조용히 무시한다 — 다음 폴링 주기에 다시 시도한다.
    }
  }, [])

  useEffect(() => {
    fetchStatus()
  }, [fetchStatus])

  // running인 동안에만 폴링한다 — 꺼져 있을 때는 상태가 바뀔 일이 없으므로
  // 굳이 서버를 계속 두드리지 않는다.
  useEffect(() => {
    if (!status?.running) return
    pollTimerRef.current = setInterval(fetchStatus, STATUS_POLL_INTERVAL_MS)
    return () => {
      if (pollTimerRef.current) clearInterval(pollTimerRef.current)
    }
  }, [status?.running, fetchStatus])

  const start = useCallback(async () => {
    setBusy(true)
    try {
      await fetch('/api/lotto/collection/start', { method: 'POST' })
      await fetchStatus()
    } finally {
      setBusy(false)
    }
  }, [fetchStatus])

  const stop = useCallback(async () => {
    setBusy(true)
    try {
      await fetch('/api/lotto/collection/stop', { method: 'POST' })
      await fetchStatus()
    } finally {
      setBusy(false)
    }
  }, [fetchStatus])

  return { status, busy, start, stop }
}
