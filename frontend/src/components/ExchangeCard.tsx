import { lazy, Suspense, useState } from 'react'
import type { ExchangeSection } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'
import { formatRate } from '../utils/exchangeFormat'

// recharts(+d3/victory-vendor 등 무거운 의존성 트리)는 대시보드 첫 화면에
// 곧바로 필요하지 않으므로(데이터 로딩 중에는 스켈레톤만 보임) 지연
// 로딩한다 — 이걸로 recharts가 vendor-react/main 청크에서 완전히 빠져
// 별도 청크로 분리된다(vite.config.ts의 manualChunks 참고).
const ExchangeChart = lazy(() => import('./ExchangeChart'))

interface Props {
  section: ExchangeSection
  onRetry: () => Promise<void>
  // 대시보드 새로고침이 진행 중인 동안 true. 새로고침 중에도 카드의
  // 나머지 부분은 마지막으로 알려진 값을 그대로 보여주지만(새 응답이
  // 도착하기 전까지 데이터를 지우지 않으므로), 차트만은 스켈레톤으로
  // 바뀐다 — 오래된 추세선은 오래된 단일 수치보다 더 오해를 주기
  // 때문이다.
  loading: boolean
}

function ExchangeChartSkeleton() {
  return (
    <div className="exchange__chart exchange__chart-skeleton" aria-label="환율 추이 불러오는 중">
      <span className="skeleton-line skeleton-line--row" />
    </div>
  )
}

export default function ExchangeCard({ section, onRetry, loading }: Props) {
  const [retrying, setRetrying] = useState(false)
  const pulsing = usePulseOnChange(section.durationMs)

  const handleRetry = async () => {
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }

  const yesterday = section.data?.yesterday
  const rising = (yesterday?.changePercent ?? 0) > 0
  const falling = (yesterday?.changePercent ?? 0) < 0
  // displayLabel은 "1 {base} = " 형태다 — 숫자 뒤에 오는 통화는
  // from/to 중 그 라벨에 이미 이름이 언급되지 않은 쪽이다.
  const quoteCurrency =
    section.data && section.data.displayLabel.includes(section.data.to) ? section.data.from : section.data?.to

  return (
    <section className="card card--exchange">
      <header className="card__header">
        <h2 className="card__title">환율</h2>
        <span className="card__duration">
          <span className={pulsing ? 'card__duration-dot card__duration-dot--pulse' : 'card__duration-dot'} />
          {section.durationMs}ms
        </span>
      </header>

      {section.success && section.data ? (
        <div className="card__body exchange__body">
          <div className="exchange__content">
          <div className="exchange__meta-row">
            <div className="exchange__pair">
              {section.data.from} → {section.data.to}
            </div>
            <div className="exchange__date">기준일 {section.data.current.date}</div>
          </div>

          <div className="exchange__rate">
            {formatRate(section.data.displayRate)}
          </div>

          <div className="exchange__hint-row">
            <div className="exchange__hint">
              {section.data.displayLabel}
              {formatRate(section.data.displayRate)} {quoteCurrency}
            </div>

            {yesterday && (
              <div className={rising ? 'exchange__yesterday exchange__yesterday--rise' : falling ? 'exchange__yesterday exchange__yesterday--fall' : 'exchange__yesterday'}>
                <span className="exchange__yesterday-arrow" aria-hidden="true">
                  {rising ? '▲' : falling ? '▼' : '–'}
                </span>
                어제: {formatRate(yesterday.displayRate)} ({rising ? '+' : ''}
                {yesterday.changePercent}%)
              </div>
            )}
          </div>

          {loading || retrying ? (
            <ExchangeChartSkeleton />
          ) : section.data.weekly && section.data.weekly.length > 0 ? (
            <Suspense fallback={<ExchangeChartSkeleton />}>
              <ExchangeChart weekly={section.data.weekly} />
            </Suspense>
          ) : (
            <p className="exchange__chart-empty">최근 7일 추이 데이터를 사용할 수 없습니다</p>
          )}
          </div>
        </div>
      ) : (
        <div className="card__body card__error">
          <p>⚠️ 환율 정보를 불러오지 못했습니다</p>
          <button type="button" onClick={handleRetry} disabled={retrying}>
            {retrying ? '재시도 중…' : '재시도'}
          </button>
        </div>
      )}
    </section>
  )
}
