// 실제 동행복권 공 색상: 1-10 노랑, 11-20 파랑, 21-30 빨강, 31-40 회색, 41-45 초록.
function ballColor(n: number): string {
  if (n <= 10) return '#fbc400'
  if (n <= 20) return '#69c8f2'
  if (n <= 30) return '#ff7272'
  if (n <= 40) return '#aaaaaa'
  return '#b0d840'
}

export default function LottoBall({ n, small, bonus }: { n: number; small?: boolean; bonus?: boolean }) {
  return (
    <span
      className={
        'lotto__ball' + (small ? ' lotto__ball--sm' : '') + (bonus ? ' lotto__ball--bonus' : '')
      }
      style={{ background: ballColor(n) }}
    >
      {n}
    </span>
  )
}
