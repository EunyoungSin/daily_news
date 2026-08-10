import { useLottoCollection } from '../../hooks/useLottoCollection'

// 매주 자동 업데이트 ON/OFF 토글 — 로또 카드와는 별개의 상태
// (useLottoCollection)를 다루므로 독립된 컴포넌트로 분리했다. 초기 50회는
// 정적 시드 파일로 채워지고, 이 토글은 그 이후 매주 최대 1개씩 새 회차를
// 확인해 추가하는 백그라운드 점검을 켜고 끌 뿐이다(더 이상 "42/50 회차
// 수집됨" 같은 배치 진행률 개념이 없다). onToggle은 토글 직후 useLotto의
// 데이터를 즉시 다시 가져오게 해서, 다음 자동 폴링을 기다리지 않고 바로
// 반응하게 한다.
function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString('ko-KR', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

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

  // 이 버튼과 상태 캡션은 LottoCard.tsx의 .lotto__toggle-row(카드 제목과
  // 최신 회차 정보 사이, 세로로 쌓이는 자기만의 줄)에 Fragment로 흘러
  // 들어간다 — 버튼 아래 캡션이 자연스럽게 다음 줄로 쌓이도록 별개의
  // flex item으로 둔다.
  return (
    <>
      <button
        type="button"
        className={running ? 'lotto__toggle-btn lotto__toggle-btn--on' : 'lotto__toggle-btn'}
        onClick={handleClick}
        disabled={busy}
        aria-pressed={running}
      >
        🔄 매주 자동 업데이트: {running ? 'ON' : 'OFF'}
      </button>
      {status && (running || status.lastCollectedAt) && (
        <span className="lotto__collection-progress">
          {running && status.nextCheckAt && <>다음 자동 확인 예정: {formatDateTime(status.nextCheckAt)} · </>}
          마지막 성공: {status.lastCollectedAt ? formatDateTime(status.lastCollectedAt) : '아직 없음'}
        </span>
      )}
    </>
  )
}
