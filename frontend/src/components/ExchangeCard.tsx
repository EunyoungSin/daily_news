import { useState } from 'react'
import { Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import type { ExchangeRatePoint, ExchangeSection } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'

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

// "YYYY-MM-DD" -> "MM/DD" — 좁은 카드 안 7개 포인트짜리 축에 들어갈
// 만큼 짧게 줄인다.
function formatChartDate(date: string): string {
  const parts = date.split('-')
  return parts.length === 3 ? `${parts[1]}/${parts[2]}` : date
}

// 백엔드의 displayRate는 항상 rate의 "보기 편한" 쪽이다 — 이미 1 이상이면
// 원래 값 그대로, 아니면 그 역수(항상 1보다 큼)를 쓴다
// (backend/exchange.go의 computeExchangeDisplay 참고) — 그래서 프론트엔드는
// 여기서 소수점 2자리 고정, 콤마 구분 포맷만 쓰면 되고, 0.00069 같은
// 1 미만 원본 rate에 필요한 동적 소수 자릿수 조정은 필요 없다.
function formatRate(value: number): string {
  return value.toLocaleString('ko-KR', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

interface ChartPoint {
  date: string
  label: string
  rate: number
}

function ExchangeChartTooltip({ active, payload }: { active?: boolean; payload?: Array<{ payload: ChartPoint }> }) {
  if (!active || !payload?.length) return null
  const point = payload[0].payload
  return (
    <div className="exchange__chart-tooltip">
      <div className="exchange__chart-tooltip-date">{point.date}</div>
      <div className="exchange__chart-tooltip-rate">{formatRate(point.rate)}</div>
    </div>
  )
}

// raw rate가 아니라 displayRate를 기준으로 그린다 — 그래야 KRW->USD
// 쌍도 헤드라인 수치나 "어제" 라인과 같은 익숙한 "~1,449" 스케일로
// 그려지고, 노이즈처럼 보이는 거의 평평한 0.00068~0.00069 라인이
// 되지 않는다.
function ExchangeChart({ weekly }: { weekly: ExchangeRatePoint[] }) {
  const data: ChartPoint[] = weekly.map((p) => ({ date: p.date, label: formatChartDate(p.date), rate: p.displayRate }))

  return (
    <div className="exchange__chart">
      <ResponsiveContainer width="100%" height={100}>
        <LineChart data={data} margin={{ top: 6, right: 20, bottom: 0, left: 4 }}>
          <XAxis
            dataKey="label"
            tick={{ fontSize: 10, fill: 'var(--text-faint)' }}
            axisLine={{ stroke: 'var(--panel-border)' }}
            tickLine={false}
          />
          <YAxis
            domain={['auto', 'auto']}
            tick={{ fontSize: 10, fill: 'var(--text-faint)' }}
            axisLine={false}
            tickLine={false}
            width={44}
            tickFormatter={(v: number) => v.toLocaleString('ko-KR', { maximumFractionDigits: 0 })}
          />
          <Tooltip content={<ExchangeChartTooltip />} cursor={{ stroke: 'var(--panel-border-strong)' }} />
          <Line
            type="monotone"
            dataKey="rate"
            stroke="var(--accent-exchange)"
            strokeWidth={2}
            dot={{ r: 3, fill: 'var(--accent-exchange)', strokeWidth: 0 }}
            activeDot={{ r: 5 }}
            isAnimationActive={false}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  )
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
            <ExchangeChart weekly={section.data.weekly} />
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
