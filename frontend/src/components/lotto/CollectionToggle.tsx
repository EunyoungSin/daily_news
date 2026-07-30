import { useLottoCollection } from '../../hooks/useLottoCollection'

// 데이터 수집 ON/OFF 토글 — 로또 카드와는 별개의 상태(useLottoCollection)를
// 다루므로 독립된 컴포넌트로 분리했다. ON인 동안 "42/50 회차 수집됨" 진행
// 상황을 함께 보여준다. onToggle은 토글 직후 useLotto의 데이터를 즉시
// 다시 가져오게 해서(section.isBackfilling 갱신), 다음 자동 폴링을 기다리지
// 않고 바로 "수집 중" 상태로 화면이 반응하게 한다.
export default function CollectionToggle({ onToggle }: { onToggle: () => void }) {
  const { status, busy, start, stop } = useLottoCollection()
  const running = status?.running ?? false

  const handleClick = async () => {
    if (running) {
      await stop()
    } else {
      await start()
    }
    onToggle()
  }

  return (
    <div className="lotto__collection-toggle">
      <button
        type="button"
        className={running ? 'lotto__toggle-btn lotto__toggle-btn--on' : 'lotto__toggle-btn'}
        onClick={handleClick}
        disabled={busy}
        aria-pressed={running}
      >
        🔄 데이터 수집: {running ? 'ON' : 'OFF'}
      </button>
      {status && (running || status.savedCount > 0) && (
        <span className="lotto__collection-progress">
          {status.savedCount}/{status.windowSize} 회차 수집됨
        </span>
      )}
    </div>
  )
}
