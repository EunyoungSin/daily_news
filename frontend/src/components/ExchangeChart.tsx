import { Area, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import type { ExchangeRatePoint } from '../types'
import { formatChartDate, formatRate } from '../utils/exchangeFormat'

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

// 마지막(오늘) 데이터 포인트에만 은은한 펄스 링을 그린다 — "지금 이
// 값"이라는 신호를 시각적으로 주는 이 대시보드만의 디테일이다. CSS로
// SVG의 r/opacity 속성을 다루는 대신 SMIL <animate>를 쓰는 이유는, 모든
// 주요 브라우저에서 안정적으로 지원되어 recharts가 매번 새로 그리는
// SVG 안에서도 애니메이션이 끊기지 않기 때문이다.
function ChartDot(props: { cx?: number; cy?: number; index?: number; lastIndex: number }) {
  const { cx, cy, index, lastIndex } = props
  if (cx == null || cy == null) return null

  if (index !== lastIndex) {
    return <circle cx={cx} cy={cy} r={3} fill="var(--accent-exchange)" />
  }

  return (
    <g>
      <circle cx={cx} cy={cy} r={3.5} fill="var(--accent-exchange)" stroke="var(--panel)" strokeWidth={1.5} />
      <circle cx={cx} cy={cy} r={3.5} fill="none" stroke="var(--accent-exchange)" strokeWidth={1.5}>
        <animate attributeName="r" values="3.5;11" dur="1.8s" repeatCount="indefinite" />
        <animate attributeName="opacity" values="0.55;0" dur="1.8s" repeatCount="indefinite" />
      </circle>
    </g>
  )
}

// raw rate가 아니라 displayRate를 기준으로 그린다 — 그래야 KRW->USD
// 쌍도 헤드라인 수치나 "어제" 라인과 같은 익숙한 "~1,449" 스케일로
// 그려지고, 노이즈처럼 보이는 거의 평평한 0.00068~0.00069 라인이
// 되지 않는다.
//
// recharts(및 d3/victory-vendor 등 딸린 의존성)는 무거워서 초기 번들에
// 바로 포함시키지 않는다 — ExchangeCard.tsx에서 React.lazy로 지연
// 로딩하므로 이 컴포넌트는 반드시 default export여야 한다.
export default function ExchangeChart({ weekly }: { weekly: ExchangeRatePoint[] }) {
  const data: ChartPoint[] = weekly.map((p) => ({ date: p.date, label: formatChartDate(p.date), rate: p.displayRate }))
  const lastIndex = data.length - 1

  return (
    <div className="exchange__chart">
      <ResponsiveContainer width="100%" height={100}>
        <LineChart data={data} margin={{ top: 6, right: 20, bottom: 0, left: 4 }}>
          <defs>
            <linearGradient id="exchangeLineGradient" x1="0" y1="0" x2="1" y2="0">
              <stop offset="0%" stopColor="var(--accent-exchange)" stopOpacity={0.55} />
              <stop offset="100%" stopColor="var(--accent-exchange)" stopOpacity={1} />
            </linearGradient>
            <linearGradient id="exchangeAreaGradient" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="var(--accent-exchange)" stopOpacity={0.22} />
              <stop offset="100%" stopColor="var(--accent-exchange)" stopOpacity={0} />
            </linearGradient>
          </defs>
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
          <Area
            type="monotone"
            dataKey="rate"
            stroke="none"
            fill="url(#exchangeAreaGradient)"
            isAnimationActive={false}
          />
          <Line
            type="monotone"
            dataKey="rate"
            stroke="url(#exchangeLineGradient)"
            strokeWidth={2}
            dot={(props: { cx?: number; cy?: number; index?: number }) => (
              <ChartDot key={props.index} {...props} lastIndex={lastIndex} />
            )}
            activeDot={{ r: 5 }}
            isAnimationActive={false}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  )
}
