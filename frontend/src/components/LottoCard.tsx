import { lazy, Suspense, useState } from 'react'
import type { LottoSection } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'
import LottoBall from './lotto/LottoBall'
import LottoRecommendation from './lotto/LottoRecommendation'
import CollectionToggle from './lotto/CollectionToggle'

// 회차 목록(최대 50행)과 히트맵(45칸)은 스크롤을 내려야 보이고 데이터도
// 무거워서 지연 로딩한다 — vite.config.ts의 manualChunks와 함께 이
// 두 컴포넌트를 초기 번들에서 분리한다.
const LottoHistoryList = lazy(() => import('./lotto/LottoHistoryList'))
const LottoHeatmap = lazy(() => import('./lotto/LottoHeatmap'))

interface Props {
  section: LottoSection | null
  loading: boolean
  error: string | null
  onRetry: () => Promise<void>
}

function formatUpdatedAt(iso: string): string {
  return new Date(iso).toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' })
}

function LottoHistorySkeleton() {
  return (
    <div className="lotto__section" aria-label="회차별 당첨번호 불러오는 중">
      <span className="skeleton-line skeleton-header" />
      <span className="skeleton-line skeleton-line--row" />
      <span className="skeleton-line skeleton-line--row" />
      <span className="skeleton-line skeleton-line--row" />
    </div>
  )
}

function LottoHeatmapSkeleton() {
  return (
    <div className="lotto__section" aria-label="번호별 출현 횟수 불러오는 중">
      <span className="skeleton-line skeleton-header" />
      <div className="skeleton-weather-row">
        {Array.from({ length: 9 }).map((_, i) => (
          <span className="skeleton-circle" key={i} style={{ width: 28, height: 28 }} />
        ))}
      </div>
    </div>
  )
}

export default function LottoCard({ section, loading, error, onRetry }: Props) {
  const [retrying, setRetrying] = useState(false)
  const pulsing = usePulseOnChange(section?.data?.latest.drwNo)

  const handleRetry = async () => {
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }

  const failureMessage = error || (section && !section.success ? section.error : null)
  const isTursoOutage = section?.dbErrorType === 'turso_outage'

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
          <p>
            {isTursoOutage
              ? '⚠️ 데이터베이스 서비스(Turso)에 일시적인 장애가 있는 것으로 보입니다. 잠시 후 자동으로 복구됩니다.'
              : failureMessage.startsWith('⚠️')
                ? failureMessage
                : `⚠️ ${failureMessage}`}
          </p>
          <div className="card__error-actions">
            <button type="button" onClick={handleRetry} disabled={retrying}>
              {retrying ? '재시도 중…' : '재시도'}
            </button>
            {isTursoOutage && (
              <a
                className="card__error-status-link"
                href="https://status.turso.tech"
                target="_blank"
                rel="noreferrer noopener"
              >
                Turso 상태 확인 →
              </a>
            )}
          </div>
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

            <LottoRecommendation recommendation={section.data.recommendation} />

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
            <Suspense fallback={<LottoHistorySkeleton />}>
              <LottoHistoryList history={section.data.history} />
            </Suspense>

            <Suspense fallback={<LottoHeatmapSkeleton />}>
              <LottoHeatmap frequency={section.data.frequency} historyLength={section.data.history.length} />
            </Suspense>
          </div>
        </div>
      )}

      <footer className="lotto__disclaimer">
        본 서비스는 참고용이며 실제 당첨을 보장하지 않습니다.
      </footer>
    </section>
  )
}
