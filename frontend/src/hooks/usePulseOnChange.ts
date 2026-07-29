import { useEffect, useRef, useState } from 'react'

// `value`가 바뀔 때마다 잠깐 true로 바뀌었다가 다시 false로 돌아온다. 각 카드의
// duration 배지가 계속 반복 애니메이션을 도는 대신, 해당 섹션의 데이터가 실제로
// 갱신되는 순간에만 한 번 부드럽게 pulse 하도록 만드는 데 쓰인다.
export function usePulseOnChange(value: string | number | undefined, durationMs = 900): boolean {
  const [pulsing, setPulsing] = useState(false)
  const prev = useRef(value)

  useEffect(() => {
    if (prev.current === value) return
    prev.current = value

    setPulsing(true)
    const id = setTimeout(() => setPulsing(false), durationMs)
    return () => clearTimeout(id)
  }, [value, durationMs])

  return pulsing
}
