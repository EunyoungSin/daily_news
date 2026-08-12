// 실제 동행복권 공 색상: 1-10 노랑, 11-20 파랑, 21-30 빨강, 31-40 회색, 41-45 초록.
// 나머지 카드는 차분한 블루그레이 톤이지만, 로또 공은 일부러 선명한
// 원색을 그대로 쓴다 — 로또 섹션 전체를 자연스럽게 눈에 띄게 하는
// 포인트 컬러 역할이다.
function ballColor(n: number): string {
  if (n <= 10) return '#fbc400'
  if (n <= 20) return '#69c8f2'
  if (n <= 30) return '#ff7272'
  if (n <= 40) return '#aaaaaa'
  return '#b0d840'
}

// matched는 "지난주 추천 결과"에서 추천 번호와 실제 당첨번호 양쪽에
// 공통으로 쓰인다 — 겹친 번호를 (색상만이 아니라) outline과 함께
// 표시해서, 이 프로젝트의 다른 강조 표시(예: 환율 카드의 상승/하락
// 화살표+텍스트)와 마찬가지로 색맹 사용자도 구분할 수 있게 한다. 세트
// 하단에 항상 "N개 일치" 텍스트도 함께 표시되므로(LottoPreviousResult.tsx
// 참고), 색/outline은 그 텍스트를 시각적으로 보강하는 역할일 뿐 유일한
// 정보 전달 수단이 아니다.
export default function LottoBall({
  n,
  small,
  bonus,
  matched,
}: {
  n: number
  small?: boolean
  bonus?: boolean
  matched?: boolean
}) {
  return (
    <span
      className={
        'lotto__ball' +
        (small ? ' lotto__ball--sm' : '') +
        (bonus ? ' lotto__ball--bonus' : '') +
        (matched ? ' lotto__ball--matched' : '')
      }
      style={{ background: ballColor(n) }}
    >
      {n}
    </span>
  )
}
