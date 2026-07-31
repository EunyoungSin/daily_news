#!/usr/bin/env bash
set -euo pipefail

# 여러 로또 회차를 한 번에 POST /api/admin/lotto/manual-entry로 채워
# 넣는다. dhlottery가 막혀서 자동 수집이 안 될 때, 다른 신뢰할 수 있는
# 출처에서 확인한 실제 당첨번호를 이 스크립트로 한 번에 채워 넣는 용도다
# (README의 "로또 섹션 > 관리자 API" 참고).
#
# 사용법:
#   1. 아래 형식으로 JSON 배열 파일을 만든다(backend/data/lotto_seed.json과
#      동일한 형식):
#        [
#          {"drwNo":1187,"drwDate":"2025-08-16","numbers":[3,12,19,27,33,41],"bonus":7},
#          {"drwNo":1188,"drwDate":"2025-08-23","numbers":[1,9,18,25,30,45],"bonus":2}
#        ]
#   2. 환경변수를 설정한다:
#        export ADMIN_SECRET_KEY=...
#        export BASE_URL=https://your-app.onrender.com   # 기본값: http://localhost:8080
#   3. 실행한다:
#        ./scripts/manual_lotto_entry.sh entries.json
#
# jq가 필요하다(JSON 배열을 순회하기 위함) — 없다면 `apt install jq` /
# `brew install jq`로 설치할 수 있다.

if [ "$#" -ne 1 ]; then
  echo "사용법: $0 <entries.json>" >&2
  exit 1
fi

entries_file="$1"
base_url="${BASE_URL:-http://localhost:8080}"

if [ -z "${ADMIN_SECRET_KEY:-}" ]; then
  echo "ADMIN_SECRET_KEY 환경변수를 설정하세요." >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq가 필요합니다 (JSON 배열을 순회하기 위함)." >&2
  exit 1
fi

if [ ! -f "$entries_file" ]; then
  echo "파일을 찾을 수 없습니다: $entries_file" >&2
  exit 1
fi

count=$(jq 'length' "$entries_file")
echo "총 ${count}개 회차를 ${base_url}에 제출합니다..."

success=0
failed=0

for i in $(seq 0 $((count - 1))); do
  entry=$(jq -c ".[$i]" "$entries_file")
  drw_no=$(echo "$entry" | jq -r '.drwNo')

  response=$(curl -s -w '\n%{http_code}' -X POST "${base_url}/api/admin/lotto/manual-entry" \
    -H "X-Admin-Key: ${ADMIN_SECRET_KEY}" \
    -H "Content-Type: application/json" \
    -d "$entry")
  http_code=$(echo "$response" | tail -n1)
  body=$(echo "$response" | sed '$d')

  if [ "$http_code" = "200" ]; then
    echo "✓ ${drw_no}회차 저장 완료: ${body}"
    success=$((success + 1))
  else
    echo "✗ ${drw_no}회차 실패 (HTTP ${http_code}): ${body}" >&2
    failed=$((failed + 1))
  fi
done

echo "완료: 성공 ${success}개, 실패 ${failed}개"
