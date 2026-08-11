# 브리핑 관제실 — 실시간 멀티API 대시보드 + AI 브리핑 + 로또

Go 백엔드가 날씨(국내 도시는 기상청 API, 해외 도시 및 폴백은 Open-Meteo) / 환율(Frankfurter) / 뉴스(NewsData.io)를 오픈 API로 병렬 수집하고, 결과를 DB(Turso/libSQL)에 캐싱합니다. 이 데이터를 바탕으로 Groq LLM이 날씨·환율·뉴스 각 섹션별 한국어 브리핑을 생성하며, 원본 데이터가 바뀌지 않으면 이전 브리핑을 그대로 재사용해 불필요한 AI 호출을 줄입니다.<br>

로또 섹션은 초기 50회차 데이터를 정적 시드로 확보한 뒤, 이후로는 공개 GitHub 데이터셋을 통해 매주 최신 회차만 최소한으로 수집해 DB(Turso/libSQL)에 저장합니다(동행복권이 이 서버의 IP를 차단해 직접 호출 대신 이 방식을 씁니다 — 두 소스 모두 실패할 경우를 대비한 관리자 수동 입력 기능도 갖추고 있습니다).<br>
이 누적 데이터를 통계로 집계해 Groq AI API로 요약 인사이트를 제공하고, 가중 랜덤 샘플링 + 패턴 필터링으로 계산한 "이번 주 추천 번호"도 함께 보여줍니다.<br>

프론트엔드는 React + TypeScript(Vite)로 구성되어 있습니다.<br>

https://daily-news-o9mf.onrender.com/ 에서 확인해 보실 수 있습니다.

## 프로젝트 구조

```
backend/    Go, 표준 라이브러리 위주 (net/http, sync, context) + Turso/libSQL(database/sql, SQLite 호환) + 오픈API 4종(기상청, Open-Meteo, Frankfuter, NewsData.io) + Groq API + 로또 GitHub 데이터셋(폴백: 동행복권 API)
frontend/   React + TypeScript (Vite)
```

## 빠르게 시작하기

### 0. 로컬 DB — 별도 설정 불필요

로또 섹션과 원본 데이터/브리핑/뉴스 번역 캐시는 DB가 있어야 동작하지만(없어도
날씨/환율/뉴스/브리핑 자체는 정상 동작하고, 캐싱만 꺼진 채로 매번 다시 계산합니다),
로컬 개발에서는 Docker나 계정 가입 같은 사전 준비가 전혀 필요 없습니다. `TURSO_DATABASE_URL`
환경변수가 없으면 서버가 자동으로 `backend/data/dashboard.db` 파일(libSQL, SQLite와 완전히
호환되는 포맷)을 만들어 그대로 사용합니다 — 아래 1번을 그대로 따라 하면 됩니다.

### 1. 백엔드 (Go)

```bash
cd backend
cp .env.example .env   # GROQ_API_KEY 등 입력 (DB는 비워두면 로컬 파일로 자동 폴백)
go run .
```

기본적으로 `http://localhost:8080` 에서 실행됩니다. `GET /api/dashboard?city=daegu&from=USD&to=KRW`,
`GET /api/lotto`, `GET /api/news?category=top&region=domestic` 로 확인할 수 있습니다. 서버 시작 시
`lotto_draws` / `ai_insight_cache` 등 필요한 테이블 전부를 `CREATE TABLE IF NOT EXISTS`로 자동
생성합니다(전체 목록은 아래 "DB 스키마" 참고).

> **참고**: DB 드라이버(`go-libsql`)는 CGO로 네이티브 libSQL 라이브러리를 호출하므로,
> 로컬에 C 컴파일러(gcc 등)가 설치되어 있어야 `go run .`/`go build`가 됩니다. Debian/Ubuntu는
> `sudo apt-get install gcc`, macOS는 Xcode Command Line Tools(`xcode-select --install`)로
> 설치할 수 있습니다.

### 2. 프론트엔드 (React + Vite)

```bash
cd frontend
npm install
npm run dev
```

`http://localhost:5173` 에서 실행되며, `/api/*` 요청은 Vite dev 서버 proxy를 통해 백엔드(:8080)로 전달됩니다.

두 프로세스를 각각 별도 터미널에서 실행하면 됩니다.

## Groq API 키 발급 (무료)

1. https://console.groq.com 에서 가입합니다.
2. 좌측 메뉴 **API Keys** 에서 새 키를 생성합니다 (무료 티어로 발급 가능).
3. `backend/.env` 파일에 아래처럼 입력합니다.

   ```
   GROQ_API_KEY=발급받은_키
   ```

키가 없거나 호출에 실패해도 앱 전체는 죽지 않고, AI 브리핑 카드만
"⚠️ AI 브리핑을 사용할 수 없습니다 (API 키 확인 필요)" 메시지와 재시도 버튼을 보여줍니다.
날씨/환율 2개 섹션은 키 유무와 무관하게 항상 정상 동작합니다. 로또 섹션의
AI 인사이트도 같은 키를 사용하며, 키가 없거나 호출에 실패해도 나머지 로또 데이터
(당첨번호/통계)는 정상 동작하고 인사이트 영역만 "⚠️ AI 인사이트를 사용할 수 없습니다"로 대체됩니다.
뉴스 섹션은 해외(International) 모드에서만 헤드라인 번역에 이 키를 사용하며, 키가 없거나
호출에 실패해도 해당 헤드라인만 "번역 실패, 원문 표시"로 대체될 뿐 섹션 전체가 죽지는 않습니다.

호출량이 많은 곳(브리핑 3섹션, 뉴스 번역)은 기본적으로 소형 모델
(`GROQ_MODEL`, 기본값 `llama-3.1-8b-instant`, 일 14,400회 쿼터)을 쓰고,
그 출력이 콘텐츠 검증에 실패했을 때 딱 한 번만 더 정확한 모델
(`GROQ_ESCALATION_MODEL`, 기본값 `llama-3.3-70b-versatile`, 일 1,000회
쿼터)로 재시도합니다 — 로또 AI 인사이트(주 1회만 생성)는 처음부터 이
큰 모델을 씁니다. 오늘 실제 호출 횟수(모델별)와 캐시 히트 수는
`GET /api/debug/groq-usage`에서 확인할 수 있습니다.

## AI 브리핑 콘텐츠 검증 (환각·반말·금칙어·반복 방지)

Groq가 생성한 문장을 그대로 내보내지 않고, `validateSectionOutput`(`backend/briefing.go`)이
고정된 순서로 여러 검사를 거칩니다. 검사는 두 등급으로 나뉩니다 — **hardFailure**는 검증에
실패하면 에스컬레이션 모델(`GROQ_ESCALATION_MODEL`)로 한 번만 재시도하고, 그래도 실패하면
`stale_fallback`(남아있는 캐시나 안내 문구로 대체)으로 처리합니다. **softFailure**는 재시도해도
계속 검출되면 결과를 그대로 내보냅니다 — 현재는 금칙어 검사만 이 등급입니다.

- **존댓말 강제** (hardFailure): 날씨/환율/뉴스 공통 프롬프트 규칙("항상 합니다체로 작성하세요")에
  더해, 뉴스 문단은 원문(NewsData.io)이 기사체(~했다)라도 반드시 합니다체로 재작성하라는 전용
  규칙이 추가돼 있습니다. `findInformalSentenceEnding`이 반말/기사체 종결을 정규식으로 탐지합니다.
