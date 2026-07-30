import { useEffect, useMemo, useState } from 'react'
import type { LottoDraw, LottoRecommendation, LottoRecommendationGroup, LottoRecommendationNumber, LottoSection } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'
import { useLottoCollection } from '../hooks/useLottoCollection'

interface Props {
  section: LottoSection | null
  loading: boolean
  error: string | null
  onRetry: () => Promise<void>
}

// 실제 동행복권 공 색상: 1-10 노랑, 11-20 파랑, 21-30 빨강, 31-40 회색, 41-45 초록.
function ballColor(n: number): string {
  if (n <= 10) return '#fbc400'
  if (n <= 20) return '#69c8f2'
  if (n <= 30) return '#ff7272'
  if (n <= 40) return '#aaaaaa'
  return '#b0d840'
}

function LottoBall({ n, small, bonus }: { n: number; small?: boolean; bonus?: boolean }) {
  return (
    <span
      className={
        'lotto__ball' + (small ? ' lotto__ball--sm' : '') + (bonus ? ' lotto__ball--bonus' : '')
      }
      style={{ background: ballColor(n) }}
    >
      {n}
    </span>
  )
}

function HistoryRow({ draw }: { draw: LottoDraw }) {
  return (
    <li className="lotto__history-row">
      <span className="lotto__history-no">{draw.drwNo}회</span>
      <span className="lotto__history-date">{draw.drwDate}</span>
      <span className="lotto__history-balls">
        {draw.numbers.map((n) => (
          <LottoBall n={n} small key={n} />
        ))}
        <span className="lotto__plus">+</span>
        <LottoBall n={draw.bonus} small bonus />
      </span>
    </li>
  )
}

function formatUpdatedAt(iso: string): string {
  return new Date(iso).toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' })
}

const RECOMMENDATION_GROUP_ICON: Record<LottoRecommendationGroup, string> = {
  hot: '🔥',
  mid: '⚖️',
  cold: '❄️',
}

const RECOMMENDATION_GROUP_LABEL: Record<LottoRecommendationGroup, string> = {
  hot: '최근 출현 많음',
  mid: '중간 빈도',
  cold: '최근 출현 적음',
}

function RecommendationBall({ n }: { n: LottoRecommendationNumber }) {
  return (
    <div className="lotto__rec-ball-wrap">
      <span className="lotto__rec-group-icon" title={RECOMMENDATION_GROUP_LABEL[n.group]} aria-hidden="true">
        {RECOMMENDATION_GROUP_ICON[n.group]}
      </span>
      <LottoBall n={n.number} />
    </div>
  )
}

// nextAvailableAt까지 남은 시간을 시/분 단위로 카운트다운하며 30초마다
// 갱신한다 (몇 시간 단위의 카운트다운이니 초 단위 정밀도까지는 필요 없다).
function useCountdownLabel(targetIso: string | undefined): string {
  const [label, setLabel] = useState('')

  useEffect(() => {
    if (!targetIso) {
      setLabel('')
      return
    }

    const targetMs = new Date(targetIso).getTime()

    const tick = () => {
      const diffMs = targetMs - Date.now()
      if (diffMs <= 0) {
        setLabel('곧 공개')
        return
      }
      const totalMinutes = Math.floor(diffMs / 60000)
      const hours = Math.floor(totalMinutes / 60)
      const minutes = totalMinutes % 60
      setLabel(`${hours}시간 ${minutes}분 후 공개`)
    }

    tick()
    const id = setInterval(tick, 30000)
    return () => clearInterval(id)
  }, [targetIso])

  return label
}

function RecommendationSection({ recommendation }: { recommendation: LottoRecommendation }) {
  const countdown = useCountdownLabel(recommendation.isBlackout ? recommendation.nextAvailableAt : undefined)

  return (
    <div className="lotto__section lotto__recommendation">
      <h3 className="lotto__section-title">🎲 이번 주 추천 번호</h3>

      {recommendation.isBlackout ? (
        <div className="lotto__rec-blackout">
          <p>
            현재는 이번 회차 판매가 마감되어 추천 번호를 준비 중입니다. 일요일 오전 6시부터 다음 회차 추천 번호를
            확인하실 수 있어요.
          </p>
          {countdown && <p className="lotto__rec-countdown">{countdown}</p>}
        </div>
      ) : (
        <div className="lotto__balls">
          {recommendation.numbers?.map((n) => (
            <RecommendationBall n={n} key={n.number} />
          ))}
        </div>
      )}

      <p className="lotto__rec-disclaimer">
        최근 50회 출현 빈도를 상/중/하로 나눠 골고루 섞은 재미용 번호로, 실제 당첨 확률과는 무관하며 특정 조합의
        구매를 권하는 것이 아닙니다.
      </p>
    </div>
  )
}

