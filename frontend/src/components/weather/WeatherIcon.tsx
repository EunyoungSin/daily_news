type WeatherKind = 'clear' | 'partly-cloudy' | 'cloudy' | 'fog' | 'rain' | 'snow' | 'thunderstorm'

// Open-Meteo/기상청 WMO 날씨 코드를 아이콘 종류로 묶는다 — 이전 이모지 버전
// (weatherGlyph)과 동일한 구간 경계를 그대로 유지해, 어떤 코드가 어떤
// 아이콘으로 보이는지는 바뀌지 않고 그림만 바뀐다. 51~86 구간은
// 이슬비/비/소나기를 모두 "비" 아이콘 하나로, 71~86 중 눈 계열은 "눈"
// 아이콘으로 묶는다.
function weatherKind(code: number): WeatherKind {
  if (code === 0) return 'clear'
  if (code === 1 || code === 2) return 'partly-cloudy'
  if (code === 3) return 'cloudy'
  if (code === 45 || code === 48) return 'fog'
  if (code >= 51 && code <= 67) return 'rain'
  if (code >= 71 && code <= 77) return 'snow'
  if (code >= 80 && code <= 82) return 'rain'
  if (code === 85 || code === 86) return 'snow'
  if (code === 95 || code === 96 || code === 99) return 'thunderstorm'
  return 'cloudy'
}

// 모든 아이콘이 공유하는 구름 실루엣 — line + duotone 스타일(윤곽선은
// currentColor 실선, 몸체는 currentColor를 옅게 채워 은은한 입체감)로
// 통일한다. 실제 색상은 렌더링하는 쪽의 CSS color 값을 그대로 물려받으므로
// (WeatherCard.tsx에서 --accent-weather를 지정) 다크/라이트 테마 전환 시
// 별도 처리가 필요 없다.
function Cloud({ y = 11.5 }: { y?: number }) {
  return (
    <path
      d={`M17.5 ${y}H8a4 4 0 0 1-.6-7.96A5.4 5.4 0 0 1 18 ${y - 6.4}a4 4 0 0 1-.5 6.4Z`}
      fill="currentColor"
      fillOpacity={0.18}
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinejoin="round"
    />
  )
}

const ICONS: Record<WeatherKind, React.ReactNode> = {
  clear: (
    <>
      <circle cx={12} cy={12} r={5} fill="currentColor" fillOpacity={0.22} stroke="currentColor" strokeWidth={1.6} />
      <g stroke="currentColor" strokeWidth={1.6} strokeLinecap="round">
        <path d="M12 1.5v2.4" />
        <path d="M12 20.1v2.4" />
        <path d="M4.2 4.2l1.7 1.7" />
        <path d="M18.1 18.1l1.7 1.7" />
        <path d="M1.5 12h2.4" />
        <path d="M20.1 12h2.4" />
        <path d="M4.2 19.8l1.7-1.7" />
        <path d="M18.1 5.9l1.7-1.7" />
      </g>
    </>
  ),
  'partly-cloudy': (
    <>
      <circle cx={8.6} cy={8} r={3.4} fill="currentColor" fillOpacity={0.22} stroke="currentColor" strokeWidth={1.5} />
      <g stroke="currentColor" strokeWidth={1.5} strokeLinecap="round">
        <path d="M8.6 2.2v1.6" />
        <path d="M3.8 3.9l1.2 1.2" />
        <path d="M2.1 8.9h1.6" />
        <path d="M13.4 3.9l-1.2 1.2" />
      </g>
      <Cloud y={19} />
    </>
  ),
  cloudy: <Cloud y={19} />,
  fog: (
    <>
      <Cloud y={12.5} />
      <g stroke="currentColor" strokeWidth={1.5} strokeLinecap="round">
        <path d="M4 16.5h16" />
        <path d="M6.5 20h11" />
      </g>
    </>
  ),
  rain: (
    <>
      <Cloud />
      <g stroke="currentColor" strokeWidth={1.6} strokeLinecap="round">
        <path d="M8 16.5l-1.2 2.6" />
        <path d="M12.5 16.5l-1.2 2.6" />
        <path d="M17 16.5l-1.2 2.6" />
      </g>
    </>
  ),
  snow: (
    <>
      <Cloud />
      <g stroke="currentColor" strokeWidth={2.1} strokeLinecap="round">
        <path d="M8 17.2v.01" />
        <path d="M12 18.6v.01" />
        <path d="M16 17.2v.01" />
        <path d="M9.6 20.4v.01" />
        <path d="M14.4 20.4v.01" />
      </g>
    </>
  ),
  thunderstorm: (
    <>
      <Cloud />
      <path
        d="M13 11.5l-3.4 5h2.9l-1.6 4.2 4.8-5.8h-3.1z"
        fill="currentColor"
        fillOpacity={0.4}
        stroke="currentColor"
        strokeWidth={1.2}
        strokeLinejoin="round"
      />
    </>
  ),
}

interface Props {
  code: number
  size?: number
  className?: string
}

export default function WeatherIcon({ code, size = 30, className }: Props) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" className={className} aria-hidden="true">
      {ICONS[weatherKind(code)]}
    </svg>
  )
}
