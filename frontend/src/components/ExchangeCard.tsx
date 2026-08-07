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
  // 이 섹션이 다시 요청되는 동안(전체 새로고침이든, "조회"로 통화만
  // 바꿔 이 섹션만 선택적으로 다시 가져오는 경우든) true. 진행 중에는
  // 통화쌍/기준일/환율값/어제 대비 텍스트와 차트가 함께 스켈레톤으로
  // 바뀐다 — 오래된 텍스트/추세선을 그대로 보여준 채 있으면 지금 보이는
  // 값이 새 통화쌍 기준인지 헷갈리기 때문이다. 둘 다 이 하나의 pending
  // 값만 보고 동시에 바뀌므로, 차트만 먼저 갱신되고 텍스트는 뒤늦게
  // 바뀌는 것처럼 보이는 일이 없다.
  pending: boolean
}

function ExchangeChartSkeleton() {
  return (
    <div className="exchange__chart exchange__chart-skeleton" aria-label="환율 추이 불러오는 중">
      <span className="skeleton-line skeleton-line--row" />
    </div>
  )
}

// pending인 동안 통화쌍/기준일/환율값/어제 대비 텍스트 영역을 대체한다 —
// 차트만 스켈레톤이고 이 텍스트는 그대로 남아있으면, 실제로는 둘 다 같은
// pending을 공유해 함께 갱신되는데도 차트만 먼저 바뀌고 텍스트는 뒤늦게
// 갑자기 바뀌는 것처럼 보였다. card--skeleton 클래스가 있어야
// skeleton-line--number 등 하위 클래스의 크기 규칙이 적용된다
// (WeatherBodySkeleton과 동일한 패턴, App.css 참고).
function ExchangeTextSkeleton() {
  return (
    <div className="exchange__text-skeleton card--skeleton" aria-label="환율 정보 갱신 중">
      <span className="skeleton-line skeleton-line--sm" />
      <span className="skeleton-line skeleton-line--number" />
      <span className="skeleton-line skeleton-line--row" />
    </div>
  )
}

export default function ExchangeCard({ section, onRetry, pending }: Props) {
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
          {pending || retrying ? (
            <ExchangeTextSkeleton />
          ) : (
            <div className="exchange__fade-in">
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
            </div>
          )}

          {pending || retrying ? (
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
