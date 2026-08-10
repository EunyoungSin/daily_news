import type { LottoDraw } from '../../types'
import LottoBall from './LottoBall'

function HistoryRow({ draw }: { draw: LottoDraw }) {
  return (
    <li className="lotto__history-row">
      <span className="lotto__history-no">{draw.drwNo}회</span>
      <span className="lotto__history-date">{draw.drwDate}</span>
      <span className="lotto__history-balls">
        {draw.numbers.map((n) => (
          <LottoBall n={n} small key={n} />
        ))}
        <span className="lotto__plus">+</span>
        <LottoBall n={draw.bonus} small bonus />
      </span>
    </li>
  )
}

// 회차별 당첨번호 목록 — 스크롤을 내려야 보이고 회차마다 공을 그리는
// 비용도 있어(최대 50행) LottoCard.tsx에서 React.lazy로 지연 로딩한다.
export default function LottoHistoryList({ history }: { history: LottoDraw[] }) {
  return (
    <div className="lotto__section lotto__section--history">
      <h3 className="lotto__section-title">회차별 당첨번호 (최근 {history.length}회)</h3>
      <ol className="lotto__history">
        {history.map((draw) => (
          <HistoryRow draw={draw} key={draw.drwNo} />
        ))}
      </ol>
    </div>
  )
}