- **금칙어 필터** (softFailure): 인터넷 은어(ㅋㅋ, 대박, 헐 등)를 쓰지 말라는 프롬프트 지시와
  `findBannedPhrase` 후처리 검사가 있습니다. 네 가지 검사 중 유일하게, 재시도까지 소진한 뒤에도
  검출되면 결과를 그대로 내보냅니다.
- **환각 방지** (hardFailure, 5종): `findUngroundedNumber`(원본에 없는 숫자),
  `findFabricatedPercentage`(% 기호 조작), `findUngroundedProperNoun`(근거 없는 고유명사),
  `findTopicMismatch`(토큰 중복도 기반 주제 이탈), `findLeakedEnglish`/`findForeignCJK`(번역 누락·
  외국어 잔존)이 각각 실제로 관측됐던 환각 사례를 회귀 테스트로 고정해두고 있습니다. 우산 필요
  여부(날씨)·상승/하락 판단(환율)처럼 숫자 해석이 필요한 판단은 애초에 LLM에 맡기지 않고 Go가
  미리 계산해(`computeUmbrellaAdvice`, `computeExchangeTrend`) 프롬프트에 답으로 제공합니다 —
  판단 자체를 LLM에서 걷어내 환각 여지를 구조적으로 없앤 것입니다.
- **반복 감지** (hardFailure): `findRepeatedPhrase`가 10자 이상인 구절이 같은 생성 결과 안에서
  재등장하는지 검사합니다. 이전에 캐시된 브리핑과 비교하지는 않습니다 — 매 생성 결과 "내부"의
  반복만 잡을 뿐, 여러 번의 생성에 걸쳐 비슷한 문구가 되풀이되는 것은 감지 대상이 아닙니다.

새로운 실패 유형을 발견했을 때는 프롬프트에 규칙 문장을 추가하기보다 먼저 위 검사기 중 하나를
추가/확장해 해결할 수 없는지부터 검토하세요 — 검사기는 실행 시점에만 비용이 들지만, 프롬프트
문구는 캐시가 미스될 때마다 세 섹션(날씨/환율/뉴스) 각각에서 토큰 비용이 듭니다. 실제로 뉴스
섹션 프롬프트는 규칙이 하나둘 늘어날 때마다 총 토큰 수가 커져 `llama-3.1-8b-instant`의 분당
한도(6,000 TPM)를 두 차례(6,148토큰, 이후 2,464토큰) 위협한 전례가 있습니다. 재발을 막기 위해
`TestWeatherBriefingPromptFitsWithinTokenBudget`/`TestExchangeBriefingPromptFitsWithinTokenBudget`/
`TestNewsBriefingPromptFitsWithinTokenBudget`(`backend/briefing_section_test.go`)이 세 섹션
프롬프트의 추정 토큰 수 예산을 각각 고정해두고 있으니, 프롬프트를 수정했다면 반드시 이 테스트를
통과하는지 확인하세요.

## NewsData.io API 키 발급 (무료)

