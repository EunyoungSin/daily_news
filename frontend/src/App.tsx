import { useCallback, useEffect, useRef, useState } from 'react'
import { useDashboard } from './hooks/useDashboard'
import { useLotto } from './hooks/useLotto'
import { useNews } from './hooks/useNews'
import { CITY_OPTIONS, CURRENCY_OPTIONS } from './types'
import type { NewsRegion } from './types'
import WeatherCard from './components/WeatherCard'
import ExchangeCard from './components/ExchangeCard'
import NewsCard from './components/NewsCard'
import BriefingCard from './components/BriefingCard'
import LottoCard from './components/LottoCard'
import './App.css'

function readInitialNewsContext(): { category: string; region: NewsRegion } {
  const params = new URLSearchParams(window.location.search)
  return {
    category: params.get('category') || 'top',
    region: params.get('region') === 'international' ? 'international' : 'domestic',
  }
}

const INITIAL_DASHBOARD_PARAMS = { city: 'daegu', from: 'USD', to: 'KRW' }

export default function App() {
  const [{ category: initialCategory, region: initialRegion }] = useState(readInitialNewsContext)
  const [newsCategory, setNewsCategory] = useState(initialCategory)
  const [newsRegion, setNewsRegion] = useState<NewsRegion>(initialRegion)

  const {
    data,
    loading,
    weatherPending,
    exchangePending,
    briefingPending,
    briefingSectionPending,
    briefingInFlight,
    error,
    params,
    applyParams,
    refresh,
    retrySection,
    autoRefresh,
    setAutoRefresh,
  } = useDashboard(
    INITIAL_DASHBOARD_PARAMS,
    { category: newsCategory, region: newsRegion },
  )

  // 도시/기준통화/변환통화는 "조회" 버튼을 눌러야만 실제로 적용된다 —
  // 이 state들은 드롭다운에서 아직 확정되지 않은 선택값을 담고, 마지막으로
  // *적용된* 선택값인 `params`(실제로 요청에 사용된 값)와는 별개다.
  // 드롭다운만 바꾸는 것으로는 절대 네트워크 요청이 발생해선 안 되며,
  // 오직 조회 버튼을 눌러야 pending 값이 params로 복사되고, useDashboard의
  // effect가 실제로 감시하는 것도 바로 이 params다.
  const [pendingCity, setPendingCity] = useState(params.city)
  const [pendingFrom, setPendingFrom] = useState(params.from)
  const [pendingTo, setPendingTo] = useState(params.to)

  const hasPendingChange = pendingCity !== params.city || pendingFrom !== params.from || pendingTo !== params.to
  const isSameCurrency = pendingFrom === pendingTo

  const applyPendingParams = useCallback(() => {
    applyParams({ city: pendingCity, from: pendingFrom, to: pendingTo })
  }, [pendingCity, pendingFrom, pendingTo, applyParams])

  // 각 드롭다운은 상대방의 현재 선택값을 비활성화하므로(아래 `disabled`
  // props 참고) 정상적인 사용으로는 pendingFrom === pendingTo에 도달할
  // 수 없다 — 이 alert는 그 방어를 우회하는 경로가 생길 경우(예: 이후
  // 옵션 목록 변경)를 대비한 보조 안전장치일 뿐, 주된 방어 수단은 아니다.
  useEffect(() => {
    if (pendingFrom === pendingTo) {
      window.alert('기준 통화와 변환 통화가 같습니다. 다른 통화를 선택해주세요.')
    }
  }, [pendingFrom, pendingTo])

  const retryWeather = useCallback(() => retrySection('weather'), [retrySection])
  const retryExchange = useCallback(() => retrySection('exchange'), [retrySection])
  const retryBriefing = useCallback(() => retrySection('briefing'), [retrySection])

  const {
    section: lottoSection,
    loading: lottoLoading,
    error: lottoError,
    retry: retryLotto,
    mode: lottoRecommendationMode,
    setMode: setLottoRecommendationMode,
    recommendationPending: lottoRecommendationPending,
  } = useLotto()

  const { section: newsSection, loading: newsLoading, error: newsError, retry: retryNews } = useNews(
    newsCategory,
    newsRegion,
  )

  // category/region을 URL에 유지해 새로고침해도 선택값이 보존되게 하고,
  // (weather/exchange나 카드 전체가 아니라) 브리핑의 뉴스 문단만
  // 개별적으로 다시 가져와서 맞춘다 — 최초 마운트 시에는 건너뛴다.
  // useDashboard의 초기 로드가 newsContext ref를 통해 현재
  // category/region을 이미 읽기 때문이다.
  const isFirstNewsEffect = useRef(true)
  useEffect(() => {
    const urlParams = new URLSearchParams(window.location.search)
    urlParams.set('category', newsCategory)
    urlParams.set('region', newsRegion)
    window.history.replaceState(null, '', `${window.location.pathname}?${urlParams.toString()}`)

    if (isFirstNewsEffect.current) {
      isFirstNewsEffect.current = false
      return
    }
    retrySection('news')
  }, [newsCategory, newsRegion, retrySection])

  return (
    <div className="app">
      <header className="app__header">
        <div>
          <p className="app__eyebrow">MULTI-SOURCE SIGNAL DESK</p>
          <h1 className="app__title">브리핑 관제실</h1>
        </div>

        <div className="controls">
          <label className="controls__field">
            <span>도시</span>
            <select
              value={pendingCity}
              onChange={(e) => setPendingCity(e.target.value)}
            >
              {CITY_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value}>{opt.label}</option>
              ))}
            </select>
          </label>

          <label className="controls__field">
            <span>기준 통화</span>
            <select
              value={pendingFrom}
              onChange={(e) => setPendingFrom(e.target.value)}
            >
              {CURRENCY_OPTIONS.map((c) => (
                <option key={c} value={c} disabled={c === pendingTo}>{c}</option>
              ))}
            </select>
          </label>

          <label className="controls__field">
            <span>변환 통화</span>
            <select
              value={pendingTo}
              onChange={(e) => setPendingTo(e.target.value)}
            >
              {CURRENCY_OPTIONS.map((c) => (
                <option key={c} value={c} disabled={c === pendingFrom}>{c}</option>
              ))}
            </select>
          </label>

          <label className="controls__toggle">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            <span>10분마다 자동 새로고침</span>
          </label>

          <button
            type="button"
            className={
              'controls__action controls__action--primary' + (hasPendingChange ? ' controls__action--pending' : '')
            }
            onClick={applyPendingParams}
            disabled={loading || isSameCurrency}
          >
            {loading ? (
              <>
                <span className="controls__action-spinner" aria-hidden="true" />
                조회 중…
              </>
            ) : (
              '조회'
            )}
          </button>

          <button type="button" className="controls__action" onClick={refresh} disabled={loading}>
            {loading ? (
              <>
                <span className="controls__action-spinner" aria-hidden="true" />
                갱신 중…
              </>
            ) : (
              '지금 새로고침'
            )}
          </button>
        </div>
      </header>

      <div className="app__status">
        {data && <span>총 처리 시간 {data.totalMs}ms</span>}
        {autoRefresh && <span className="app__status-dot" aria-hidden="true" />}
        {hasPendingChange && <span className="app__status-hint">선택을 반영하려면 조회를 눌러주세요</span>}
      </div>

      {error && (
        <div className="app__banner">
          <span>⚠️ {error}</span>
          <button type="button" onClick={refresh}>다시 시도</button>
        </div>
      )}

      <div className="dashboard-grid">
        <div className="dashboard-grid__row1">
          {data ? (
            <WeatherCard section={data.weather} onRetry={retryWeather} pending={weatherPending} />
          ) : (
            <div className="card card--skeleton">
              <span className="skeleton-line skeleton-header" />
              <div className="skeleton-weather-row">
                <span className="skeleton-circle" />
                <div className="skeleton-weather-col">
                  <span className="skeleton-line skeleton-line--number" />
                  <span className="skeleton-line skeleton-line--sm" />
                </div>
              </div>
              <span className="skeleton-line skeleton-line--row" />
            </div>
          )}

          {data ? (
            <ExchangeCard section={data.exchange} onRetry={retryExchange} pending={exchangePending} />
          ) : (
            <div className="card card--skeleton">
              <span className="skeleton-line skeleton-header" />
              <span className="skeleton-line skeleton-line--sm" />
              <span className="skeleton-line skeleton-line--number" />
              <span className="skeleton-line skeleton-line--row" />
            </div>
          )}

          <NewsCard
            section={newsSection}
            loading={newsLoading}
            error={newsError}
            onRetry={retryNews}
            category={newsCategory}
            region={newsRegion}
            onCategoryChange={setNewsCategory}
            onRegionChange={setNewsRegion}
            briefingInFlight={briefingInFlight}
          />
        </div>

        {data ? (
          <BriefingCard
            section={data.briefing}
            pending={briefingPending}
            weatherPending={briefingSectionPending.weather}
            exchangePending={briefingSectionPending.exchange}
            newsPending={briefingSectionPending.news}
            onRetry={retryBriefing}
          />
        ) : (
          <div className="card card--skeleton card--briefing">
            <span className="skeleton-line skeleton-header" />
            <span className="skeleton-line skeleton-line--row" />
            <span className="skeleton-line skeleton-line--row" />
            <span className="skeleton-line skeleton-line--row" />
          </div>
        )}
      </div>

      {/* 위 대시보드 그리드와는 독립적이다 — 자체 fetch, 자체
          로딩/에러 상태, 자체 재시도를 갖는다. */}
      <div className="dashboard-grid dashboard-grid--lotto">
        <LottoCard
          section={lottoSection}
          loading={lottoLoading}
          error={lottoError}
          onRetry={retryLotto}
          recommendationMode={lottoRecommendationMode}
          onRecommendationModeChange={setLottoRecommendationMode}
          recommendationPending={lottoRecommendationPending}
        />
      </div>
    </div>
  )
}
