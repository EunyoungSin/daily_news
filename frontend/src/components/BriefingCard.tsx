import { useState } from 'react'
import type { BriefingSection, BriefingSectionMeta } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'

interface Props {
  section: BriefingSection
  // 브리핑 전체가 아직 성공한 적이 없거나(카드가 처음 마운트될 때는
  // 이미 data가 있는 상태이므로 사실상 완전 실패 후 재시도, 또는 뉴스
  // 카테고리/지역 변경 시에만 true) 카드 전체를 스켈레톤으로 덮는다.
  // "조회"로 city/currency만 바뀐 경우에는 이 플래그 대신 아래
  // weatherPending/exchangePending이 문단 단위로만 스켈레톤을 켠다.
  pending: boolean
  // "조회"로 도시가 바뀌어 날씨 문단만 다시 합성되는 동안 true —
  // 환율/뉴스 문단은 그대로 유지된 채 보여진다.
  weatherPending: boolean
  // "조회"로 통화가 바뀌어 환율 문단만 다시 합성되는 동안 true —
  // 날씨/뉴스 문단은 그대로 유지된 채 보여진다.
  exchangePending: boolean
  onRetry: () => Promise<void>
}

function formatUpdatedAt(iso: string): string {
  return new Date(iso).toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' })
}

// 한국어 브리핑 텍스트를 '.' 기준으로 문장 단위로 분리하되, 마침표 자체는 유지한다.
// '.'는 뒤에 공백(또는 문자열 끝)이 오고, 소수점(양쪽에 숫자가 있는 경우, 예:
// "1.5%"나 "1470.11")이 아닐 때만 문장 경계로 취급한다. 그래야 숫자 중간이
// 잘려나가는 일이 없다.
function splitSentences(text: string): string[] {
  const sentences: string[] = []
  let current = ''

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    current += ch

    if (ch === '.') {
      const prev = text[i - 1]
      const next = text[i + 1]
      const isDecimalPoint = prev !== undefined && next !== undefined && /\d/.test(prev) && /\d/.test(next)
      const isBoundary = next === undefined || /\s/.test(next)

      if (!isDecimalPoint && isBoundary) {
        sentences.push(current.trim())
        current = ''
      }
    }
  }

  if (current.trim()) sentences.push(current.trim())
  return sentences
}

// 여러 섹션이 동시에 대체됐을 때는 카드 상단에 한 번만 통합 배너로
// 안내하고(showUnifiedStaleBanner), 정확히 하나만 대체됐을 때는 그
// 섹션 옆에만 작은 배지를 붙인다 — 이 두 문구가 그 두 경우에 쓰인다.
const STALE_FALLBACK_MESSAGE = '⚠️ 일시적으로 사용량 제한이 있어 이전에 저장된 내용을 보여드립니다'
const STALE_FALLBACK_MESSAGE_MULTI =
  '⚠️ 일부 항목은 최신 정보가 아닐 수 있어요, 사용량 제한으로 이전 내용을 보여드리는 중입니다'

// 브리핑 내 weather/exchange/news 중 하나의 섹션. 자신의 generatedAt이
// 바뀔 때만 잠깐 하이라이트된다 — 즉 이번 요청에서 *이* 섹션이 실제로
// 재생성됐을 때만이고, 나머지 두 섹션이 갱신됐다고 해서 반응하지 않는다.
// showStaleBadge는 이 섹션만 단독으로 stale_fallback일 때만 true다 —
// 여러 섹션이 동시에 그렇다면 카드 상단의 통합 배너가 대신 표시되므로
// 개별 배지는 생략한다(BriefingCard의 showUnifiedStaleBanner 참고).
// pending은 "조회"로 이 섹션에 해당하는 값(도시 또는 통화)이 바뀌어 이
// 문단만 다시 합성되는 동안 true다 — 이때는 옆 문단들의 text/showStaleBadge와
// 무관하게 이 문단 자리에만 작은 스켈레톤을 보여준다.
function BriefingSectionBlock({
  meta,
  text,
  pending,
  showStaleBadge,
}: {
  meta: BriefingSectionMeta
  text: string
  pending: boolean
  showStaleBadge: boolean
}) {
  const justUpdated = usePulseOnChange(meta.generatedAt, 2500)

  if (pending) {
    return (
      <div className="briefing__section briefing__section--pending" aria-label="문단 갱신 중">
        <span className="briefing__skeleton-line" />
        <span className="briefing__skeleton-line briefing__skeleton-line--short" />
      </div>
    )
  }

  if (!text.trim()) return null

  return (
    <div className={justUpdated ? 'briefing__section briefing__section--fresh' : 'briefing__section'}>
      {splitSentences(text).map((sentence, i) => (
        <p key={i} className="briefing__sentence">{sentence}</p>
      ))}
      {showStaleBadge && (
        <p className="briefing__stale-badge">
          {STALE_FALLBACK_MESSAGE} ({formatUpdatedAt(meta.generatedAt)} 기준)
        </p>
      )}
    </div>
  )
}

