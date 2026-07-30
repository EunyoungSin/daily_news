// "YYYY-MM-DD" -> "MM/DD" — 좁은 카드 안 7개 포인트짜리 축에 들어갈
// 만큼 짧게 줄인다.
export function formatChartDate(date: string): string {
  const parts = date.split('-')
  return parts.length === 3 ? `${parts[1]}/${parts[2]}` : date
}

// 백엔드의 displayRate는 항상 rate의 "보기 편한" 쪽이다 — 이미 1 이상이면
// 원래 값 그대로, 아니면 그 역수(항상 1보다 큼)를 쓴다
// (backend/exchange.go의 computeExchangeDisplay 참고) — 그래서 프론트엔드는
// 여기서 소수점 2자리 고정, 콤마 구분 포맷만 쓰면 되고, 0.00069 같은
// 1 미만 원본 rate에 필요한 동적 소수 자릿수 조정은 필요 없다.
export function formatRate(value: number): string {
  return value.toLocaleString('ko-KR', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}
