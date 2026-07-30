# 브리핑 관제실 — 실시간 멀티API 대시보드 + AI 브리핑 + 로또

Go 백엔드가 날씨(국내 도시는 기상청 API, 해외 도시 및 폴백은 Open-Meteo) / 환율(Frankfurter) / 뉴스(NewsData.io)를 오픈 API로 병렬로 수집하고, 그 결과를 Groq LLM에 넘겨 한국어 한 줄 브리핑을 생성합니다.<br>
로또 섹션은 동행복권 공개 API로 회차 데이터를 MySQL에 영구 저장하고, 그 통계를 Groq AI API로 요약합니다.<br>
프론트엔드는 React + TypeScript(Vite)로 구성되어 있습니다.<br>
https://daily-news-o9mf.onrender.com/ 에서 확인 해 보실 수 있습니다.

## 프로젝트 구조

```
backend/    Go, 표준 라이브러리 위주 (net/http, sync, context) + MySQL(database/sql) + 오픈API 4종(기상청, Open-Meteo, Frankfuter, NewsData.io) + Groq API
frontend/   React + TypeScript (Vite)
```

## 빠르게 시작하기

### 0. 로컬 MySQL (Docker)

로또 섹션은 MySQL이 있어야 동작합니다(없어도 날씨/환율/뉴스/브리핑은 정상 동작).

```bash
docker run --name lotto-mysql \
  -e MYSQL_ROOT_PASSWORD=root_pw \
  -e MYSQL_DATABASE=dashboard \
  -e MYSQL_USER=dashboard \
  -e MYSQL_PASSWORD=dashboard_pw \
  -p 3306:3306 -d mysql:8
```

### 1. 백엔드 (Go)

```bash
cd backend
cp .env.example .env   # GROQ_API_KEY, DB_* 입력 (없어도 나머지 섹션은 정상 동작)
go run .
```

기본적으로 `http://localhost:8080` 에서 실행됩니다. `GET /api/dashboard?city=daegu&from=USD&to=KRW`,
`GET /api/lotto`, `GET /api/news?category=top&region=domestic` 로 확인할 수 있습니다. 서버 시작 시
`lotto_draws` / `ai_insight_cache` 테이블을 `CREATE TABLE IF NOT EXISTS`로 자동 생성합니다.

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
카테고리(`category`) + 지역(`region`) 조합별로 30분 TTL의 캐시(`getCachedOrFetchNews`)를 MySQL의
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
| 환율 | `exchange:{from}:{to}` | 30분 |
| 뉴스 | `news:{region}:{category}` | 30분 |

```sql
CREATE TABLE raw_data_cache (
  cache_key VARCHAR(100) PRIMARY KEY,
  data_json TEXT NOT NULL,
  fetched_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  expires_at DATETIME NOT NULL
);
```

세 종류 모두 캐시 키에 접두사가 붙어 있어 서로 절대 섞이지 않습니다. 외부 API 호출이
실패했을 때 만료된 캐시라도 남아있으면, 화면을 완전히 비우는 대신 그 옛 데이터를
"잠정치"로 대신 보여줍니다(로그에 남습니다).

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
- 통계(번호별 출현 횟수, 최근 10회 출현 번호)는 Go가 아니라 MySQL의
  `UNION ALL` + `GROUP BY`로 집계합니다.
- AI 인사이트는 `ai_insight_cache` 테이블에 `latest_drw_no` 기준으로 캐싱되어,
  새 회차가 추가되기 전까지는 Groq를 다시 호출하지 않습니다.

### DB 스키마

