import { useState } from 'react'
import type { NewsRegion, NewsSection } from '../types'
import { NEWS_CATEGORY_OPTIONS } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'

interface Props {
  section: NewsSection | null
  loading: boolean
  error: string | null
  onRetry: () => Promise<void>
  category: string
  region: NewsRegion
  onCategoryChange: (category: string) => void
  onRegionChange: (region: NewsRegion) => void
}

function formatPubDate(pubDate: string): string {
  // NewsData.io는 "YYYY-MM-DD HH:mm:ss"(UTC) 형식으로 응답한다 — 헤드라인
  // 목록에서는 날짜 부분만 보여줘도 한눈에 파악하기에 충분하다.
  return pubDate.split(' ')[0] ?? pubDate
}

export default function NewsCard({
  section,
  loading,
  error,
  onRetry,
  category,
  region,
  onCategoryChange,
  onRegionChange,
}: Props) {
  const [retrying, setRetrying] = useState(false)
  // 헤드라인별 "원문/한글" override — 값이 없으면 "region 기본값을 사용"
  // (번역이 있으면 한글)한다는 의미이고, 값이 있으면 사용자가 그 행을
  // 명시적으로 전환했다는 의미다.
  const [itemOverrides, setItemOverrides] = useState<Record<string, 'ko' | 'original'>>({})
  const pulsing = usePulseOnChange(section?.durationMs)

  const handleRetry = async () => {
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }

  const setItemMode = (id: string, mode: 'ko' | 'original') => {
    setItemOverrides((prev) => ({ ...prev, [id]: mode }))
  }

  const failureMessage = error || (section && !section.success ? section.error : null)

  return (
    <section className="card card--news">
      <header className="card__header">
        <h2 className="card__title">뉴스</h2>
        {section && (
          <span className="card__duration">
            <span className={pulsing ? 'card__duration-dot card__duration-dot--pulse' : 'card__duration-dot'} />
            {section.durationMs}ms
          </span>
        )}
      </header>

      <div className="card__body news__body">
        <div className="news__controls">
          <div className="news__category-pills" role="tablist">
            {NEWS_CATEGORY_OPTIONS.map((opt) => (
              <button
                key={opt.value}
                type="button"
                role="tab"
                aria-selected={category === opt.value}
                className={category === opt.value ? 'news__category-pill news__category-pill--active' : 'news__category-pill'}
                onClick={() => onCategoryChange(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>

          <div className="briefing__tabs news__region-tabs" role="tablist">
            <button
              type="button"
              role="tab"
              aria-selected={region === 'domestic'}
              className={region === 'domestic' ? 'briefing__tab briefing__tab--active' : 'briefing__tab'}
              onClick={() => onRegionChange('domestic')}
            >
              🇰🇷 국내
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={region === 'international'}
              className={region === 'international' ? 'briefing__tab briefing__tab--active' : 'briefing__tab'}
              onClick={() => onRegionChange('international')}
            >
              🌐 해외
            </button>
          </div>
        </div>

        <div className="news__content">
        {loading && !section && (
          <div className="news__skeleton" aria-label="뉴스 불러오는 중">
            {[0, 1, 2, 3, 4].map((i) => (
              <div className="skeleton-news-row" key={i}>
                <span className="skeleton-circle" />
                <span className="skeleton-line skeleton-line--row" />
              </div>
            ))}
          </div>
        )}

        {!loading && failureMessage && !section?.data && (
          <div className="card__error">
            <p>{failureMessage}</p>
            <button type="button" onClick={handleRetry} disabled={retrying}>
              {retrying ? '재시도 중…' : '재시도'}
            </button>
          </div>
        )}

        {section?.notice && <p className="news__notice">ℹ️ {section.notice}</p>}

        {section?.data && (
          <ol className="news__list">
            {section.data.items.map((item, index) => {
              const mode = itemOverrides[item.id] ?? 'ko'
              const translationFailed = region === 'international' && mode === 'ko' && !item.translatedTitle
              const displayTitle =
                region === 'international' && mode === 'ko' && item.translatedTitle ? item.translatedTitle : item.title

              return (
                <li key={item.id} className="news__item">
                  <span className="news__rank">{index + 1}</span>
                  <div className="news__item-body">
                    <a href={item.link} target="_blank" rel="noopener noreferrer" className="news__link">
                      {displayTitle}
                      {translationFailed && <span className="news__translation-fallback"> (번역 실패)</span>}
                    </a>
                    <div className="news__item-footer">
                      <span className="news__source">
                        {item.sourceName}
                        {item.pubDate && <> · {formatPubDate(item.pubDate)}</>}
                      </span>
                      {region === 'international' && (
                        <span className="news__item-toggle">
                          <button
                            type="button"
                            className={mode === 'ko' ? 'news__item-toggle-btn news__item-toggle-btn--active' : 'news__item-toggle-btn'}
                            onClick={() => setItemMode(item.id, 'ko')}
                          >
                            한글
                          </button>
                          <span className="news__item-toggle-sep" aria-hidden="true">·</span>
                          <button
                            type="button"
                            className={mode === 'original' ? 'news__item-toggle-btn news__item-toggle-btn--active' : 'news__item-toggle-btn'}
                            onClick={() => setItemMode(item.id, 'original')}
                          >
                            원문
                          </button>
                        </span>
                      )}
                    </div>
                  </div>
                </li>
              )
            })}
          </ol>
        )}
        </div>
      </div>
    </section>
  )
}
