import { useEffect, useState } from 'react'
import type { LottoRecommendation, LottoRecommendationMode, LottoRecommendationSet } from '../../types'
import LottoBall from './LottoBall'

const MODE_OPTIONS: { value: LottoRecommendationMode; label: string }[] = [
  { value: 'trend', label: '🔥 고빈도 번호 우선' },
  { value: 'regression', label: '❄️ 저빈도 번호 우선' },
  { value: 'uniform', label: '⚖️ 완전 무작위' },
]

const BAND_LABELS = ['1-9', '10-19', '20-29', '30-39', '40-45']

// 4단계 파이프라인이 통과시킨 세트 하나 — 번호 6개와 그 통계(홀짝비,
// 합계, 구간분포, 직전 회차와의 중복)를 함께 보여준다. 어떤 세트가
// "더 나은" 세트라는 뜻이 아니라, 그 세트가 3단계 패턴 필터의 어떤
// 조건들을 만족했는지 사실을 보여줄 뿐이다.
function RecommendationSetCard({ set }: { set: LottoRecommendationSet }) {
  const bandSummary = BAND_LABELS.map((label) => `${label}: ${set.stats.bandDistribution[label] ?? 0}`).join(' · ')

  return (
    <div className="lotto__rec-set">
      <div className="lotto__balls">
        {set.numbers.map((n) => (
          <LottoBall n={n} key={n} />
        ))}
      </div>
      <div className="lotto__rec-set-stats">
        홀짝 {set.stats.oddEvenRatio} · 합계 {set.stats.sum} · 직전 회차 중복 {set.stats.overlapWithPrevious}개
      </div>
      <div className="lotto__rec-set-stats lotto__rec-set-stats--bands">구간분포 {bandSummary}</div>
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

// AI 브리핑 카드의 "문단 갱신 중" 스켈레톤(briefing__skeleton-line)과 완전히
// 같은 바 스타일을 재사용한다 — 번호 뱃지 6개는 원형 스켈레톤
// (skeleton-circle, LottoBall 기본 크기인 36px에 맞춤)으로, 통계 두 줄은
// 실제 텍스트("홀짝 ... · 합계 ... · 직전 회차 중복 ...개", "구간분포 ...")
// 길이에 맞춰 너비를 다르게 준 가로 막대로 표시한다.
function RecommendationSetSkeleton() {
  return (
    <div className="lotto__rec-set" aria-label="추천 번호 다시 계산 중">
      <div className="lotto__balls">
        {Array.from({ length: 6 }).map((_, i) => (
          <span className="skeleton-circle" key={i} style={{ width: 36, height: 36 }} />
        ))}
      </div>
      <span className="briefing__skeleton-line lotto__rec-set-stats" />
      <span className="briefing__skeleton-line briefing__skeleton-line--short lotto__rec-set-stats lotto__rec-set-stats--bands" />
    </div>
  )
}

interface Props {
  recommendation: LottoRecommendation
  mode: LottoRecommendationMode
  onModeChange: (mode: LottoRecommendationMode) => void
  pending: boolean
}

export default function LottoRecommendation({ recommendation, mode, onModeChange, pending }: Props) {
  const countdown = useCountdownLabel(recommendation.isBlackout ? recommendation.nextAvailableAt : undefined)

  return (
    <div className="lotto__section lotto__recommendation">
      <h3 className="lotto__section-title">🎲 이번 주 추천 번호</h3>

      <label className="lotto__rec-mode-label">
        추천 방식
        <select
          value={mode}
          onChange={(e) => onModeChange(e.target.value as LottoRecommendationMode)}
          disabled={pending}
        >
          {MODE_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </label>

      {pending ? (
        <RecommendationSetSkeleton />
      ) : recommendation.isBlackout ? (
        <div className="lotto__rec-blackout">
          <p>
            현재는 이번 회차 판매가 마감되어 추천 번호를 준비 중입니다. 일요일 오전 6시부터 다음 회차 추천 번호를
            확인하실 수 있어요.
          </p>
          {countdown && <p className="lotto__rec-countdown">{countdown}</p>}
        </div>
      ) : (
        recommendation.set && <RecommendationSetCard set={recommendation.set} />
      )}

      <p className="lotto__rec-disclaimer">
        재미로 즐기는 번호입니다. 실제 당첨 확률에는 아무 영향이 없으며, 특정 조합을 구매하시라는 의미가 아닙니다.
      </p>
    </div>
  )
}