뉴스 섹션은 [NewsData.io](https://newsdata.io)의 공개 API(`/api/1/latest`)를 사용합니다.

1. https://newsdata.io 에서 가입합니다 (이메일만 있으면 되고, 신용카드는 필요 없습니다).
2. 대시보드에서 API Key를 확인합니다.
3. `backend/.env` 파일에 아래처럼 입력합니다.

   ```
   NEWSDATA_API_KEY=발급받은_키
   ```

무료 티어는 **일 200 크레딧**로 제한되어 있고, 요청 1회가 크레딧 1을 소비합니다. 이 프로젝트는
카테고리(`category`) + 지역(`region`) 조합별로 30분 TTL의 캐시(`getCachedOrFetchNews`)를 DB(Turso/libSQL)의
`raw_data_cache` 테이블에 저장해두어, 같은 조합에 대한 `GET /api/news` 요청과 AI 브리핑 내부
조회가 API를 중복 호출하지 않도록 크레딧을 절약합니다 — 서버 메모리가 아니라 DB에 저장하므로
Render 무료 티어처럼 슬립 후 재시작되는 환경에서도 캐시가 유지됩니다(아래 "원본 데이터 캐시"
참고). 그래도 카테고리·지역 조합이 많으므로(7개 카테고리 × 2개 지역 = 14가지) 무료 티어에서는
여러 조합을 짧은 시간에 자주 오가면 한도에 도달할 수 있습니다. 오늘 사용량이 한도(180회)에
근접하면, 만료된 캐시라도 추가 호출 없이 그대로 서빙합니다.

키가 없거나 호출에 실패해도 앱 전체는 죽지 않고, 뉴스 카드만 "⚠️ 뉴스를 불러올 수 없습니다"
메시지와 재시도 버튼을 보여줍니다. 날씨/환율/로또/AI 브리핑(뉴스 문단을 제외한 나머지 부분)은
평소대로 동작합니다.

## 기상청(KMA) API 키 발급 (무료)

국내 도시(서울/대구/부산/인천) 날씨는 [공공데이터포털](https://www.data.go.kr)의
"기상청_단기예보 ((구)_동네예보)" API(초단기실황조회 + 단기예보조회)를 사용합니다 — 네이버 날씨가
참조하는 것과 같은 관측 데이터라, 온도가 다른 서비스와 거의 일치합니다. 해외 도시(도쿄/뉴욕/런던)는
기상청 API의 예보 범위 밖이라 항상 Open-Meteo를 사용합니다.

1. https://www.data.go.kr 에서 가입 후, "기상청_단기예보 ((구)_동네예보)" 오픈API를 검색해
   활용신청합니다(승인은 보통 즉시~수 분 내 자동 처리됩니다).
2. 마이페이지 > 오픈API > 활용신청 현황에서 서비스키를 확인합니다. **"일반 인증키(Encoding)"** 값을
   사용해야 합니다 — 이미 URL 인코딩된 값이라 코드가 추가 인코딩 없이 쿼리스트링에 그대로
   붙입니다. "Decoding" 키를 넣으면 이중 인코딩되어 인증에 실패합니다.
3. `backend/.env` 파일에 아래처럼 입력합니다.

   ```
   KMA_SERVICE_KEY=발급받은_인증키_Encoding
   ```

키가 없거나 호출에 실패(서비스 장애, 격자 변환 오류 등)해도 국내 도시는 자동으로 Open-Meteo로
폴백되므로 날씨 섹션이 죽는 일은 없습니다 — 다만 그 경우 온도가 네이버 날씨와 정확히 일치하지는
않을 수 있습니다. 승인 전 개발계정은 보통 일 1,000회, 승인 후에는 일 10,000회로 호출 한도가
늘어나는데, 이 프로젝트는 대시보드 새로고침당 국내 도시 1곳에 최대 2회(실황+예보) 호출하는
수준이라 이 한도 안에서 여유롭게 운영됩니다.

## 원본 데이터 캐시 (날씨/환율/뉴스)

날씨/환율/뉴스는 외부 API 원본 응답을 그대로 `raw_data_cache` 테이블에 저장해두고, 만료
전이면 외부 API를 다시 호출하지 않고 DB에 저장된 값을 그대로 돌려줍니다. 예전에는 프로세스
메모리(`sync.Mutex`/`RWMutex` + map) TTL 캐시였는데, Render 무료 티어처럼 인스턴스가
슬립했다 깨어나거나 재배포될 때 메모리가 통째로 초기화되어 캐시도 함께 사라지고, 재시작
직후 여러 요청이 한꺼번에 외부 API를 다시 두드리는 문제가 있었습니다. DB에 저장해두면
프로세스가 재시작돼도 캐시가 그대로 남아있습니다.

| 데이터 | 캐시 키 형식 | TTL |
|---|---|---|
| 날씨 | `weather:{도시}` | 10분 |
| 환율(현재값) | `exchange:{from}:{to}` | 30분 |
| 환율(7일 추이) | `exchange:weekly:{from}:{to}` | 30분 |
| 뉴스 | `news:{region}:{category}` | 30분 |

환율은 현재값과 7일 추이를 서로 다른 캐시 키에 독립적으로 저장합니다 — 하나로 묶어
저장했다면, 현재값 조회는 성공하고 7일 추이 조회만 실패한 순간의 결과가 "성공"으로
통째로 캐싱되어 그 TTL 내내 차트가 빈 채로 굳어버릴 수 있기 때문입니다. 키를
분리하면 7일 추이만 독립적으로 재시도할 수 있습니다.

```sql
CREATE TABLE raw_data_cache (
  cache_key TEXT PRIMARY KEY,
  data_json TEXT NOT NULL,
  fetched_at TEXT DEFAULT CURRENT_TIMESTAMP,
  expires_at TEXT NOT NULL
);
```

세 종류 모두 캐시 키에 접두사가 붙어 있어 서로 절대 섞이지 않습니다. 외부 API 호출이
실패했을 때 만료된 캐시라도 남아있으면, 화면을 완전히 비우는 대신 그 옛 데이터를
"잠정치"로 대신 보여줍니다(로그에 남습니다).

뉴스 헤드라인 번역(해외 모드)도 같은 방식으로 `news_translation_cache` 테이블에
`article_id` 기준으로 캐싱되어, 서버가 재시작돼도 같은 기사를 다시 Groq로 번역하지
않습니다.

## 날씨 예보 슬롯 캐시 (지난 시각도 예보값으로 복구)

날씨 카드의 오전 8시/오후 2시 슬롯은 `weather_slot_cache` 테이블에 (도시, 날짜, 시각)
단위로 영속 저장됩니다.

```sql
CREATE TABLE weather_slot_cache (
  city TEXT NOT NULL,
  slot_date TEXT NOT NULL,
  slot_time TEXT NOT NULL,
  temperature REAL NOT NULL,
  weather_code INTEGER NOT NULL,
  precipitation_probability INTEGER NOT NULL,
  source TEXT NOT NULL DEFAULT 'observed' CHECK (source IN ('observed', 'forecast')),
  updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (city, slot_date, slot_time)
);
```

기상청 단기예보(`getVilageFcst`)는 발표 시각(base_time) 이후 시점만 예보하므로, 그 발표
시각이 이미 지난 슬롯(예: 지금이 14시인데 08시 슬롯을 다시 조회)은 응답에서 아예 빠집니다.
이를 두 단계로 처리합니다.

1. 슬롯 시각이 아직 지나지 않았는데 값이 없으면 `not_yet_available`("곧 발표될 예정입니다") —
   정상 상황입니다.
2. 슬롯 시각이 이미 지났는데 캐시에도 없으면, "지금 기준 최신 발표"가 아니라 그 슬롯
   시각보다 **이전에 발표된** 단기예보 회차로 소급 조회합니다(예: 08시 슬롯이면 05시
   발표분) — 그 발표 시점 기준으로는 08시가 여전히 "미래"라서, 몇 시간 뒤에 다시 조회해도
   항상 값이 나옵니다. 그마저도 실패하면 `past_missing`("일시적으로 가져오지 못했습니다")
   으로 표시됩니다.

이렇게 복구된 값은 `source` 컬럼에 `forecast`로 표시되어(제때 확보된 값은 `observed`)
DB에 저장되고, 프론트엔드 날씨 카드에 "예보치" 배지 + 툴팁("실시간 관측 시점을 놓쳐
예보값으로 대체되었습니다")으로 안내됩니다. 다만 AI 브리핑 문장을 생성할 때는 이 구분을
쓰지 않고 값을 그대로 활용합니다 — 신뢰도 자체는 실측과 다르지 않기 때문입니다. 해외
도시는 이 문제 자체가 없습니다 — Open-Meteo는 당일 00:00부터의 시간별 데이터를 항상
통째로 돌려주므로, 지난 시각도 처음부터 채워져 있습니다.

## 로또 섹션

동행복권이 자동화된 요청을 차단하는 것으로 보여(짧은 시간에 여러 회차를 병렬로
긁어오면 이후 요청을 응답 없이 드롭), 데이터 수집 방식을 세 갈래로 나눴습니다 —
"한꺼번에 많이 긁어오기"를 아예 없애고, 최소한의 자동 갱신 + 막혔을 때의 수동
대체 수단으로 구성합니다.

### 1. 초기 데이터는 정적 시드 파일

`backend/data/lotto_seed.json`을 `go:embed`로 바이너리에 포함시켜, 서버가 처음
뜰 때 `lotto_draws`가 비어있으면 dhlottery를 전혀 호출하지 않고 이 파일 내용을
그대로 DB에 넣습니다(이미 데이터가 있으면 매 재시작마다 아무 것도 하지
않으므로 안전합니다). 형식:

```json
[
  { "drwNo": 1185, "drwDate": "2025-08-23", "numbers": [3, 12, 19, 27, 33, 41], "bonus": 7 }
]
```

이 파일은 개발자가 신뢰할 수 있는 출처에서 직접 채워 넣는 정적 데이터로
취급합니다 — 지어낸 값을 넣어두면 실제 당첨번호인 것처럼 화면에 노출될
위험이 있으므로, 절대 임의로 생성한 번호를 커밋하지 않습니다. 이후 회차는
아래 "관리자 API"로 채우거나, 이 파일 자체를 편집한 뒤 DB를 비우고
재시작하면 됩니다.

### 2. 자동 수집 — 주 1회, 최신 회차 1개만

데이터 수집은 화면의 "🔄 매주 자동 업데이트: ON/OFF" 토글로만 켜고 끕니다
(`POST /api/lotto/collection/start` / `/stop`, `GET /api/lotto/collection/status`) —
서버 시작 시 자동으로 걸리지 않습니다. ON으로 켜면 백그라운드 goroutine이
`time.Ticker`로 24시간마다(토글을 켠 즉시 1회 포함) 딱 한 가지만 확인합니다:
DB에 저장된 최신 회차의 다음 번호가, 2002-12-07(1회차)부터 매주 토요일
추첨되는 주기로 계산했을 때 이미 발표됐어야 할 시점인지. 그렇다면 그 회차
**하나만** 조회를 시도합니다 — 여러 회차를 동시에 병렬로 긁어오는 로직은
완전히 제거했습니다.

**원래는 dhlottery(`https://www.dhlottery.co.kr/common.do?method=getLottoNumber`)를
서버가 직접 호출했지만, dhlottery가 이 서버의 IP를 차단해 자동 요청이 계속
실패하는 상태가 됐습니다.** 이를 우회하기 위해, 커뮤니티가 유지 관리하는
공개 GitHub 저장소 [`smok95/lotto`](https://github.com/smok95/lotto)에서
회차 데이터를 가져오도록 바꿨습니다(`fetchLottoDrawFromGitHub`,
`backend/lotto.go`) — 이 저장소는 매주 토요일 추첨 직후(실측: 2026-07-25/
08-01/08-08 모두 KST 20:41~21:00 사이 커밋)에 회차별 JSON 파일
(`https://raw.githubusercontent.com/smok95/lotto/main/results/{회차}.json`)을
자동 커밋해 그대로 서빙하므로, dhlottery를 전혀 두드리지 않고도 최신 회차를
얻을 수 있습니다.

dhlottery를 직접 호출하던 원래 코드(`fetchLottoDraw`/
`fetchLottoDrawWithShortRetry`)는 **삭제하지 않고 `backend/lotto.go`에 그대로
남겨뒀지만, 자동 수집 경로에서는 더 이상 호출하지 않습니다** — 이미 이
서버의 IP가 차단된 상태라 폴백으로 다시 두드려봐야 실패만 반복할 뿐이고,
그 재시도 자체가 차단을 더 굳히는 원인이 될 수도 있기 때문입니다. GitHub
소스가 실패하면(파일이 아직 없거나 형식이 바뀐 경우) 그대로 다음 정기
점검까지 기다립니다 — dhlottery로의 폴백은 없습니다. 나중에 dhlottery
접근이 다시 가능해지거나 이 GitHub 저장소가 더 이상 유지되지 않게 되면,
남겨둔 코드를 다시 연결해서 쓸 수 있습니다.

GitHub 소스가 계속 실패하면 아래 "관리자 API"로 수동 입력할 수 있습니다. 상태
응답은 `lastCollectedAt`(마지막으로 신규 회차를 저장한 시각),
`lastCheckedAt`/`nextCheckAt`(마지막/다음 점검 시각), `savedCount`를 담습니다.

### 3. 관리자 API — dhlottery가 막혔을 때의 수동 대체 수단

자동 수집이 계속 실패한다면(예: dhlottery가 이 서버의 IP를 아예 차단), 회차를
수동으로 채워 넣을 수 있는 관리자 전용 API가 있습니다. **프론트엔드 화면
어디에도 이 기능으로 연결되는 버튼이나 메뉴가 없습니다** — 순수하게 API로만
사용합니다.

`backend/.env`에 `ADMIN_SECRET_KEY`를 설정하면 활성화됩니다(비어있으면 두
엔드포인트 모두 503을 반환합니다).

- `POST /api/admin/lotto/manual-entry` — 회차 하나를 저장/정정합니다.
  ```bash
  curl -X POST https://your-app.onrender.com/api/admin/lotto/manual-entry \
    -H "X-Admin-Key: {ADMIN_SECRET_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"drwNo":1187,"drwDate":"2025-08-16","numbers":[3,12,19,27,33,41],"bonus":7}'
  ```
  `numbers`는 정확히 6개, 1~45 범위이며 중복이 없어야 하고, `bonus`도 1~45
  범위이면서 `numbers`와 겹치지 않아야 합니다 — 하나라도 어긋나면 400과 함께
  구체적인 사유를 반환합니다. 이미 저장된 회차 번호로 다시 요청하면(오타 정정
  등) 기존 값을 덮어씁니다.
- `GET /api/admin/lotto/status` — 현재 저장된 회차의 최소/최대 번호와, 그
  범위 안에서 비어있는(아직 못 채운) 회차 목록을 보여줍니다. 어느 회차부터
  `manual-entry`로 채워야 하는지 알아내는 용도입니다.
  ```bash
  curl https://your-app.onrender.com/api/admin/lotto/status -H "X-Admin-Key: {ADMIN_SECRET_KEY}"
  ```

여러 회차를 한 번에 채워 넣으려면 `scripts/manual_lotto_entry.sh`를 참고하세요
— 파일 안의 배열에 회차를 여러 개 적어두고 한 번 실행하면 하나씩 순차적으로
제출합니다.

`X-Admin-Key`가 없거나 틀리면 401을, `ADMIN_SECRET_KEY` 자체가 설정되지 않았으면
503을 반환합니다 — 두 경우 모두 DB에는 전혀 접근하지 않습니다.

### 4. 이번 주 추천 번호 — 4단계 파이프라인

"이번 주 추천 번호"는 4단계로 계산됩니다(`backend/lotto_recommendation_pipeline.go`):

1. **빈도 집계**: 최근 50회 데이터에서 번호별 출현 횟수와, 마지막 출현
   이후 몇 회차가 지났는지(미출현 기간)를 계산합니다.
2. **가중치 정책 선택**: 화면에서 고를 수 있는 세 가지 모드 중 하나로
   가중 랜덤 샘플링을 합니다 — `trend`(🔥 고빈도 번호 우선, 출현 빈도
   상위 12개에 가중치 부여, 화면 기본 선택값), `regression`(❄️ 저빈도
   번호 우선, 미출현 기간이 가장 긴 12개에 가중치 부여), `uniform`
   (⚖️ 완전 무작위). `mode` 파라미터 없이 API를 호출하거나 잘못된 값을
   주면 `uniform`으로 대체됩니다 — 화면 기본 선택값인 `trend`와는 별개로,
   프론트엔드가 항상 명시적으로 `mode`를 붙여 요청하므로 이 대체값이
   실제로 쓰이는 경우는 거의 없습니다. 이 가중치는 과거 출현 패턴을
   재미로 반영한 것일 뿐, 실제 당첨 확률은 매 회차 독립적으로
   균등합니다 — 추첨 기계는 지난 회차를 기억하지 못합니다.
3. **패턴 필터 검증**: 뽑힌 6개 조합이 홀짝 비율(2:4~4:2), 구간 분포
   (1-9/10-19/20-29/30-39/40-45 중 한 구간에 4개 이상 몰리지 않음), 총합
   (100~170), 연속번호(3개 이상 연속 금지), 직전 회차 중복(2개 이상
   금지) 조건을 모두 만족하는지 확인합니다. 하나라도 벗어나면 2단계로
   돌아가 재추출하며, 최대 1000회를 재추출해도 못 찾으면 마지막 후보를
   그대로 쓰고 경고 로그를 남깁니다(실제로는 무작위 6개 조합 대부분이
   이미 통과하므로 이 상한에 도달하는 경우는 극히 드뭅니다).
4. **결과 출력**: 통과한 세트 1개를 홀짝비/합계/구간분포/직전회차중복
   통계와 함께 오름차순으로 반환합니다.

캐싱은 `(cycle_start_date, mode)` 복합키로 이루어집니다 — 같은 주기(cycle)
안에서 화면에서 uniform → trend → uniform으로 모드를 오가도, 각 모드가
처음 계산됐을 때의 세트를 독립적으로 캐싱해뒀다가 그대로 재사용합니다
(다시 계산하지 않습니다). 회차가 갱신되거나 이미 저장된 회차의 오타가
정정되어 `based_on_data_hash`가 지금의 frequency와 달라지면, 그 시점에
조회되는 모드부터 순차적으로 재계산됩니다 — 다른 모드의 캐시는 그
모드가 다음에 조회될 때 각자 독립적으로 무효화됩니다.

이번 회차 판매가 마감되는 토요일 20:00 KST부터 다음 회차 번호가 공개되는
일요일 06:00 KST까지는 추천을 아예 숨기고(`isBlackout: true`)
`nextAvailableAt`으로 다음 공개 시각만 안내합니다.

`GET /api/lotto?mode={uniform|trend|regression}`로 원하는 모드를 지정할 수
있고, 값이 없거나 잘못된 값이면 `uniform`으로 대체됩니다.

### 그 외

- 통계(번호별 출현 횟수, 최근 10회 출현 번호)는 Go가 아니라 DB의
  `UNION ALL` + `GROUP BY`로 집계합니다.
- AI 인사이트는 `ai_insight_cache` 테이블에 `(latest_drw_no, data_hash,
  prompt_version)` 기준으로 캐싱되어, 새 회차가 추가되거나 기존 회차
  데이터가 정정돼 통계 입력이 바뀌기 전까지는 Groq를 다시 호출하지
  않습니다. `prompt_version`(`backend/lotto_ai.go`의
  `insightPromptVersion` 상수, 현재 `"v3"`)이 캐시 키에 포함된 이유는,
  회차/통계가 그대로인 상태에서 system prompt 문구만 바꿔도 캐시가
  자동으로 무효화되어야 하기 때문입니다 — 이 컬럼이 없던 시절에는
  프롬프트를 "정확히 3문장, disclaimer 금지"로 고쳤는데도 이미 저장된
  예전 4문장짜리 캐시가 계속 서빙되는 사고가 실제로 있었습니다.
  **system prompt 내용을 바꿀 때마다 `insightPromptVersion` 문자열도
  반드시 함께 올려야** 이 문제가 재발하지 않습니다. 프롬프트는 정확히
  3문장(최다 출현 번호/최소 출현 번호/최근 10회 특이 번호)만 요구하고
  disclaimer나 마무리 문장은 붙이지 않습니다 — 그래도 모델이 습관적으로
  disclaimer성 문장을 다시 붙이는 경우에 대비해
  `stripLeakedDisclaimer`(`backend/lotto_ai.go`)가 캐시 히트/신규 생성
  양쪽 모두에서 "※"나 "통계적 재미"가 든 문장을 한 번 더 걸러냅니다.

### DB 스키마

```sql
CREATE TABLE lotto_draws (
  drw_no INTEGER PRIMARY KEY,
  drw_date TEXT,
  num1 INTEGER, num2 INTEGER, num3 INTEGER,
  num4 INTEGER, num5 INTEGER, num6 INTEGER,
  bonus_no INTEGER,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_insight_cache (
  latest_drw_no INTEGER PRIMARY KEY,
  insight_text TEXT NOT NULL,
  data_hash TEXT NOT NULL DEFAULT '',
  prompt_version TEXT NOT NULL DEFAULT '',
  generated_at TEXT DEFAULT CURRENT_TIMESTAMP
);

-- (cycle_start_date, mode) 복합 기본키 — 사이클마다, 그리고 그 사이클
-- 안에서 선택된 모드마다 정확히 한 행만 존재한다. numbers/stats_json은
-- 세트 1개(번호 6개와 그 통계)를 담는다. number_groups는 예전 방식의
-- 흔적으로 더 이상 쓰지 않지만 컬럼 자체는 남아있다.
CREATE TABLE lotto_recommendation (
  cycle_start_date TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'uniform',
  based_on_drw_no INTEGER NOT NULL DEFAULT 0,
  based_on_data_hash TEXT NOT NULL DEFAULT '',
  numbers TEXT NOT NULL,
  number_groups TEXT NOT NULL DEFAULT '',
  stats_json TEXT NOT NULL DEFAULT '{}',
  generated_at TEXT DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (cycle_start_date, mode)
);

-- 날씨/환율/뉴스 AI 브리핑 문단 캐시. section 값은 "weather", "exchange",
-- "news:{region}:{category}" 형태다.
CREATE TABLE briefing_section_cache (
  section TEXT PRIMARY KEY,
  data_hash TEXT NOT NULL,
  detailed_text TEXT NOT NULL,
  generated_at TEXT DEFAULT CURRENT_TIMESTAMP
);
```

SQLite/libSQL은 MySQL과 달리 컬럼/DB 단위 charset 설정이 없습니다 — 문자열은 항상
UTF-8이라, 한글 텍스트 INSERT가 charset 불일치로 실패하는 문제 자체가 존재하지
않습니다(MySQL 시절에는 이 문제를 피하려고 테이블마다 `utf8mb4`를 명시했었습니다).

### DB 연결 설정

환경변수로 제어합니다 (`backend/.env.example` 참고):

| 변수 | 설명 |
|---|---|
| `TURSO_DATABASE_URL` | Turso 데이터베이스 URL (예: `libsql://your-db-name-your-username.turso.io`) |
| `TURSO_AUTH_TOKEN` | Turso 인증 토큰 |

**둘 다 비워두면** 서버가 자동으로 로컬 파일(`backend/data/dashboard.db`, `LOCAL_DB_PATH`로
경로 변경 가능)로 폴백합니다 — 로컬 개발 시 기본 동작입니다.

로컬에서 파일 대신 배포용과 같은 원격 Turso DB로 테스트해보고 싶다면, `backend/.env`에
배포 환경과 동일한 `TURSO_DATABASE_URL`/`TURSO_AUTH_TOKEN` 값을 넣으면 됩니다. 다만 이 경우
**로컬과 배포 서버가 완전히 같은 데이터를 공유**합니다 — 로컬에서 로또 데이터 수집을 켜거나
캐시를 채우면 그대로 배포 서버에도 반영됩니다. 이 둘을 분리하고 싶다면 로컬 전용 Turso
DB를 하나 더 만들거나(`turso db create dashboard-db-dev`), `.env`에서 두 값을 비워 로컬
파일 DB로 되돌리면 됩니다.

원격(Turso) 연결은 `SetMaxOpenConns(10)` / `SetMaxIdleConns(5)` / `SetConnMaxLifetime(3분)`으로
설정되어 있습니다 — 클라우드 제공자가 유휴 연결을 끊어버리는 경우, 그보다 먼저 커넥션을
재활용해서 "connection reset" 에러를 피하기 위함입니다. 로컬 파일 연결은 반대로
`SetMaxOpenConns(1)`로 강제합니다 — SQLite/libSQL은 동시 쓰기 연결이 여러 개면 "database is
locked" 에러가 나기 쉬운데, 커넥션 풀을 1개로 좁히면 모든 쿼리가 자연히 직렬화되어 이
문제가 원천적으로 사라집니다(개인용 대시보드 수준의 트래픽에서는 그로 인한 성능 손해가
무시할 만합니다).

DB 연결에 실패해도 서버는 죽지 않습니다. 로또 섹션만 DB 에러 상태로 응답하고,
날씨/환율/뉴스/브리핑은 평소대로 동작합니다(다만 원본 데이터/브리핑/뉴스 번역 캐싱은
꺼진 채로 매번 다시 계산·호출합니다).

마이그레이션은 서버 시작 시 한 번만 실행되는 일회성 작업이라, 그 시점에 Turso 인프라가
잠깐(몇 초~십수 초) 흔들리고 있으면 실제로는 금방 끝날 장애인데도 로또 섹션이 재배포
전까지 계속 비활성화된 채로 남는 문제가 있었습니다. 이를 완화하기 위해
`connectDBWithRetry`(`backend/db.go`)가 연결+마이그레이션 전체 시퀀스를 3초 간격으로 최대
4회 재시도한 뒤에만 실패를 확정합니다(최악의 경우 서버 시작이 약 9초 지연되지만, 시작 시
단 한 번만 감수하는 비용입니다).

그래도 최종 실패하면 에러 메시지를 `classifyDBErrorType`으로 분류해 원인을 구분합니다:
"upstream forward failed" 문구나 5xx 상태 코드(502 Bad Gateway 포함)가 메시지에 있으면
Turso 인프라 자체의 장애(`turso_outage`)로, 그 외(인증 토큰 오류, DNS/타임아웃 등)는 우리
쪽 설정 문제일 수 있는 일반 연결 실패(`connection_failed`)로 분류합니다. 이 분류 결과는
`GET /api/lotto` 응답의 `dbErrorType` 필드로 함께 내려가며, 프론트엔드는 `turso_outage`일
때 "일시적인 장애이니 잠시 후 자동 복구됩니다" 안내와 [Turso 상태
페이지](https://status.turso.tech) 링크를 보여주고, 그 외에는 기존처럼 "⚠️ 데이터베이스에
연결할 수 없습니다" + 재시도 버튼을 보여줍니다.

## 프론트엔드 디자인 시스템

`frontend/src/index.css`의 `:root`에 다크/라이트 두 모드용 토큰을 정의하고,
나머지 스타일(`App.css`)은 이 토큰만 참조합니다.

- **서페이스는 톤온톤 램프**: `--surface-0`(배경)~`--surface-3`(가장 밝은 패널)까지
  한 색조 안에서 명도만 다른 4단계를 두고, `--panel`/`--panel-alt`/`--panel-sunken`이
  그 단계를 가리킵니다. 카드 배경/테두리/스켈레톤이 전부 이 램프 하나에서 나오므로
  임의로 색을 더할 필요 없이 위계만으로 깊이감을 만듭니다.
- **정체성 액센트는 섹션 전용**: `--accent-weather`/`-exchange`/`-news`/`-briefing`/`-lotto`는
  각 카드를 식별하는 용도로만 씁니다(카드 상단 바, 탭 활성 배경 등). 예전에는 전역
  토글/포커스 링에도 `--accent-weather`를 재사용했는데, 그러면 같은 청록색이 "날씨"와
  "그냥 활성 상태"라는 두 가지 의미를 동시에 가리키게 되어 정작 날씨 카드를 봐도 그
  색이 왜 거기 있는지 헷갈렸습니다. 카드 정체성과 무관한 범용 상호작용(토글, select
  포커스, "조회" 버튼의 평상시 강조)은 별도 토큰 `--accent-interactive`(현재
  `--accent-briefing`의 별칭)를 씁니다.
- **로또 공은 의도적 예외**: 나머지 카드는 채도를 낮춘 차분한 톤을 쓰지만, 로또 공
  (`LottoBall.tsx`)만은 동행복권 실제 색상 그대로(1-10 노랑 `#fbc400`, 11-20 파랑
  `#69c8f2`, 21-30 빨강 `#ff7272`, 31-40 회색 `#aaaaaa`, 41-45 초록 `#b0d840`) 선명하게
  남겨뒀습니다. 한 번 채도를 낮춰봤지만, 그러면 그 색이 실제 당첨 번호대를 가리키는
  신호라는 걸 알아보기 어려워졌고, 로또 섹션 전체가 다른 카드들 사이에서 시선을
  끄는 포인트 컬러 역할도 함께 사라졌습니다.
- **타이포그래피 위계**: 환율의 큰 숫자(`.exchange__rate`, 44px/700)나 날씨 온도
  (`.weather__period-temp`, 25px/700)처럼 카드의 핵심 데이터는 크고 굵게, 기준일·
  응답 시간 같은 메타 정보는 작고 흐리게(`--text-faint`) 대비를 분명히 뒀습니다.
  카드 제목(`.card__title`)과 로또 섹션 라벨(`.lotto__section-title`)은 크기/자간/색상을
  동일한 스케일로 맞춰, 카드 제목과 카드 내부 소제목이 같은 위계 톤으로 읽히게
  했습니다.
- **날씨 아이콘**: 이모지 대신 `frontend/src/components/weather/WeatherIcon.tsx`에서
  line + duotone 스타일로 직접 그린 SVG 7종(맑음/구름조금/흐림/안개/비/눈/뇌우)을
  씁니다. 윤곽선은 `currentColor` 실선, 몸체(태양 원반·구름 실루엣)는 `currentColor`를
  옅게 채운 것이라, 렌더링하는 쪽에서 CSS `color`만 지정하면(현재는
  `--accent-weather`) 다크/라이트 테마 전환 시 별도 처리 없이 그대로 맞춰집니다.
- **시그니처 디테일**: 환율 차트(`ExchangeChart.tsx`)의 선 아래에 은은한 그라데이션
  영역을 채우고, 마지막(오늘) 데이터 포인트에만 펄스 링 애니메이션을 그립니다. recharts가
  매 렌더마다 SVG를 새로 그리는 환경에서도 애니메이션이 끊기지 않도록 CSS 대신 SVG
  네이티브 `<animate>`(SMIL)를 씁니다.

**그리드 아이템에는 `min-width: 0`/`min-height: 0`을 꼭 같이 둘 것** — `.card`와
`.dashboard-grid__row1`처럼 flex/grid 아이템으로 쓰이는 요소는 두 속성을 함께
선언해야 합니다. 한쪽만 두면, 자식 안에 (예: `.briefing__section`의 "방금 갱신됨"
하이라이트용 음수 마진처럼) `overflow: hidden`으로 시각적으로는 가려지는 요소가
있어도 그 min-content 크기가 그리드 트랙의 최소 너비 계산에는 그대로 반영되어,
카드 자체는 멀쩡해 보이는데 페이지 전체가 뷰포트보다 몇 px 더 넓어져 조용히 가로
스크롤이 생기는 문제가 실제로 있었습니다(390px 폭에서 재현·확인).

## 프로덕션 빌드 (백엔드가 프론트엔드를 정적으로 서빙)

Vite 빌드 결과물은 `go:embed` 로 백엔드 바이너리에 포함할 수 있도록
`backend/static/` 으로 직접 출력되게 설정되어 있습니다 (`frontend/vite.config.ts`).

```bash
cd frontend
npm run build          # 결과물이 ../backend/static 에 생성됨

cd ../backend
go build -o dashboard-server .
./dashboard-server      # http://localhost:8080 에서 API + 정적 파일 모두 서빙
```

recharts(환율 차트가 쓰는, 용량이 큰 라이브러리)와 로또 카드의 회차 목록/히트맵처럼
스크롤해야 보이는 무거운 컴포넌트는 `React.lazy` + `Suspense`로 지연 로딩되고,
`vite.config.ts`의 `manualChunks`가 `react`/`react-dom`을 `vendor-react` 청크로,
recharts와 그 의존성을 `recharts` 청크로 따로 묶어 초기 로딩에 필요한 번들 크기를
줄입니다.

## 배포 가이드 (무료 Turso + 백엔드 호스팅)

### 1. Turso 무료 데이터베이스

1. https://turso.tech 에서 가입합니다 (신용카드 불필요).
2. CLI를 설치합니다.

   ```bash
   curl -sSfL https://get.tur.so/install.sh | bash
   ```
3. CLI로 로그인하고 데이터베이스를 만듭니다.

   ```bash
   turso auth signup    # 이미 가입했다면: turso auth login
   turso db create dashboard-db
   ```

   > **브라우저를 띄울 수 없는 환경(서버, CI, 샌드박스 등)이라면**: `turso auth login`/
   > `signup`은 로컬 브라우저를 열어야 완료되는데, `--headless` 옵션을 줘도 이런 환경에서는
   > 세션이 제대로 이어지지 않아 로그인이 끝나지 않을 수 있습니다. 이 경우 Platform API
   > Token을 대신 씁니다: [app.turso.tech](https://app.turso.tech) 대시보드(다른 기기의
   > 브라우저에서 직접 로그인)의 **Account Settings → API Tokens**에서 토큰을 발급받은 뒤
   >
   > ```bash
   > export TURSO_API_TOKEN=발급받은_토큰
   > turso db create dashboard-db   # turso auth login 없이 바로 동작
   > ```
   >
   > 이렇게 하면 `turso auth login` 단계 자체를 건너뛰고 모든 `turso` 명령을 바로 쓸 수
   > 있습니다.
4. 접속 정보를 확인합니다.

   ```bash
   turso db show dashboard-db --url     # TURSO_DATABASE_URL 값
   turso db tokens create dashboard-db  # TURSO_AUTH_TOKEN 값
   ```

### 2. 백엔드 호스팅 (Render 등)

환경변수에 아래 값을 등록합니다.

```
TURSO_DATABASE_URL=<turso db show --url 결과>
TURSO_AUTH_TOKEN=<turso db tokens create 결과>
GROQ_API_KEY=<Groq API 키>
```

Render 무료 웹 서비스는 15분간 요청이 없으면 슬립 상태가 되고, 이후 첫 요청에서
컨테이너를 다시 띄우는 콜드스타트가 발생합니다(수십 초 지연). 무료 티어의 알려진
제약이니 참고하세요. Turso 자체는 서버리스라 이 콜드스타트와 무관하게 항상 즉시
응답합니다 — 예전 Aiven MySQL처럼 유휴 연결이 끊어지는 것을 걱정할 필요가 없습니다
(그래도 재연결 안전망으로 `SetConnMaxLifetime(3분)`은 그대로 유지했습니다).

## API 응답 형태

`GET /api/dashboard`는 NDJSON(줄바꿈으로 구분된 JSON) 스트림 두 줄을 반환합니다. 뉴스는
더 이상 이 응답에 포함되지 않고 별도의 `/api/news` 엔드포인트로 분리되어 있습니다(아래 참고) —
카테고리/지역을 바꿔도 날씨·환율은 재요청되지 않게 하기 위한 구조입니다.

- `stage: "partial"` — 날씨/환율(병렬 수집)이 끝나는 즉시 전송됩니다.
- `stage: "final"` — 위 결과와 뉴스를 바탕으로 생성되는 AI 브리핑까지 포함해 마지막에 전송됩니다.

이렇게 나눈 이유는 AI 브리핑이 날씨/환율/뉴스 결과에 의존하는 순차 단계라서,
브리핑이 끝날 때까지 나머지 카드까지 기다리게 하지 않기 위함입니다.
프론트엔드는 이를 이용해 날씨/환율 카드를 먼저 채우고,
AI 브리핑 카드만 스켈레톤 상태로 대기시킵니다.

각 섹션(`weather`/`exchange`/`briefing`)은 공통으로
`success`, `durationMs`, `error?`, `data?` 필드를 가지며, 한 섹션이 실패해도
나머지 섹션 응답에는 영향을 주지 않습니다. 정확한 타입은
`backend/models.go` 와 `frontend/src/types.ts` 에 동일하게 정의되어 있습니다.

`GET /api/news?category={category}&region={domestic|international}`도 `/api/dashboard`와
별도의 단발성 JSON 응답입니다(NDJSON 아님). 같은 `success`/`durationMs`/`error?`/`data?` 형태를
따릅니다.

- `category`: `top`/`business`/`technology`/`sports`/`entertainment`/`health`/`science` 중 하나
  (기본값 `top`).
- `region`: `domestic`(기본값, `country=kr&language=ko`) 또는 `international`(`language=en`).
- `data.items`: 최대 5건, 각 항목은 `id`, `title`, `link`, `sourceName`, `pubDate`, `description`을
  가지며, `region=international`인 경우에만 `translatedTitle`(번역 실패 시 빈 문자열)이 채워집니다.
- 카테고리와 지역을 모두 캐시 키에 포함하므로, 조합이 다르면 서로 캐시를 공유하지 않습니다
  (`getCachedOrFetchNews`, 30분 TTL). AI 브리핑의 뉴스 문단도 같은 카테고리/지역 조합별로
  독립적으로 캐싱됩니다(`briefing_section_cache`의 `section` 값이 `news:{region}:{category}` 형태).

`GET /api/lotto`도 `/api/dashboard`와 별도의 단발성 JSON 응답입니다(NDJSON 아님).
같은 `success`/`durationMs`/`error?`/`data?` 형태를 따르며, `?mode={uniform|trend|regression}`
쿼리 파라미터를 받습니다(위 "이번 주 추천 번호" 참고). `data`는 다음 필드를 가집니다.

- `latest`: 최신 회차 1건 (`drwNo`, `drwDate`, `numbers`, `bonus`)
- `history`: 최근 50회 목록, 최신순
- `frequency`: 번호(1~45, 문자열 키) → 최근 50회 중 출현 횟수
- `recentAppeared`: 최근 10회 동안 출현한 번호(중복 제거)
- `aiInsight`: `{ available, text, cached, generatedAt? }` — `available`이 `false`면
  `text`는 안내 메시지이고 나머지 필드는 무시하면 됩니다.
- `recommendation`: `{ isBlackout, mode?, set?, cycleStartDate?, generatedAt?, nextAvailableAt? }` —
  `isBlackout`이 `true`면 판매 마감~신규 회차 공개 사이라 번호 추천이
  숨겨진 상태이고, `nextAvailableAt`에 다음 공개 시각이 담깁니다. 그 외에는
  `mode`(요청받은 가중치 정책)와 `set: { numbers, stats }`가 채워집니다 —
  `stats`는 `{ oddEvenRatio, sum, bandDistribution, overlapWithPrevious }`
  형태입니다(위 "이번 주 추천 번호" 참고).

`GET /healthz`는 DB/외부 API를 전혀 건드리지 않고 프로세스가 요청을 받을 수
있으면 즉시 200을 반환합니다 — Render 등 플랫폼 헬스체크 전용이며(위
`render.yaml`의 `healthCheckPath` 참고), dhlottery/Groq/NewsData.io 장애가
헬스체크 실패로 오인되어 불필요한 재시작이 도는 것을 막습니다.

## 동시성 설계

- 날씨 / 환율: `sync.WaitGroup` + 섹션별 독립 `context.WithTimeout` 로 병렬 처리(환율 등
  대부분의 섹션은 8초, 날씨만 21초 — 바로 아래 참고). 한 섹션이 실패하거나 타임아웃되어도
  나머지 섹션은 정상적으로 응답합니다.
- 날씨(국내 도시): 기상청 API 자체는 그 21초 예산 중 최대 9초(`kmaSubTimeout`)만 쓰도록
  하위 컨텍스트로 감싸서 시도합니다 — 기상청 쪽이 느리거나 실패해도 Open-Meteo로 폴백할
  시간이 남도록 하기 위함입니다. 나머지 예산은 이미 지난 시각 슬롯(08:00/14:00)을 소급
  복구하는 시도(`backfillFetchTimeout`, 역시 9초)를 위한 것입니다 — 재시도 로직이 최소
  한 단계는 도중에 잘리지 않고 온전히 시도할 수 있어야, data.go.kr 게이트웨이가 잠깐
  느려진 것뿐인 상황에서 복구 가능한 데이터를 "일시적으로 가져오지 못했습니다"로
  성급하게 포기하지 않습니다. 격자 좌표(nx/ny)는 위경도→LCC 변환
  함수(`latLonToGrid`)로 구현했지만, 실제로 쓰는 4개 도시는 기상청이 공개한 격자표 값을
  그대로 하드코딩했습니다(`domesticGrid`) — 도시 대표 위경도가 격자 경계에 걸리면 변환
  함수 결과가 1칸 어긋날 수 있어서입니다(대구가 실제로 그런 경우).
- 뉴스: NewsData.io `/api/1/latest`를 카테고리/지역 조합 하나당 한 번만 호출합니다.
  `getCachedOrFetchNews`의 30분 TTL DB 캐시(`raw_data_cache`)를 `GET /api/news` 핸들러와
  AI 브리핑의 내부 조회가 함께 사용하므로, 같은 조합을 브리핑과 뉴스 카드가 동시에
  요청해도 무료 티어 크레딧을 두 번 쓰지 않습니다. `region=international`일 때만 Groq로
  헤드라인 5개를 한 번의 JSON 모드 호출로 배치 번역하며, 번역 결과는 기사 id 기준으로
  별도 캐싱되어 같은 기사가 남아있는 동안 재번역하지 않습니다(`news_translation_cache`).
  캐시가 막 만료된 직후 두 요청(브리핑 내부 조회 + 뉴스 카드)이 거의 동시에 도착하면
  각자 "캐시 없음"을 보고 NewsData.io를 중복 호출하는 순간(cache stampede)이 있었는데,
  `coalesceNewsFetch`가 같은 category+region 조합의 동시 호출을 하나로 합쳐서(뒤에 온
  요청은 새로 조회하는 대신 먼저 온 요청의 결과를 기다렸다가 공유) 이를 막습니다.
- AI 브리핑: 날씨/환율/뉴스 결과가 모두 필요하므로 병렬이 아니라 순차 단계로 이어집니다
  (`backend/handler.go` 참고). 뉴스 문단의 캐시 키는 카테고리와 지역을 함께 포함하므로
  (`news:{region}:{category}`), 조합이 바뀌면 브리핑도 새로 생성됩니다. 각 문단의
  캐시 히트 여부는 `briefing_section_cache.data_hash`와, 이번 요청의 입력(날씨/환율/
  뉴스 원본 데이터 자체)을 해시한 값을 비교해서 결정합니다(`resolveBriefingSection`)
  — 조회 시각이나 category/region 문자열이 아니라 실제 콘텐츠를 해시하므로, 입력이
  그대로면 Groq를 다시 호출하지 않고 캐시된 문장을 그대로 재사용합니다. 뉴스 문단은
  헤드라인 배열을 해싱하기 전에 article id 기준으로 정렬한 사본으로 해시를 계산합니다
  (`hashNewsInput`) — NewsData.io가 같은 헤드라인 집합을 다른 순서로 돌려줘도(순서를
  보장한다는 문서가 없음) 콘텐츠가 같으면 캐시가 무효화되지 않게 하기 위함입니다.
  실제 Groq 프롬프트에 넣는 순서는 원래 순서 그대로 유지되며, 정렬은 오직 해시
  계산에만 적용됩니다.
- 로또: `GET /api/lotto`는 dhlottery를 전혀 건드리지 않고 DB에 있는 데이터만
  읽어서 보여줍니다 — 수집(초기 시드 + 매주 최대 1회 자동 확인)은 완전히
  분리된 백그라운드 경로입니다(위 "로또 섹션" 참고). 자동 확인 자체도 한 번에
  회차 하나만 조회하므로 별도의 동시성 제어(세마포어 등)가 필요 없습니다.
- 프론트엔드에서도 로또 섹션은 날씨/환율/브리핑과 별도의 훅(`useLotto`)으로,
  뉴스 섹션은 또 별도의 훅(`useNews`)으로 요청·로딩·에러·재시도 상태를 독립적으로
  관리합니다. 뉴스 카테고리/지역은 `useDashboard`의 `params`가 아니라 별도의
  `NewsContext`(ref)로 전달되어, 바꿔도 날씨/환율 재요청을 유발하지 않고
  `retrySection('briefing')`만 호출해 브리핑의 뉴스 문단만 갱신합니다. 선택한
  카테고리/지역은 URL 쿼리스트링(`?category=&region=`)에 저장되어 새로고침해도
  유지됩니다.
- "조회" 버튼(`useDashboard`의 `applyParams`)은 방금 선택한 도시/통화를 클릭
  직전의 값과 비교해, 실제로 바뀐 쪽에 해당하는 섹션만 로딩 상태로 표시합니다 —
  도시만 바꿨다면 날씨 카드와 브리핑의 날씨 문단만(`weatherPending`), 통화만
  바꿨다면 환율 카드와 브리핑의 환율 문단만(`exchangePending`) 스켈레톤으로
  바뀌고, 나머지 카드/문단은 이전 값을 그대로 유지합니다. 백엔드가 날씨·환율·
  브리핑을 한 요청에 함께 스트리밍하므로(섹션만 골라 계산하게 하는 파라미터가
  없음) 네트워크 요청 자체는 항상 전체를 다시 계산해 돌려주지만, 바뀌지 않은
  쪽은 원본 데이터 캐시/브리핑 문단 캐시에 걸려 사실상 동일한 값을 돌려받을
  뿐이고, 프론트엔드는 그 응답에서 실제로 바뀐 필드만 골라 반영합니다. 도시와
  통화가 둘 다 그대로면(조회 버튼이 눌릴 이유가 없는 상황이지만) 아무 요청도
  보내지 않습니다. 뉴스 카테고리/지역 변경(`retrySection('briefing')`)은 이
  선택적 갱신과 무관하게, 기존처럼 브리핑 카드 전체를 스켈레톤으로 덮습니다.
