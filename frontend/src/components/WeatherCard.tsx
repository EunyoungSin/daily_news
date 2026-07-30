import { useState } from 'react'
import type { WeatherSection } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'

function weatherGlyph(code: number): string {
  if (code === 0) return '☀'
  if (code === 1 || code === 2) return '⛅'
  if (code === 3) return '☁'
  if (code === 45 || code === 48) return '\u{1F32B}'
  if (code >= 51 && code <= 67) return '\u{1F327}'
  if (code >= 71 && code <= 77) return '\u{1F328}'
  if (code >= 80 && code <= 86) return '\u{1F326}'
  if (code === 95 || code === 96 || code === 99) return '⛈'
  return '–'
}

const HIGH_PRECIP_THRESHOLD = 40

function PeriodCell({
  label,
  tempC,
  code,
  description,
  precipProbability,
  available = true,
  unavailableReason,
}: {
  label: string
  tempC: number
  code: number
  description: string
  precipProbability?: number
  // 예보 구간(오전 8시/오후 2시)은 unavailable 상태로 내려올 수 있다 — 백엔드가
  // 해당 시간대의 관측값을 가져오지 못했거나 신뢰할 수 없었던 경우다
  // (backend/models.go의 PeriodForecast doc comment 참고). "현재"는 이런 개념이
  // 없으므로(현재 상황은 로드에 성공하거나 섹션 전체가 실패하거나 둘 중 하나이므로)
  // 이 prop을 넘기지 않고 기본값 true를 사용한다.
  available?: boolean
  // available이 false일 때 어떤 안내 문구를 보여줄지 결정한다 —
  // types.ts의 PeriodForecast.unavailableReason 참고.
  unavailableReason?: 'not_yet_available' | 'past_missing'
}) {
  const highPrecip = precipProbability !== undefined && precipProbability >= HIGH_PRECIP_THRESHOLD

  if (!available) {
    // "past_missing"(슬롯 시각이 이미 지났는데도 즉시 재조회까지 실패한
    // 예외 상황)에는 "곧 발표될 예정입니다"라고 하면 사실과 다르므로,
    // 발표를 기다리는 뉘앙스가 없는 별도 문구를 쓴다. 그 외(아직 발표
    // 전, 또는 이전 버전 서버가 내려준 필드 없는 응답)에는 기존처럼
    // "곧 발표될 예정입니다"를 보여준다.
    const message =
      unavailableReason === 'past_missing' ? '일시적으로 데이터를 가져오지 못했습니다' : '곧 발표될 예정입니다'
    return (
      <div className="weather__period weather__period--unavailable">
        <div className="weather__period-label">{label}</div>
        <div className="weather__period-na">{message}</div>
      </div>
    )
  }

  return (
    <div className="weather__period">
      <div className="weather__period-label">{label}</div>
      <div className="weather__period-glyph" aria-hidden="true">
        {weatherGlyph(code)}
      </div>
      <div className="weather__period-temp">{tempC.toFixed(1)}°</div>
      <div className="weather__period-desc">{description}</div>
      {precipProbability !== undefined && (
        <div className={highPrecip ? 'weather__precip weather__precip--high' : 'weather__precip'}>
          {highPrecip ? '☂ ' : ''}
          강수 {precipProbability}%
        </div>
      )}
    </div>
  )
}

interface Props {
  section: WeatherSection
  onRetry: () => Promise<void>
}

type Tab = 'today' | 'tomorrow'