// 데이터 수집 ON/OFF 토글 — 로또 카드와는 별개의 상태(useLottoCollection)를
// 다루므로 독립된 컴포넌트로 분리했다. ON인 동안 "42/50 회차 수집됨" 진행
// 상황을 함께 보여준다. onToggle은 토글 직후 useLotto의 데이터를 즉시
// 다시 가져오게 해서(section.isBackfilling 갱신), 다음 자동 폴링을 기다리지
// 않고 바로 "수집 중" 상태로 화면이 반응하게 한다.
function CollectionToggle({ onToggle }: { onToggle: () => void }) {
  const { status, busy, start, stop } = useLottoCollection()
  const running = status?.running ?? false

  const handleClick = async () => {
    if (running) {
      await stop()
    } else {
      await start()
    }
    onToggle()
  }

  return (
    <div className="lotto__collection-toggle">
      <button
        type="button"
        className={running ? 'lotto__toggle-btn lotto__toggle-btn--on' : 'lotto__toggle-btn'}
        onClick={handleClick}
        disabled={busy}
        aria-pressed={running}
      >
        🔄 데이터 수집: {running ? 'ON' : 'OFF'}
      </button>
      {status && (running || status.savedCount > 0) && (
        <span className="lotto__collection-progress">
          {status.savedCount}/{status.windowSize} 회차 수집됨
        </span>
      )}
    </div>
  )
}

export default function LottoCard({ section, loading, error, onRetry }: Props) {
  const [retrying, setRetrying] = useState(false)
  const pulsing = usePulseOnChange(section?.data?.latest.drwNo)

  const freqEntries = useMemo(() => {
    const freq = section?.data?.frequency ?? {}
    return Array.from({ length: 45 }, (_, i) => i + 1).map((n) => ({
      n,
      count: freq[String(n)] ?? 0,
    }))
  }, [section])

  const maxCount = useMemo(() => Math.max(1, ...freqEntries.map((f) => f.count)), [freqEntries])
  const minCount = useMemo(() => Math.min(...freqEntries.map((f) => f.count)), [freqEntries])

  const handleRetry = async () => {
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }

  const failureMessage = error || (section && !section.success ? section.error : null)

  return (
    <section className="card card--lotto">
      <header className="card__header">
        <h2 className="card__title">🎱 로또</h2>
        {section && (
          <span className="card__duration">
            <span className={pulsing ? 'card__duration-dot card__duration-dot--pulse' : 'card__duration-dot'} />
            {section.durationMs}ms
          </span>
        )}
      </header>

      <CollectionToggle onToggle={onRetry} />

      {loading && !section && (
        <div className="card__body lotto__skeleton" aria-label="로또 데이터 불러오는 중">
          <div className="skeleton-weather-row">
            {Array.from({ length: 6 }).map((_, i) => (
              <span className="skeleton-circle" key={i} style={{ width: 36, height: 36 }} />
            ))}
          </div>
          <span className="skeleton-line skeleton-line--row" />
          <span className="skeleton-line skeleton-line--row" />
          <span className="skeleton-line skeleton-line--row" />
        </div>
      )}

      {section?.isBackfilling && !section.data && (
        <div className="card__body card__preparing" aria-live="polite">
          <p>⏳ 로또 데이터를 처음 준비하는 중입니다.</p>
          <p className="card__preparing-sub">몇 초 후 자동으로 다시 확인합니다.</p>
        </div>
      )}

      {!loading && failureMessage && !section?.data && !section?.isBackfilling && (
        <div className="card__body card__error">
          <p>⚠️ {failureMessage}</p>
          <button type="button" onClick={handleRetry} disabled={retrying}>
            {retrying ? '재시도 중…' : '재시도'}
          </button>
        </div>
      )}

      {section?.data && (
        <div className="card__body lotto__body">
          <div className="lotto__col">
            <div className="lotto__latest">
              <div className="lotto__latest-meta">
                {section.data.latest.drwNo}회 · {section.data.latest.drwDate}
              </div>
              <div className="lotto__balls">
                {section.data.latest.numbers.map((n) => (
                  <LottoBall n={n} key={n} />
                ))}
                <span className="lotto__plus">+</span>
                <LottoBall n={section.data.latest.bonus} bonus />
              </div>
            </div>

            <RecommendationSection recommendation={section.data.recommendation} />

            <div className="lotto__section">
              <h3 className="lotto__section-title">최근 10회 출현 번호</h3>
              <div className="lotto__badges">
                {section.data.recentAppeared.map((n) => (
                  <LottoBall n={n} small key={n} />
                ))}
              </div>
            </div>

            <div className="lotto__section lotto__insight">
              <h3 className="lotto__section-title">
                <span className="briefing__badge">AI</span> 통계 인사이트
              </h3>
              {section.data.aiInsight.available ? (
                <>
                  <p className="lotto__insight-text">{section.data.aiInsight.text}</p>
                  {section.data.aiInsight.generatedAt && (
                    <div className="briefing__meta">
                      <span>마지막 업데이트: {formatUpdatedAt(section.data.aiInsight.generatedAt)}</span>
                      {section.data.aiInsight.cached && <span className="briefing__cached-badge">캐시됨</span>}
                    </div>
                  )}
                </>
              ) : (
                <p className="lotto__insight-fallback">{section.data.aiInsight.text}</p>
              )}
            </div>
          </div>

          <div className="lotto__col">
            <div className="lotto__section">
              <h3 className="lotto__section-title">회차별 당첨번호 (최근 {section.data.history.length}회)</h3>
              <ol className="lotto__history">
                {section.data.history.map((draw) => (
                  <HistoryRow draw={draw} key={draw.drwNo} />
                ))}
              </ol>
            </div>

            <div className="lotto__section">
              <h3 className="lotto__section-title">번호별 출현 횟수 (최근 {section.data.history.length}회)</h3>
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
          </div>
        </div>
      )}

      <footer className="lotto__disclaimer">
        본 서비스는 참고용이며 실제 당첨을 보장하지 않습니다.
      </footer>
    </section>
  )
}
