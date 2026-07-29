import { useState } from 'react'
import type { BriefingSection, BriefingSectionMeta } from '../types'
import { usePulseOnChange } from '../hooks/usePulseOnChange'

interface Props {
  section: BriefingSection
  pending: boolean
  onRetry: () => Promise<void>
}

type Mode = 'simple' | 'detailed'

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

// 브리핑 내 weather/exchange/news 중 하나의 섹션. 자신의 generatedAt이
// 바뀔 때만 잠깐 하이라이트된다 — 즉 이번 요청에서 *이* 섹션이 실제로
// 재생성됐을 때만이고, 나머지 두 섹션이 갱신됐다고 해서 반응하지 않는다.
function BriefingSectionBlock({ meta, text }: { meta: BriefingSectionMeta; text: string }) {
  const justUpdated = usePulseOnChange(meta.generatedAt, 2500)

  if (!text.trim()) return null

  return (
    <div className={justUpdated ? 'briefing__section briefing__section--fresh' : 'briefing__section'}>
      {splitSentences(text).map((sentence, i) => (
        <p key={i} className="briefing__sentence">{sentence}</p>
      ))}
    </div>
  )
}

export default function BriefingCard({ section, pending, onRetry }: Props) {
  const [retrying, setRetrying] = useState(false)
  const [mode, setMode] = useState<Mode>('simple')
  const pulsing = usePulseOnChange(section.data?.generatedAt)

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
        {!pending && section.success && section.data && (
          <div className="briefing__tabs card__header-tabs" role="tablist">
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'simple'}
              className={mode === 'simple' ? 'briefing__tab briefing__tab--active' : 'briefing__tab'}
              onClick={() => setMode('simple')}
            >
              간단히
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'detailed'}
              className={mode === 'detailed' ? 'briefing__tab briefing__tab--active' : 'briefing__tab'}
              onClick={() => setMode('detailed')}
            >
              자세히
            </button>
          </div>
        )}
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
          <div className="briefing__text">
            <BriefingSectionBlock
              meta={section.data.briefingMeta.weather}
              text={mode === 'simple' ? section.data.briefingMeta.weather.simple : section.data.briefingMeta.weather.detailed}
            />
            <BriefingSectionBlock
              meta={section.data.briefingMeta.exchange}
              text={mode === 'simple' ? section.data.briefingMeta.exchange.simple : section.data.briefingMeta.exchange.detailed}
            />
            <BriefingSectionBlock
              meta={section.data.briefingMeta.news}
              text={mode === 'simple' ? section.data.briefingMeta.news.simple : section.data.briefingMeta.news.detailed}
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
