// 한국어 텍스트를 '.' 기준으로 문장 단위로 분리하되, 마침표 자체는 유지한다.
// '.'는 뒤에 공백(또는 문자열 끝)이 오고, 소수점(양쪽에 숫자가 있는 경우, 예:
// "1.5%"나 "1470.11")이 아닐 때만 문장 경계로 취급한다. 그래야 숫자 중간이
// 잘려나가는 일이 없다. AI 브리핑(날씨/환율/뉴스)과 로또 AI 인사이트가
// 공유한다 — 둘 다 Groq가 생성한 여러 문장짜리 텍스트를 문장 단위로
// 줄바꿈해서 보여줘야 하는 같은 문제를 갖고 있다.
export function splitSentences(text: string): string[] {
  const sentences: string[] = []
  let current = ''

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    current += ch

    if (ch === '.') {
      const prev = text[i - 1]
      const next = text[i + 1]
      const isDecimalPoint = prev !== undefined && next !== undefined && /\d/.test(prev) && /\d/.test(next)
      const isBoundary = next === undefined || /\s/.test(next)

      if (!isDecimalPoint && isBoundary) {
        sentences.push(current.trim())
        current = ''
      }
    }
  }

  if (current.trim()) sentences.push(current.trim())
  return sentences
}
