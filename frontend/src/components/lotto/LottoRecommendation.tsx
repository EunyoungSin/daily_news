import { useEffect, useState } from 'react'
import type { LottoRecommendation, LottoRecommendationGroup, LottoRecommendationNumber } from '../../types'
import LottoBall from './LottoBall'

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

export default function LottoRecommendation({ recommendation }: { recommendation: LottoRecommendation }) {
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
