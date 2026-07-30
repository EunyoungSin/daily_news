import { useMemo } from 'react'

interface Props {
  frequency: Record<string, number>
  historyLength: number
}

// 번호별 출현 횟수 히트맵 — 스크롤을 내려야 보이고 45칸을 매번 다시
// 계산/렌더링하는 비용도 있어 LottoCard.tsx에서 React.lazy로 지연 로딩한다.
export default function LottoHeatmap({ frequency, historyLength }: Props) {
  const freqEntries = useMemo(() => {
    return Array.from({ length: 45 }, (_, i) => i + 1).map((n) => ({
      n,
      count: frequency[String(n)] ?? 0,
    }))
  }, [frequency])

  const maxCount = useMemo(() => Math.max(1, ...freqEntries.map((f) => f.count)), [freqEntries])
  const minCount = useMemo(() => Math.min(...freqEntries.map((f) => f.count)), [freqEntries])

  return (
    <div className="lotto__section">
      <h3 className="lotto__section-title">번호별 출현 횟수 (최근 {historyLength}회)</h3>
      <div className="lotto__heatmap">
        {freqEntries.map(({ n, count }) => {
          const ratio = maxCount === minCount ? 0.5 : (count - minCount) / (maxCount - minCount)
          const mix = Math.round(12 + ratio * 78)
          return (
            <div
              key={n}
              className="lotto__heat-cell"
              style={{ background: `color-mix(in srgb, var(--accent-lotto) ${mix}%, var(--panel-sunken))` }}
              title={`${n}번: ${count}회`}
            >
              <span className="lotto__heat-num">{n}</span>
              <span className="lotto__heat-count">{count}</span>
            </div>
          )
        })}
      </div>
      <div className="lotto__heat-legend">
        <span>적음</span>
        <span className="lotto__heat-gradient" aria-hidden="true" />
        <span>많음</span>
      </div>
    </div>
  )
}
