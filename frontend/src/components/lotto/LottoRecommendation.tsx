import { useEffect, useState } from 'react'
import type { LottoRecommendation, LottoRecommendationMode, LottoRecommendationSet } from '../../types'
import LottoBall from './LottoBall'

const MODE_OPTIONS: { value: LottoRecommendationMode; label: string }[] = [
  { value: 'uniform', label: '완전 무작위' },
  { value: 'trend', label: '핫넘버 우선' },
  { value: 'regression', label: '콜드넘버 우선' },
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

interface Props {
  recommendation: LottoRecommendation
  mode: LottoRecommendationMode
  onModeChange: (mode: LottoRecommendationMode) => void
}

export default function LottoRecommendation({ recommendation, mode, onModeChange }: Props) {
  const countdown = useCountdownLabel(recommendation.isBlackout ? recommendation.nextAvailableAt : undefined)
  // trend/regression은 이름만 보면 예측 알고리즘처럼 들릴 수 있어, 과거
  // 출현 패턴을 재미로 반영한 것일 뿐이라는 설명을 눈에 잘 띄게 붙인다.
  const showModeCaveat = mode === 'trend' || mode === 'regression'

  return (
    <div className="lotto__section lotto__recommendation">
      <h3 className="lotto__section-title">🎲 이번 주 추천 번호</h3>

      <label className="lotto__rec-mode-label">
        추천 방식
        <select value={mode} onChange={(e) => onModeChange(e.target.value as LottoRecommendationMode)}>
          {MODE_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </label>
      {showModeCaveat && (
        <p className="lotto__rec-mode-caveat">
          ⚠️ 과거 출현 패턴을 재미로 반영한 방식일 뿐, 실제 확률과는 무관합니다.
        </p>
      )}

      {recommendation.isBlackout ? (
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
