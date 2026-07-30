import { Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
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
