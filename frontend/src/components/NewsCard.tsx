import { useEffect, useState } from 'react'
import type { NewsItem, NewsRegion, NewsSection } from '../types'
import { NEWS_CATEGORY_OPTIONS } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'

// 사유별로 나눠 볼 수 있도록 백엔드(news_translation.go)의 실패 사유
// 상수와 그대로 맞춘 한글 라벨이다 — 콘솔에서 값을 그대로 읽어도
// 뜻을 알 수 있지만, 사유별 그룹 제목에는 이 라벨을 쓴다.
const TRANSLATION_FAILURE_REASON_LABELS: Record<string, string> = {
  rate_limit: 'rate_limit (Groq 요청 한도 초과, 쿨다운 45초)',
  validation_failed: 'validation_failed (한자/영어 혼입 등 검증 실패, 쿨다운 5분)',
  api_error: 'api_error (그 외 일반 API 오류, 쿨다운 5분)',
}

// logNewsTranslationFailuresByReason은 이번에 새로 받아온 뉴스 목록 중
// 번역이 쿨다운으로 원문 폴백된 항목들을, 사유별로 그룹 지어 콘솔에
// 남긴다 — 백엔드 로그("뉴스: 번역 실패(사유=..., 쿨다운=...)")를 서버
// 콘솔에서 직접 볼 수 없는 상황에서도, 브라우저 개발자 도구만으로 "이
// 헤드라인이 왜 원문으로 보이는지"를 사유별로 확인할 수 있게 한다.
function logNewsTranslationFailuresByReason(items: NewsItem[]): void {
  const byReason = new Map<string, NewsItem[]>()
  for (const item of items) {
    if (!item.translationFailureReason) continue
    const group = byReason.get(item.translationFailureReason) ?? []
    group.push(item)
    byReason.set(item.translationFailureReason, group)
  }
  if (byReason.size === 0) return

  for (const [reason, group] of byReason) {
    console.groupCollapsed(`[뉴스 번역 쿨다운] ${TRANSLATION_FAILURE_REASON_LABELS[reason] ?? reason} — ${group.length}건`)
    for (const item of group) {
      console.log(`· ${item.id}: "${item.title}"`)
    }
    console.groupEnd()
  }
}

interface Props {
  section: NewsSection | null
  loading: boolean
  error: string | null
  onRetry: () => Promise<void>
  category: string
  region: NewsRegion
  onCategoryChange: (category: string) => void
  onRegionChange: (region: NewsRegion) => void
  // briefingInFlight: AI 브리핑 3섹션(날씨/환율/뉴스)이 아직 생성 중이면
  // true — 이 카드 자체의 loading(뉴스 목록 자체 조회)과는 별개다. 뉴스
  // 카테고리/지역 탭을 빠르게 전환하면 이 카드의 자체 조회뿐 아니라
  // 브리핑의 뉴스 문단도 함께 재생성되는데(둘 다 Groq를 호출한다), 이
  // 카드의 loading만으로 탭을 잠그면 자체 조회는 먼저 끝나고 브리핑
  // 쪽만 아직 진행 중인 틈에 탭을 또 바꿔 Groq 호출이 겹쳐 쌓일 수
  // 있다. 두 플래그를 모두 disabled 조건에 반영해 그 틈을 없앤다.
  briefingInFlight: boolean
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
  briefingInFlight,
}: Props) {
  const [retrying, setRetrying] = useState(false)
  const tabsDisabled = loading || briefingInFlight
  // 헤드라인별 "원문/한글" override — 값이 없으면 "region 기본값을 사용"
  // (번역이 있으면 한글)한다는 의미이고, 값이 있으면 사용자가 그 행을
  // 명시적으로 전환했다는 의미다.
  const [itemOverrides, setItemOverrides] = useState<Record<string, 'ko' | 'original'>>({})
  const pulsing = usePulseOnChange(section?.durationMs)

  // section.data.items가 실제로 바뀔 때(새 조회가 끝났을 때)만 로그를
  // 남긴다 — item 배열 참조는 useNews.ts가 매 요청마다 새로 만들므로,
  // ko/원문 토글 같은 이 컴포넌트 자체의 리렌더와는 섞이지 않는다.
  useEffect(() => {
    if (region !== 'international' || !section?.data?.items) return
    logNewsTranslationFailuresByReason(section.data.items)
  }, [region, section?.data?.items])

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
                disabled={tabsDisabled}
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
              disabled={tabsDisabled}
            >
              🇰🇷 국내
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={region === 'international'}
              className={region === 'international' ? 'briefing__tab briefing__tab--active' : 'briefing__tab'}
              onClick={() => onRegionChange('international')}
              disabled={tabsDisabled}
            >
              🌐 해외
            </button>
          </div>

          {briefingInFlight && <p className="news__controls-hint">브리핑 생성 중에는 잠시 후 이용해주세요</p>}
        </div>

        <div className="news__content">
        {loading && (
          // AI 브리핑 카드(BriefingCard)의 "문단 갱신 중" 스켈레톤과 완전히
          // 같은 바 스타일(briefing__skeleton-line — 회색 반투명 shimmer
          // 가로 막대)을 그대로 재사용한다. 헤드라인 5개 자리에 맞춰
          // 5개를 두되, 실제 제목처럼 길이가 들쭉날쭉해 보이도록 너비를
          // 조금씩 다르게 준다 — 전부 같은 너비면 오히려 "막대 목록"처럼
          // 보여 헤드라인 리스트라는 인상이 덜 든다.
          <div className="news__skeleton" aria-label="뉴스 불러오는 중">
            {['94%', '82%', '90%', '70%', '85%'].map((width, i) => (
              <span className="briefing__skeleton-line" style={{ width }} key={i} />
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

        {!loading && section?.notice && <p className="news__notice">ℹ️ {section.notice}</p>}

        {!loading && section?.data && (
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
