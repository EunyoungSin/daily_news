import { useLottoCollection } from '../../hooks/useLottoCollection'

// 매주 자동 업데이트 ON/OFF 토글 — 로또 카드와는 별개의 상태
// (useLottoCollection)를 다루므로 독립된 컴포넌트로 분리했다. 초기 50회는
// 정적 시드 파일로 채워지고, 이 토글은 그 이후 자동 수집을 켜고 끌 뿐이다.
// 서버 시작 시(또는 토글을 켤 때) 밀린 회차가 있으면 먼저 그 전부를
// 순차적으로 채우고(status.catchingUp — "N회차 밀려있어 순차적으로 채우는
// 중입니다" 안내), 밀린 게 없어지면 그 뒤로는 매주 최대 1개씩 새 회차를
// 확인하는 평상시 점검으로 돌아간다. onToggle은 토글 직후 useLotto의
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
  // 자동 수집은 서버 시작 시 기본 ON이므로(LOTTO_AUTO_COLLECTION_DEFAULT),
  // 최초 상태 응답이 아직 도착하기 전에도 OFF로 잠깐 깜빡이지 않도록 ON을
  // 기본값으로 보여준다 — 응답이 도착하면 실제 running 값으로 바로
  // 교체된다(사용자가 직접 껐거나 서버가 off로 설정된 경우 포함).
  const running = status?.running ?? true

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
      {status?.catchingUp && (
        <span className="lotto__catchup-progress" aria-live="polite">
          ⏳ {status.totalPendingCount}회차 밀려있어 순차적으로 채우는 중입니다 (
          {status.processedCount}/{status.totalPendingCount})
        </span>
      )}
      {status && (running || status.lastCollectedAt) && (
        <span className="lotto__collection-progress">
          {running && status.nextCheckAt && <>다음 자동 확인 예정: {formatDateTime(status.nextCheckAt)} · </>}
          마지막 성공: {status.lastCollectedAt ? formatDateTime(status.lastCollectedAt) : '아직 없음'}
        </span>
      )}
    </>
  )
}
