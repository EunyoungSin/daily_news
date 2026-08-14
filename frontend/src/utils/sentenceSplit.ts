// 문장 종결이 아닌 마침표로 취급할 일반적인 영문 약어(대소문자 구분 없이
// 매칭) — 뉴스/법률/기업명에 자주 등장하는 것 위주로, 완벽할 필요는 없다.
// 예: "Vantage Integrated Securities Solution Pvt. Ltd."가 "Pvt."/"Ltd." 뒤에서
// 잘못 줄바꿈되어 회사명이 여러 줄로 쪼개지는 문제를 막는다.
const ABBREVIATIONS = [
  'pvt', 'ltd', 'inc', 'co', 'corp', 'llc', 'llp',
  'jr', 'sr', 'mr', 'mrs', 'dr', 'vs', 'etc', 'no', 'st',
]

// 마침표 바로 앞 단어가 위 약어 목록과 일치하는지 검사한다 — 단어 경계
// 기준으로 매칭해야 "Post"의 "st"처럼 약어가 다른 단어 끝부분과 우연히
// 겹치는 경우를 오매칭하지 않는다. `i` 플래그로 대소문자를 구분하지 않으면
// `[^a-z]`도 함께 대소문자 무관 부정 매칭이 되어 알파벳이 아닌 모든 경계
// (공백, 문자열 시작, 한글 등)를 올바르게 인식한다.
const abbreviationEndingPattern = new RegExp(`(?:^|[^a-z])(${ABBREVIATIONS.join('|')})$`, 'i')

// 한국어 텍스트를 '.' 기준으로 문장 단위로 분리하되, 마침표 자체는 유지한다.
// '.'는 뒤에 공백(또는 문자열 끝)이 오고, 소수점(양쪽에 숫자가 있는 경우, 예:
// "1.5%"나 "1470.11")이 아니고, 앞 단어가 일반적인 영문 약어(위
// ABBREVIATIONS 참고)도 아닐 때만 문장 경계로 취급한다. 그래야 숫자
// 중간이나 회사명 중간이 잘려나가는 일이 없다. AI 브리핑(날씨/환율/뉴스)과
// 로또 AI 인사이트가 공유한다 — 둘 다 Groq가 생성한 여러 문장짜리 텍스트를
// 문장 단위로 줄바꿈해서 보여줘야 하는 같은 문제를 갖고 있다.
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
      const isAbbreviation = abbreviationEndingPattern.test(current.slice(0, -1))

      if (!isDecimalPoint && !isAbbreviation && isBoundary) {
        sentences.push(current.trim())
        current = ''
      }
    }
  }

  if (current.trim()) sentences.push(current.trim())
  return sentences
}