export default function BriefingCard({ section, pending, weatherPending, exchangePending, onRetry }: Props) {
  const [retrying, setRetrying] = useState(false)
  const pulsing = usePulseOnChange(section.data?.generatedAt)

  const briefingMeta = section.data?.briefingMeta
  const staleSectionCount = briefingMeta
    ? (['weather', 'exchange', 'news'] as const).filter((key) => briefingMeta[key].status === 'stale_fallback').length
    : 0
  // 정확히 하나만 대체됐으면 그 섹션 옆에만 배지를 붙이고, 둘 이상이면
  // 개별 배지 대신 카드 상단에 통합 배너 하나만 보여준다.
  const showUnifiedStaleBanner = staleSectionCount >= 2
  const showPerSectionStaleBadge = staleSectionCount === 1

  const handleRetry = async () => {
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }

  return (
    <section className="card card--briefing">
      <header className="card__header">
        <h2 className="card__title">
          <span className="briefing__badge">AI</span> 오늘의 브리핑
        </h2>
        {!pending && (
          <span className="card__duration">
            <span className={pulsing ? 'card__duration-dot card__duration-dot--pulse' : 'card__duration-dot'} />
            {section.durationMs}ms
          </span>
        )}
      </header>

      {pending ? (
        <div className="card__body briefing__skeleton" aria-label="브리핑 합성 중">
          <span className="briefing__skeleton-line" />
          <span className="briefing__skeleton-line" />
          <span className="briefing__skeleton-line briefing__skeleton-line--short" />
        </div>
      ) : section.success && section.data ? (
        <div className="card__body">
          {showUnifiedStaleBanner && <p className="briefing__stale-banner">{STALE_FALLBACK_MESSAGE_MULTI}</p>}

          <div className="briefing__text">
            <BriefingSectionBlock
              meta={section.data.briefingMeta.weather}
              text={section.data.briefingMeta.weather.detailed}
              pending={weatherPending}
              showStaleBadge={showPerSectionStaleBadge && section.data.briefingMeta.weather.status === 'stale_fallback'}
            />
            <BriefingSectionBlock
              meta={section.data.briefingMeta.exchange}
              text={section.data.briefingMeta.exchange.detailed}
              pending={exchangePending}
              showStaleBadge={showPerSectionStaleBadge && section.data.briefingMeta.exchange.status === 'stale_fallback'}
            />
            <BriefingSectionBlock
              meta={section.data.briefingMeta.news}
              text={section.data.briefingMeta.news.detailed}
              pending={false}
              showStaleBadge={showPerSectionStaleBadge && section.data.briefingMeta.news.status === 'stale_fallback'}
            />
          </div>

          <div className="briefing__meta">
            <span>마지막 업데이트: {formatUpdatedAt(section.data.generatedAt)}</span>
            {section.data.cached && <span className="briefing__cached-badge">캐시됨</span>}
          </div>
        </div>
      ) : (
        <div className="card__body card__error">
          <p>{section.error || '⚠️ AI 브리핑을 사용할 수 없습니다'}</p>
          <button type="button" onClick={handleRetry} disabled={retrying}>
            {retrying ? '재시도 중…' : '재시도'}
          </button>
        </div>
      )}
    </section>
  )
}