export default function WeatherCard({ section, onRetry }: Props) {
  const [retrying, setRetrying] = useState(false)
  const [tab, setTab] = useState<Tab>('today')
  const pulsing = usePulseOnChange(section.durationMs)

  const handleRetry = async () => {
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }

  return (
    <section className="card card--weather">
      <header className="card__header">
        <h2 className="card__title">날씨</h2>
        {section.success && section.data && (
          <div className="briefing__tabs weather__tabs card__header-tabs" role="tablist">
            <button
              type="button"
              role="tab"
              aria-selected={tab === 'today'}
              className={tab === 'today' ? 'briefing__tab briefing__tab--active' : 'briefing__tab'}
              onClick={() => setTab('today')}
            >
              오늘
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={tab === 'tomorrow'}
              className={tab === 'tomorrow' ? 'briefing__tab briefing__tab--active' : 'briefing__tab'}
              onClick={() => setTab('tomorrow')}
            >
              내일
            </button>
          </div>
        )}
        <span className="card__duration">
          <span className={pulsing ? 'card__duration-dot card__duration-dot--pulse' : 'card__duration-dot'} />
          {section.durationMs}ms
        </span>
      </header>

      {section.success && section.data ? (
        <div className="card__body weather__body">
          <div className="weather__content">
          <div className="weather__city-label">
            {section.data.current.cityLabel}
            {section.data.dataSource === 'open-meteo-fallback' && (
              <span className="weather__source-badge" title="기상청 데이터를 일시적으로 불러오지 못해 보조 소스로 대체된 값입니다">
                잠정치 (보조 데이터 소스)
              </span>
            )}
          </div>

          {tab === 'today' ? (
            <div className="weather__periods" key="today">
              <PeriodCell
                label="현재"
                tempC={section.data.current.temperatureC}
                code={section.data.current.weatherCode}
                description={section.data.current.description}
              />
              <PeriodCell
                label="오전 8시"
                tempC={section.data.forecast.today.morning.temperatureC}
                code={section.data.forecast.today.morning.weatherCode}
                description={section.data.forecast.today.morning.description}
                precipProbability={section.data.forecast.today.morning.precipProbability}
                available={section.data.forecast.today.morning.available}
                unavailableReason={section.data.forecast.today.morning.unavailableReason}
              />
              <PeriodCell
                label="오후 2시"
                tempC={section.data.forecast.today.afternoon.temperatureC}
                code={section.data.forecast.today.afternoon.weatherCode}
                description={section.data.forecast.today.afternoon.description}
                precipProbability={section.data.forecast.today.afternoon.precipProbability}
                available={section.data.forecast.today.afternoon.available}
                unavailableReason={section.data.forecast.today.afternoon.unavailableReason}
              />
            </div>
          ) : (
            <div className="weather__periods weather__periods--two" key="tomorrow">
              <PeriodCell
                label="오전 8시"
                tempC={section.data.forecast.tomorrow.morning.temperatureC}
                code={section.data.forecast.tomorrow.morning.weatherCode}
                description={section.data.forecast.tomorrow.morning.description}
                precipProbability={section.data.forecast.tomorrow.morning.precipProbability}
                available={section.data.forecast.tomorrow.morning.available}
                unavailableReason={section.data.forecast.tomorrow.morning.unavailableReason}
              />
              <PeriodCell
                label="오후 2시"
                tempC={section.data.forecast.tomorrow.afternoon.temperatureC}
                code={section.data.forecast.tomorrow.afternoon.weatherCode}
                description={section.data.forecast.tomorrow.afternoon.description}
                precipProbability={section.data.forecast.tomorrow.afternoon.precipProbability}
                available={section.data.forecast.tomorrow.afternoon.available}
                unavailableReason={section.data.forecast.tomorrow.afternoon.unavailableReason}
              />
            </div>
          )}
          </div>

          <div className="weather__footer">
            {tab === 'today' && (
              <dl className="weather__stats">
                <div>
                  <dt>풍속</dt>
                  <dd>{section.data.current.windSpeedKph.toFixed(1)} km/h</dd>
                </div>
                <div>
                  <dt>관측 시각</dt>
                  <dd>{section.data.current.observedAt.replace('T', ' ')}</dd>
                </div>
              </dl>
            )}

            <a
              className="weather__detail-link"
              href={section.data.current.detailUrl}
              target="_blank"
              rel="noopener noreferrer"
            >
              자세히 보기 →
            </a>
          </div>
        </div>
      ) : (
        <div className="card__body card__error">
          <p>⚠️ 날씨 정보를 불러오지 못했습니다</p>
          <button type="button" onClick={handleRetry} disabled={retrying}>
            {retrying ? '재시도 중…' : '재시도'}
          </button>
        </div>
      )}
    </section>
  )
}
