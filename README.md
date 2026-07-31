# 브리핑 관제실 — 실시간 멀티API 대시보드 + AI 브리핑 + 로또

Go 백엔드가 날씨(국내 도시는 기상청 API, 해외 도시 및 폴백은 Open-Meteo) / 환율(Frankfurter) / 뉴스(NewsData.io)를 오픈 API로 병렬로 수집하고, 그 결과를 Groq LLM에 넘겨 한국어 한 줄 브리핑을 생성합니다.<br>
로또 섹션은 동행복권 공개 API로 회차 데이터를 DB(Turso/libSQL)에 영구 저장하고, 그 통계를 Groq AI API로 요약합니다.<br>
프론트엔드는 React + TypeScript(Vite)로 구성되어 있습니다.<br>
https://daily-news-o9mf.onrender.com/ 에서 확인 해 보실 수 있습니다.

## 프로젝트 구조

```
backend/    Go, 표준 라이브러리 위주 (net/http, sync, context) + Turso/libSQL(database/sql, SQLite 호환) + 오픈API 4종(기상청, Open-Meteo, Frankfuter, NewsData.io) + Groq API
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

- 데이터 출처: 동행복권 공개 API(`https://www.dhlottery.co.kr/common.do?method=getLottoNumber&drwNo={회차}`),
  인증 불필요.
- 데이터 수집은 화면의 "🔄 데이터 수집: ON/OFF" 토글 버튼으로만 켜고 끕니다
  (`POST /api/lotto/collection/start` / `/stop`, `GET /api/lotto/collection/status`) —
  서버 시작 시 자동으로 걸리지 않습니다. ON으로 켜면 2002-12-07(1회차)부터 매주 토요일
  추첨되는 주기로 계산한 "이론적 최신 회차"를 기준으로 DB에 없는 회차만 골라 백그라운드
  goroutine에서 순차적으로 채우고, 회차 하나를 가져올 때마다 즉시 저장합니다 — 중간에
  실패하거나 서버가 재시작돼도 이미 저장된 회차는 유지됩니다. `GET /api/lotto`는 이 수집을
  트리거하지 않고 DB에 있는 데이터만 읽어서 보여주며, 아직 수집된 회차가 없으면
  `isBackfilling` 상태로 응답해 프론트엔드가 "준비 중" 안내와 함께 자동으로 다시 확인합니다.
- dhlottery는 짧은 시간에 요청이 몰리면 이후 요청을 응답 없이 드롭하는 것으로 보여, 동시
  호출 수를 2개로 낮추고(`LOTTO_FETCH_CONCURRENCY`로 조정 가능) 요청 사이에 최소 400ms
  간격을 두며, 실패한 회차는 5초 → 15초 → 40초로 점점 늘려가며 재시도합니다. 기본
  Go 클라이언트 User-Agent 대신 일반 브라우저처럼 보이는 값을 명시적으로 지정해 차단
  가능성도 낮췄습니다.
- 통계(번호별 출현 횟수, 최근 10회 출현 번호)는 Go가 아니라 DB의
  `UNION ALL` + `GROUP BY`로 집계합니다.
- AI 인사이트는 `ai_insight_cache` 테이블에 `latest_drw_no` 기준으로 캐싱되어,
  새 회차가 추가되기 전까지는 Groq를 다시 호출하지 않습니다.

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
  insight_text TEXT,
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

DB 연결에 실패해도 서버는 죽지 않습니다. 로또 섹션만 "⚠️ 데이터베이스에 연결할 수
없습니다" 상태로 응답하고, 날씨/환율/뉴스/브리핑은 평소대로 동작합니다(다만 원본 데이터/
브리핑/뉴스 번역 캐싱은 꺼진 채로 매번 다시 계산·호출합니다).

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
같은 `success`/`durationMs`/`error?`/`data?` 형태를 따르며, `data`는 다음 필드를 가집니다.

- `latest`: 최신 회차 1건 (`drwNo`, `drwDate`, `numbers`, `bonus`)
- `history`: 최근 50회 목록, 최신순
- `frequency`: 번호(1~45, 문자열 키) → 최근 50회 중 출현 횟수
- `recentAppeared`: 최근 10회 동안 출현한 번호(중복 제거)
- `aiInsight`: `{ available, text, cached, generatedAt? }` — `available`이 `false`면
  `text`는 안내 메시지이고 나머지 필드는 무시하면 됩니다.

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
  (`news:{region}:{category}`), 조합이 바뀌면 브리핑도 새로 생성됩니다.
- 로또: `GET /api/lotto` 요청마다 먼저 신규 회차 동기화(`syncLottoDraws`)를 실행합니다.
  이미 최신 상태면 DB 조회 두 번(개수/최신 회차)만 하고 끝나므로 비용이 크지 않습니다.
  동기화 자체는 뮤텍스로 직렬화되어, 동시에 들어온 요청 여러 개가 같은 신규 회차를
  중복해서 insert 시도하지 않습니다. dhlottery API 호출은 세마포어(6개)로 동시성을
  제한합니다.
- 프론트엔드에서도 로또 섹션은 날씨/환율/브리핑과 별도의 훅(`useLotto`)으로,
  뉴스 섹션은 또 별도의 훅(`useNews`)으로 요청·로딩·에러·재시도 상태를 독립적으로
  관리합니다. 뉴스 카테고리/지역은 `useDashboard`의 `params`가 아니라 별도의
  `NewsContext`(ref)로 전달되어, 바꿔도 날씨/환율 재요청을 유발하지 않고
  `retrySection('briefing')`만 호출해 브리핑의 뉴스 문단만 갱신합니다. 선택한
  카테고리/지역은 URL 쿼리스트링(`?category=&region=`)에 저장되어 새로고침해도
  유지됩니다.