```sql
CREATE TABLE lotto_draws (
  drw_no INT PRIMARY KEY,
  drw_date DATE,
  num1 TINYINT, num2 TINYINT, num3 TINYINT,
  num4 TINYINT, num5 TINYINT, num6 TINYINT,
  bonus_no TINYINT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_insight_cache (
  latest_drw_no INT PRIMARY KEY,
  insight_text TEXT,
  generated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

두 테이블 모두 `utf8mb4`로 생성됩니다 — 서버/DB 기본 charset이 `latin1`인 환경(일부
관리형 MySQL의 기본값)에서는 한글 인사이트 텍스트 INSERT가 charset 불일치로 실패하기
때문에, 테이블 생성 시 charset을 명시적으로 지정합니다.

### DB 연결 설정

환경변수로 제어합니다 (`backend/.env.example` 참고):

| 변수 | 설명 |
|---|---|
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | 접속 정보 |
| `DB_USE_TLS` | `true`면 TLS로 연결 (Aiven 등 클라우드 MySQL은 대부분 필수) |
| `DB_CA_CERT_PATH` | (선택) TLS 서버 인증서를 검증할 CA 인증서 경로. 없으면 `DB_USE_TLS=true`여도 암호화만 하고 인증서 검증은 생략합니다 |

Connection pool은 `SetMaxOpenConns(10)` / `SetMaxIdleConns(5)` / `SetConnMaxLifetime(3분)`으로
설정되어 있습니다 — 클라우드 MySQL이 유휴 연결을 끊어버리는 경우, 그보다 먼저 커넥션을
재활용해서 "connection reset" 에러를 피하기 위함입니다.

MySQL 연결에 실패해도 서버는 죽지 않습니다. 로또 섹션만 "⚠️ 데이터베이스에 연결할 수
없습니다" 상태로 응답하고, 날씨/환율/뉴스/브리핑은 평소대로 동작합니다.

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

## 배포 가이드 (무료 MySQL + 백엔드 호스팅)

### 1. Aiven 무료 MySQL

1. https://aiven.io 에서 가입합니다 (신용카드 불필요).
2. 새 서비스 생성 → **MySQL** 선택 → 무료 플랜(1GB 저장공간, 이 프로젝트 규모엔 충분)으로 생성합니다.
3. 서비스 개요(Overview) 페이지에서 **Host**, **Port**, **User**, **Password**, **Default database name**을
   확인합니다. 이 값들을 각각 `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`에 사용합니다.
4. Aiven MySQL은 TLS 연결을 요구합니다. 같은 페이지에서 CA 인증서(`ca.pem`)를 다운로드해
   서버에 두고 `DB_CA_CERT_PATH`로 경로를 지정하면 인증서 검증까지 포함한 완전한 TLS
   연결이 됩니다(권장). 다운로드하지 않고 `DB_USE_TLS=true`만 설정해도 연결은 되지만,
   서버 인증서를 검증하지 않습니다.

### 2. 백엔드 호스팅 (Render 등)

환경변수에 아래 값을 등록합니다.

```
DB_HOST=<Aiven Host>
DB_PORT=<Aiven Port>
DB_USER=<Aiven User>
DB_PASSWORD=<Aiven Password>
DB_NAME=<Aiven DB명>
DB_USE_TLS=true
GROQ_API_KEY=<Groq API 키>
```

`DB_CA_CERT_PATH`를 쓰려면 CA 인증서 파일을 배포 환경에 함께 올려야 합니다(Render의
Secret File 기능 등). 파일을 올리기 번거로우면 `DB_USE_TLS=true`만 설정한 채 배포해도
동작은 합니다(인증서 검증 생략).

Render 무료 웹 서비스는 15분간 요청이 없으면 슬립 상태가 되고, 이후 첫 요청에서
컨테이너를 다시 띄우는 콜드스타트가 발생합니다(수십 초 지연). 무료 티어의 알려진
제약이니 참고하세요.

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
  (`getCachedOrFetchNews`, 5분 TTL). AI 브리핑의 뉴스 문단도 같은 카테고리/지역 조합별로
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

- 날씨 / 환율: `sync.WaitGroup` + 섹션별 독립 `context.WithTimeout(8초)` 로 병렬 처리.
  한 섹션이 실패하거나 타임아웃되어도 나머지 섹션은 정상적으로 응답합니다.
- 날씨(국내 도시): 기상청 API를 그 8초 타임아웃 중 최대 5초(`kmaSubTimeout`)만 쓰도록
  하위 컨텍스트로 감싸서 시도합니다 — 기상청 쪽이 느리거나 실패해도 나머지 3초 안에
  Open-Meteo로 폴백할 시간이 남도록 하기 위함입니다. 격자 좌표(nx/ny)는 위경도→LCC 변환
  함수(`latLonToGrid`)로 구현했지만, 실제로 쓰는 4개 도시는 기상청이 공개한 격자표 값을
  그대로 하드코딩했습니다(`domesticGrid`) — 도시 대표 위경도가 격자 경계에 걸리면 변환
  함수 결과가 1칸 어긋날 수 있어서입니다(대구가 실제로 그런 경우).
- 뉴스: NewsData.io `/api/1/latest`를 카테고리/지역 조합 하나당 한 번만 호출합니다.
  `getCachedOrFetchNews`의 30분 TTL DB 캐시(`raw_data_cache`)를 `GET /api/news` 핸들러와
  AI 브리핑의 내부 조회가 함께 사용하므로, 같은 조합을 브리핑과 뉴스 카드가 동시에
  요청해도 무료 티어 크레딧을 두 번 쓰지 않습니다. `region=international`일 때만 Groq로
  헤드라인 5개를 한 번의 JSON 모드 호출로 배치 번역하며, 번역 결과는 기사 id 기준으로
  별도 캐싱되어 같은 기사가 남아있는 동안 재번역하지 않습니다(`news_translation_cache`).
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
