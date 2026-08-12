import { useState } from 'react'
import type { LottoRecommendationMatch, LottoRecommendationMode } from '../../types'
import LottoBall from './LottoBall'

// LottoRecommendation.tsx의 MODE_OPTIONS와 라벨을 맞춘다 — 사용자가 이미
// 익숙한 표현을 그대로 재사용해서, "이 결과가 어떤 추천 방식 얘기인지"를
// 바로 알아볼 수 있게 한다.
const MODE_LABELS: Record<LottoRecommendationMode, string> = {
  trend: '🔥 고빈도 번호 우선',
  regression: '❄️ 저빈도 번호 우선',
  uniform: '⚖️ 완전 무작위',
}

// 0개일 때와 1개 이상일 때 모두 담백한 재미 위주 톤을 유지한다 — "적중률"/
// "명중률" 같은 성적표 표현이나, 다른 모드와 비교하는 문구는 여기서도
// 다른 어디서도 절대 쓰지 않는다(이 파일 전체의 설계 원칙).
function matchPhrase(count: number): string {
  if (count === 0) return '이번엔 하나도 안 맞았어요 😅'
  return `${count}개 일치했네요 🎉`
}

function PreviousResultCard({ result }: { result: LottoRecommendationMatch }) {
  const matchedSet = new Set(result.matchedNumbers)

  return (
    <div className="lotto__prev-result-card">
      <div className="lotto__prev-result-header">
        <span className="lotto__prev-result-mode">{MODE_LABELS[result.mode]}</span>
        {result.isRetroactive && (
          <span className="lotto__prev-result-retroactive">(참고용으로 사후에 계산됨)</span>
        )}
      </div>

      <p className="lotto__prev-result-summary">
        지난주 추천 번호와 실제 당첨번호를 비교해봤어요! {matchPhrase(result.matchedCount)}
      </p>

      <div className="lotto__prev-result-row">
        <span className="lotto__prev-result-label">추천</span>
        <div className="lotto__balls">
          {result.numbers.map((n) => (
            <LottoBall n={n} small matched={matchedSet.has(n)} key={`rec-${n}`} />
          ))}
        </div>
      </div>
      <div className="lotto__prev-result-row">
        <span className="lotto__prev-result-label">실제</span>
        <div className="lotto__balls">
          {result.actualNumbers.map((n) => (
            <LottoBall n={n} small matched={matchedSet.has(n)} key={`actual-${n}`} />
          ))}
        </div>
      </div>
    </div>
  )
}

// results는 항상 trend -> regression -> uniform 3개 항목 고정 순서로
// 온다(백엔드 getLottoPreviousRecommendationResult 참고) — 이 컴포넌트는
// 그 순서를 그대로 렌더링할 뿐 절대 재정렬하지 않는다. 일치 개수가 큰
// 순서로 정렬하면 "1등/2등"처럼 순위가 있는 것으로 보이기 쉬워서,
// 어떤 추천 방식이 다른 방식보다 우수하다는 인상을 주지 않으려는
// 이 기능의 설계 의도와 정면으로 어긋난다.
export default function LottoPreviousResult({ results }: { results: LottoRecommendationMatch[] }) {
  const [open, setOpen] = useState(false)

  return (
    <div className="lotto__section lotto__prev-result">
      <h3 className="lotto__section-title lotto__prev-result-title">
        <button
          type="button"
          className="lotto__prev-result-toggle"
          onClick={() => setOpen((prev) => !prev)}
          aria-expanded={open}
        >
          📊 지난주 추천 결과 보기
          <span className="lotto__prev-result-caret" aria-hidden="true">
            {open ? '▲' : '▼'}
          </span>
        </button>
      </h3>

      {open && (
        <div className="lotto__prev-result-body">
          {results.map((result) => (
            <PreviousResultCard result={result} key={result.mode} />
          ))}
          <p className="lotto__prev-result-disclaimer">
            ※ 참고로 일치 개수는 순전히 우연입니다. 세 방식 모두 당첨 확률에는 차이가 없습니다.
          </p>
        </div>
      )}
    </div>
  )
}
