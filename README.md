# 브리핑 관제실 — 실시간 멀티API 대시보드 + AI 브리핑 + 로또

Go 백엔드가 날씨(국내 도시는 기상청 API, 해외 도시 및 폴백은 Open-Meteo) / 환율(Frankfurter) / 뉴스(NewsData.io)를 오픈 API로 병렬 수집하고, 결과를 DB(Turso/libSQL)에 캐싱합니다. 이 데이터를 바탕으로 Groq LLM이 날씨·환율·뉴스 각 섹션별 한국어 브리핑을 생성하며, 원본 데이터가 바뀌지 않으면 이전 브리핑을 그대로 재사용해 불필요한 AI 호출을 줄입니다.<br>

로또 섹션은 초기 50회차 데이터를 정적 시드로 확보한 뒤, 공개 GitHub 데이터셋을 통해 회차를 수집해 DB(Turso/libSQL)에 저장합니다(원래는 동행복권 API를 직접 호출했지만, 여러 회차를 동시에 병렬로 긁어오다가 동행복권이 이 서버의 IP를 봇으로 판단해 차단해버린 뒤로 이 방식을 씁니다 — 동행복권 호출 코드는 삭제하지 않고 현재 쓰이지 않는 백업으로 남아있습니다). 자동 수집은 서버가 시작할 때 기본으로 켜지며, 밀린 회차가 있으면 전부 순차적으로 한 번에 채운 뒤 이후로는 매주 최신 회차만 확인합니다(GitHub 소스가 실패할 경우를 대비한 관리자 수동 입력 기능도 갖추고 있습니다).<br>
이 누적 데이터를 통계로 집계해 Groq AI API로 요약 인사이트를 제공하고, 가중 랜덤 샘플링 + 패턴 필터링으로 계산한 "이번 주 추천 번호"도 함께 보여줍니다.<br>

프론트엔드는 React + TypeScript(Vite)로 구성되어 있습니다.<br>

https://daily-news-o9mf.onrender.com/ 에서 확인해 보실 수 있습니다.

## 프로젝트 구조

```
backend/    Go, 표준 라이브러리 위주 (net/http, sync, context) + Turso/libSQL(database/sql, SQLite 호환) + 오픈API 4종(기상청, Open-Meteo, Frankfuter, NewsData.io) + Groq API + 로또 GitHub 데이터셋(백업: 동행복권 API, 현재 미사용 / 수동 대체: 관리자 API)
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
(`GROQ_MODEL`, 기본값 `openai/gpt-oss-20b`)을 쓰고, 그 출력이 콘텐츠
검증에 실패했을 때 딱 한 번만 더 정확한 모델(`GROQ_ESCALATION_MODEL`,
기본값 `openai/gpt-oss-120b`)로 재시도합니다 — 로또 AI 인사이트(주 1회만
생성)는 처음부터 이 큰 모델을 씁니다. 오늘 실제 호출 횟수(모델별)와 캐시
히트 수는 `GET /api/debug/groq-usage`에서 확인할 수 있습니다.

**(2026-08) 모델 교체:** 원래 기본값이던 `llama-3.1-8b-instant`/
`llama-3.3-70b-versatile`을 Groq가 완전히 지원 종료해("모델이 존재하지
않거나 접근 권한이 없다"는 에러로 브리핑/번역/로또 인사이트가 전부
실패하기 시작해) 위 두 모델로 교체했습니다. 무료 티어 한도는 Groq 콘솔
문서([console.groq.com/docs/rate-limits](https://console.groq.com/docs/rate-limits))
기준 **두 모델이 완전히 동일**합니다 — 30 RPM · 1,000 RPD(하루 요청 수) ·
8,000 TPM(분당 토큰) · 200,000 TPD(하루 토큰). 예전에는 8B 모델을
"70B 모델(하루 1,000회)보다 훨씬 여유로운 하루 14,400회 쿼터"라는 이유로
호출량이 많은 곳의 기본값으로 골랐는데, 이제는 그 쿼터 차이 자체가
사라졌습니다 — 지금은 순수하게 속도/비용 때문에 소형 모델을 기본값으로
씁니다. 즉 브리핑/번역에 쓰는 기본 모델의 하루 요청 한도가 14,400회에서
1,000회로 크게 줄었다는 뜻이므로(`GET /api/debug/groq-usage`로 확인
가능), Render 등에 배포된 환경에 `GROQ_MODEL`/`GROQ_ESCALATION_MODEL`을
과거 모델명으로 직접 지정해둔 환경변수가 있다면 반드시 함께 갱신하거나
제거해야 합니다(제거하면 위 새 기본값이 그대로 적용됩니다).

**`reasoning_effort` — 이 교체 과정에서 발견한, 반드시 필요했던 추가
조치:** `openai/gpt-oss-20b`/`openai/gpt-oss-120b`는 둘 다 **추론
모델**이라, 최종 답변을 쓰기 전에 숨겨진 "추론" 과정을 거치고 이 추론도
`max_tokens`/`completion_tokens` 예산을 함께 소비합니다. 모델을 그냥
교체만 하고 이 사실을 몰랐다면, 브리핑 3섹션(각 `maxTokens=300`)이 실제
운영 환경에서 **100% 재현되는 완전한 장애**를 일으켰을 것입니다 — 직접
Groq API를 호출해 확인한 결과, `reasoning_effort`를 지정하지 않으면
(기본값 `medium`) 이 앱의 날씨 브리핑 프롬프트 하나만으로도 매번
`max_tokens`(300) 전부가 추론에 소진되고(`reasoning_tokens=298/300`)
최종 답변(`content`)이 완전히 빈 문자열로, `finish_reason=length`로
돌아왔습니다(`generateSectionText`가 이를 "briefing response was empty"
로 처리해 매번 `stale_fallback`으로 떨어짐). `callGroqChat`이 모든 요청에
`reasoning_effort: "low"`를 고정으로 실어 보내도록 고쳐서 해결했습니다 —
같은 프롬프트로 재검증한 결과 추론 토큰이 10~130 수준으로 줄어 답변이
정상적으로 돌아옵니다. 다만 "low"는 상한이 아니라 느슨한 가이드일 뿐이라
완전히 없앨 수는 없고(뉴스 번역 배치에서는 추론 토큰이 16~410까지
관측됨), 그래서 위 문단처럼 각 호출부의 `maxTokens`에도 넉넉한 여유를
남겨뒀습니다. 두 모델 모두 `reasoning_effort`의 `low`/`medium`/`high`를
지원하므로(Groq 문서) 호출부를 가리지 않고 `callGroqChat` 한 곳에서만
이 값을 고정한다 — 향후 이 계열이 아닌 비추론 모델로 다시 바꾸는 경우,
이 필드가 무시되는지(대개 알 수 없는 필드는 무시되지만) 확인이 필요합니다.

TPM(분당 토큰) rate limit로 Groq 호출이 실패하는 것과는 별도로,
`callGroqChat`(`backend/groq.go`)이 이를 위한 전용 재시도를 갖고
있습니다. Groq의 429 에러 메시지에는 "Please try again in {N}s"처럼
정확히 몇 초 뒤에 재시도하면 되는지가 실려 오는데,
`parseGroqRetryAfterSeconds`가 정규식으로 이 대기 시간을 파싱해서, 그
시간(+0.5초 여유)만큼만 기다린 뒤 **같은 모델·같은 요청**으로 다시
시도합니다. 위 문단의 "검증 실패 시 모델 승격" 재시도와는 완전히
별개의 로직입니다 — 이 쪽은 모델을 바꾸지 않고 같은 모델로 아주
짧게 기다렸다 재시도할 뿐이라 서로 간섭하지 않으며, 브리핑
3섹션·뉴스 번역·로또 인사이트가 전부 거쳐 가는 `callGroqChat` 한
곳에만 있어서 호출부를 가리지 않고 모두 적용됩니다.

재시도는 최대 `maxGroqRateLimitRetries`(기본 3회, 최초 시도 포함
총 최대 4회)까지 반복하며, **재시도할 때마다 그 시점의 최신 에러
메시지에서 대기 시간을 다시 파싱**합니다 — 첫 실패의 대기 시간을
그대로 재사용하지 않습니다. 여러 Groq 호출이 같은 TPM 예산을 두고
경쟁하는 상황에서는 재시도 시점에 다른 호출이 그사이 더 많은 토큰을
써버려 안내되는 대기 시간 자체가 매번 달라질 수 있어서(예: 1차
실패는 1.2초, 2차 실패는 4.5초), 최초 값을 고정해서 재사용하면
아직 부족한 시간만큼만 기다리고 다시 실패하는 헛수고가 되기
쉽습니다. 다만 다음 두 조건 중 하나라도 걸리면 그 시점에서 즉시
재시도를 멈추고 원래 에러를 반환합니다 — 사용자를 무리하게 기다리게
하지 않기 위해서입니다. 이 반환값을 받는 `resolveBriefingSection`은
직전에 성공한 캐시가 남아 있으면 그걸 재사용해 `stale_fallback`으로,
캐시조차 없으면 `failed`로 응답합니다(아래 "브리핑 3섹션" 문단 참고).

- 한 번의 대기가 `maxGroqRateLimitRetryWait`(10초)를 넘거나, 에러
  메시지 형식이 달라 대기 시간을 파싱할 수 없는 경우
- 다음 재시도의 대기 시간을 더하면 누적 대기가
  `maxGroqRateLimitTotalWait`(20초)를 넘을 것으로 예상되는 경우 —
  개별 대기는 매번 10초 이하라도 여러 번 이어지면 총합이 사용자
  체감상 너무 길어질 수 있어 별도의 전체 예산을 둡니다.
- **ctx에 남은 예산 자체가 부족한 경우** — 위 두 조건은 `callGroqChat`
  자신의 재시도 예산만 볼 뿐, 호출부가 넘겨준 `ctx`(예:
  `briefingGenerationTimeout`)에 남은 시간은 보지 않았습니다. 실제
  보고된 사고: rate limit 대기 시간이 8.18초였는데 당시 섹션 예산이
  8초라, 무조건 기다리는 이 로직이 대기 도중 ctx를 만료시켜 재시도
  자체를 시도해보지도 못한 채 "context deadline exceeded"로
  실패했습니다(이러면 상위 로그에도 진짜 원인인 rate limit이 아니라
  타임아웃만 남아 원인 추적이 어려워집니다). 이제 대기를 시작하기
  전에 `ctx.Deadline()`으로 남은 시간을 확인해, **"대기 시간 + 예상
  호출 시간(`groqRateLimitRetryCallOverhead`, 2초)"이 남은 예산 이상**
  이거나 **대기 시간 자체가 남은 예산의 `groqRateLimitRetryBudgetRatio`
  (80%) 이상**이면 그 시점에서 즉시(기다리지 않고) 원래 rate limit
  에러를 반환합니다 — 기다렸다 실패하는 것보다 즉시 폴백 처리로
  넘어가는(캐시가 있으면 `stale_fallback`, 없으면 `failed`) 편이
  응답 속도에 낫고, 에러도 `ctx.Err()`가 아닌 원래 사유 그대로 남아
  로그에서 바로 rate limit 때문이었음을 알 수 있습니다. `ctx`에
  데드라인이 없으면(테스트가 `context.Background()`를 쓰는 경우 등)
  이 검사는 건너뜁니다.

브리핑 3섹션(weather/exchange/news)은 `getBriefing`이 goroutine으로
병렬 생성하고, 뉴스 헤드라인 번역도 별도의 `/api/news` 요청 경로로
거의 동시에 Groq를 호출할 수 있어서(최악의 경우 최대 4개), 이 호출들이
전부 같은 순간에 나가면 서로 TPM 예산을 두고 경쟁해 하나가 rate
limit에 걸리면 나머지도 함께 걸리기 쉬워집니다. `callGroqChat`은
실제 HTTP 전송 직전에 `acquireGroqCallSlot`으로 전역 게이트
(`groqCallGate`)를 거칩니다 — 동시 실행 개수를 `maxConcurrentGroqCalls`
(기본 2개)로 제한하는 세마포어와, 세마포어 슬롯을 얻더라도 호출
시작 시각 자체를 최소 `groqCallStagger`(기본 250ms)만큼 벌리는
스태거링을 함께 적용합니다. 완전히 순차(1개씩)로 만들면 브리핑
3섹션의 체감 응답 시간이 3배로 늘어나지만, 2개까지만 허용해도
"4개가 한 순간에 몰리는" 최악의 경우는 피하기에 충분하고, 250ms
스태거는 사용자가 체감하기 어려운 수준이면서도 같은 분(minute)
버킷 안에서 순간적으로 몰리는 토큰 소비의 피크를 낮춰줍니다.
재시도 대기까지 포함해 이 게이트의 슬롯을 함수 전체 동안 쥐고
있는데, 이미 rate limit에 걸려 백오프 중인 호출이 슬롯을 놓아줄
때까지 다른 호출을 기다리게 하는 편이, 함께 더 많은 호출을 새로
쏴서 상황을 악화시키는 것보다 낫기 때문입니다.

뉴스 헤드라인 번역(`fetchNewsTranslation`, `backend/news_translation.go`)의
`maxTokens`는 700 → 500(첫 조정 당시) → **1200(2026-08, 모델 교체 후)**으로
바뀌었습니다. `estimateTokenCount`(`groq.go`)로 다소 긴 편에 속하는 실제
뉴스 제목 스타일 번역문 5개(각 35~40자) + NewsData.io 스타일
article_id(32자)로 구성한 배치 출력 JSON의 순수 콘텐츠 분량 자체는 예전
측정 그대로 약 330토큰입니다. 700 → 500 조정 당시에는(기본 모델이
`llama-3.1-8b-instant`였을 때) `max_tokens`가 순수하게 "생성 가능한 상한"일
뿐이라 정상적으로 성공하는 번역이라면 실제 사용 토큰이 700이든 500이든
거의 동일했습니다.

**(2026-08) 기본 모델을 `openai/gpt-oss-20b`로 바꾼 뒤에는 이 전제가
깨졌습니다.** 이 모델은 추론 모델이라 `completion_tokens` 예산을 최종
답변보다 먼저 숨겨진 "추론"에 쓰는데(아래 "Groq API 키 발급" 문단의
`reasoning_effort` 설명 참고), 실제 배치 5개 호출을 반복 관찰한 결과
추론에만 쓴 토큰 수가 16~410으로 요청마다 크게 들쭉날쭉했습니다 —
500에서는 실제로 완료 480/500(96%)까지 소진해 위험할 만큼 여유가
없었고, 800으로 올린 뒤에도 한 번은 670/800(84%)까지 소진되는 것이
실측됐습니다. `reasoning_effort="low"`는 상한이 아니라 느슨한 가이드일
뿐이라 이런 변동을 프롬프트 조정만으로 없앨 수 없다고 보고, 1200으로
한 번 더 올려 관측된 최악값(670) 대비 약 79% 여유를 확보했습니다. 이
조정의 효과는 여전히 평소 소비량을 늘리는 게 아니라(정상적으로 성공하는
번역이라면 상한과 무관하게 소비량이 거의 동일합니다), 추론이 예상보다
길어져 콘텐츠가 잘려 응답이 통째로 비거나(`generateSectionText`의
"response was empty"와 같은 실패 유형) JSON 파싱이 깨지는 사고를 막는
데 있습니다.

## AI 브리핑 콘텐츠 검증 (환각·반말·금칙어·반복 방지)

Groq가 생성한 문장을 그대로 내보내지 않고, `validateSectionOutput`(`backend/briefing.go`)이
고정된 순서로 여러 검사를 거칩니다. 검사는 두 등급으로 나뉩니다 — **hardFailure**는 검증에
실패하면 에스컬레이션 모델(`GROQ_ESCALATION_MODEL`)로 한 번만 재시도하고, 그래도 실패하면
`stale_fallback`(남아있는 캐시나 안내 문구로 대체)으로 처리합니다. **softFailure**는 재시도해도
계속 검출되면 결과를 그대로 내보냅니다 — 현재는 금칙어 검사만 이 등급입니다.

이 콘텐츠 검증은 생성된 텍스트를 사후에 검사하는 것이고, 그 이전에 `resolveBriefingSection`의
`hasData` 가드가 애초에 원본 데이터(`WeatherData`/`ExchangeData`/`NewsData`)가 있을 때만 Groq를
호출하도록 세 섹션 모두에 동일하게 적용되어 있습니다(날씨는 `weather != nil`, 환율은
`exchange != nil`, 뉴스는 `news != nil && len(news.Items) > 0`). 실제 보고된 사례: NewsData.io
조회가 타임아웃으로 실패해 `news`가 `nil`인 채로 `getBriefing`에 전달됐는데 이 가드가 없어서,
`nil`이 JSON으로 직렬화된 `"[뉴스 데이터]: null"`이라는 의미 없는 프롬프트가 그대로 Groq에
전달되고 `groundingText`가 비어 있어 환각 검사기들마저 스스로 건너뛰는 최악의 조합으로 의미
없는 응답이 나왔습니다. 데이터가 없으면 Groq를 아예 호출하지 않고, 되돌아갈 캐시가 있으면
`stale_fallback`으로, 없으면 섹션별 안내 문구("⚠️ 뉴스 데이터를 가져오지 못해 브리핑을 생성할
수 없습니다" 등)로 즉시 처리합니다.

뉴스 섹션은 에스컬레이션 재시도까지 실패했을 때 한 단계를 더 거칩니다: 곧바로
`stale_fallback`으로 넘어가는 대신, `generateNewsSectionText`가 `pickNewsItemToExclude`로
(생성문에 남은 숫자와 후보 헤드라인들의 숫자를 대조해) 문제로 의심되는 헤드라인 하나를
추정해 제외하고, 남은 헤드라인만으로 저렴한 기본 모델부터 한 번 더 생성을 시도합니다. 판별이
안 되면(숫자가 전혀 겹치지 않으면) 프롬프트가 우선순위를 낮게 매긴 마지막 항목을 기본값으로
제외합니다. 이 재시도마저 실패하면 그때 기존처럼 `stale_fallback`으로 넘어갑니다 — 특정
헤드라인 하나가 계속 이상한 결과를 유발할 때, 매번 오래된 캐시로 대체하는 대신 나머지
헤드라인으로라도 새 브리핑을 만들어내기 위한 장치입니다. 이 마지막 재시도가 실패했을 때
`retryErr`(이번 시도의 실제 실패 사유) 대신 최초 실패(`err`, 예: "한자/CJK 문자 감지")를 그대로
반환하던 버그가 있었습니다 — 그러면 이 재시도가 실제로는 API 오류든 또 다른 검증 실패든, 로그와
최종 실패 사유에는 항상 이전 시도의 낡은 사유만 남아 진짜 원인을 알 수 없었습니다. 이제 이번
시도의 결과(`retryText`/`retryErr`)를 반환하고, 로그에 두 실패 사유를 함께 남깁니다.

`useFallback`이 켜지는 검증(주제 불일치·조작된 퍼센트·근거 없는 고유명사)이 재시도 후에도
실패하면 `generateSectionText`는 `hallucinationFallback`(`newsHallucinationFallback`, "가장
인기 있는 뉴스: {원문 제목}" 형태)을 **`err == nil`로** 반환합니다 — LLM이 생성한 문장을 아예
쓰지 않고 원문 제목 그대로를 쓰므로 hallucination 여지 자체가 없다는 의도였지만, 이 `err ==
nil`이 `resolveBriefingSection`에는 "정상 생성 성공"으로 보여서 다른 진짜 성공 결과와 구분
없이 그대로 `briefing_section_cache`에 캐싱되는 버그가 있었습니다. `stale_fallback`(생성이
실패해 *이전* 캐시를 대체 값으로 재사용 — 캐시에 새로 쓰지 않음)과 이 hallucination fallback
(이번 생성이 안전 문구로 대체됐지만 `err`가 없어 *새 캐시 행으로 그대로 저장*됨)은 서로 다른
개념인데, 코드는 후자를 전자와 똑같이 취급하지 않고 오히려 캐시를 오염시켰던 셈입니다. 실제로
`news:international:science` 캐시 행에 "가장 인기 있는 뉴스: A 3.6-ton mirror three inches
thick sits on a Maui volcano..."라는 영어 원문이 그대로 고정되어, 같은 헤드라인 집합이
남아있는 동안(뉴스 원본 캐시 TTL 30분) 정상 생성을 다시 시도할 기회조차 주어지지 않는 것을
실제 캐시 데이터로 확인했습니다. `generateSectionText`/`generateNewsSectionText`가 `isFallback
bool`을 추가로 반환하도록 고치고, `briefing_section_cache`에 `is_fallback` 컬럼을 추가해
캐싱 시점에 이 값을 함께 저장합니다. `resolveBriefingSection`은 `data_hash`가 일치해도
`is_fallback`이 true인 행은 "재사용 가능한 캐시"로 보지 않고 매번 재생성을 다시 시도합니다 —
재생성이 다시 실패하면 기존 `stale_fallback` 경로가 그대로 이 캐시 행(과 그 안의 폴백
텍스트)을 대체 값으로 서빙하므로, 별도의 TTL을 두지 않고도 사실상 매 요청이 복구 기회가
됩니다. 이 컬럼이 없던 기존 배포에는 `ensureColumnExists`(`db.go`)가 `ALTER TABLE ADD
COLUMN`으로 추가하며, 기존 행은 `DEFAULT 0`(정상 결과 취급)으로 채워지므로 이미 고정되어 있던
행은 자동으로 풀리지 않습니다 — 실제로 `news:international:science` 행은 배포 후 수동으로
`is_fallback = 1`을 백필해 다음 요청부터 재생성이 시도되도록 했습니다.

CJK(한자·중국어·일본어) 금지 규칙은 domestic/international이 완전히 같은
`newsSectionSystemPrompt`(따라서 같은 `briefingCommonRules`)를 공유하므로 애초에 두 경로 모두에
적용되어 있지만, 재발 사례가 있어 규칙 목록 맨 앞(최우선)으로 옮기고 문구를 강화했습니다.
`annotateNumericUnits`가 원문(NewsData.io 원본이 아니라 우리 코드)에 "revenue of 6010만 달러
misses"처럼 영어 문장과 이미 계산된 한글 숫자 표기를 의도적으로 섞어 넣어두는데("사전 변환"
방식 — 위 통화 단위 단락 참고), 이 혼종 입력이 모델의 언어 경계 판단을 흐트러뜨려 한자/일본어
유출을 유발할 수 있다고 보고, "입력이 섞여 있어도 숫자 값은 유지한 채 문장 전체를 순수 한국어로
재구성하라"는 지침을 명시적으로 추가했습니다. `foreignScriptPattern`(`briefing.go`)은 한자 범위뿐
아니라 히라가나(U+3040–309F)·가타카나(U+30A0–30FF) 범위도 처음부터 포함하고 있어서,
"Mesa Laboratories"를 "메사 랩터러リーズ"처럼 일본어 음차가 섞인 표기로 잘못 옮기는 경우도 이
검사에 걸립니다 — 다만 검증만으로는 같은 실패가 재시도마다 반복될 수 있어서,
`newsSectionSystemPrompt`에 "영어 고유명사는 외래어 표기법에 맞는 한글이나 영어 원문 그대로만
쓰고, 일본어·중국어식 음차는 금지"라는 규칙을 추가해 애초에 생성 단계에서 막습니다.

위 "일본어 음차" 대응은 고유명사(회사명 등)를 소리 나는 대로 옮기려다 가나/한자가 섞이는
경우를 막기 위한 것이었는데, 이와는 다른 새 유형의 재발 사례가 있었습니다: "belly size beats
BMI at predicting heart attacks" 헤드라인을 다루다가 "배圍"(한글 "배" + 한자 "圍"가 뒤섞인,
어느 언어에도 존재하지 않는 표현)가 생성됐습니다. 이번엔 고유명사가 아니라 "배 둘레"의 한자어
표현인 腹圍(복위)처럼, 흔히 한자로도 표기되는 일반/전문 용어(의학·과학·법률 등)를 모델이
무리하게 정확히 옮기려다 생긴 실패였습니다. `findForeignScript`가 사후에 이미 이 출력을
걸러냈지만, 검증만으로는 재시도 후에도 같은 헤드라인이 다시 선택되면 같은 실패가 반복될 수
있어(`pickNewsItemToExclude`가 매번 정확히 이 헤드라인을 제외 대상으로 골라준다는 보장이
없습니다), `newsSectionSystemPrompt`에 규칙 5번("의학·과학·법률 등 전문 용어도 한자를 섞지
말고 한글로만 쓰세요... 한글로 옮기기 애매하면 억지로 옮기지 말고 쉬운 말로 풀어 쓰세요")을
추가했습니다. 헤드라인 번역(`news_translation.go`의 `newsTranslationSystemPrompt`)도 영어
원문을 한국어로 옮기는 별개의 Groq 호출이라 똑같은 실패가 날 수 있어, 두 프롬프트는 상수를
공유하지 않지만 같은 문구를 양쪽에 동일하게 반영했습니다 — `TestNewsSectionSystemPromptCoversTechnicalTermHanjaMixing`/
`TestNewsTranslationSystemPromptCoversTechnicalTermHanjaMixing`(`briefing_section_test.go`/
`news_translation_test.go`)이 두 프롬프트 모두에 이 규칙이 실제로 존재하는지 회귀 테스트로
고정합니다. 이 규칙 추가로 `newsSectionSystemPrompt`가 늘어나 프롬프트 토큰 예산 테스트
(`TestNewsBriefingPromptFitsWithinTokenBudget`)가 기존 예산(1500)을 넘어섰는데, 이미 문구를
최대한 압축한 상태였고 늘어난 뒤에도 실제 6,000 TPM 한도까지는 여전히 여유가 있어
`briefingNewsPromptTokenBudget`을 1650으로 올렸습니다 — 실제 Groq API로 5회 반복 생성해
매번 "배 둘레가 BMI보다 심장 질환 예측에 더 정확합니다"류의 순수 한글 문장이 나오는 것을
확인했습니다.

한자·가나만 잡던 이 검사는 이후 다른 문자 체계로도 재발했습니다: 인도 도시 "Ahmedabad"를
"아마다바드"로 표기하려다 힌디어 데바나가리 문자(अहमदाबाद)가 그대로 노출됐습니다. 국제
뉴스가 인도·중동·동남아·러시아 등 다양한 지역을 다루는 만큼, `foreignScriptPattern`(원래
이름 `foreignCJKPattern`)이 매칭하는 범위를 한자(U+4E00–9FFF 등)·가나(U+3040–30FF)에서
데바나가리(U+0900–097F, 힌디어)·벵골 문자(U+0980–09FF)·아랍 문자(U+0600–06FF)·히브리
문자(U+0590–05FF)·태국 문자(U+0E00–0E7F)·키릴 문자(U+0400–04FF)·그리스 문자
(U+0370–03FF)까지 넓혔습니다 — 함수/변수 이름도 검사 범위가 CJK를 넘어선 것을 반영해
`findForeignScript`로 바꿨습니다. 로마자(영어 고유명사를 원문 그대로 쓰는 것)는 이 검사와
무관합니다 — 로마자 잔존 여부는 `findLeakedEnglish`가 별도로 담당하므로 "Ahmedabad"를
영어 그대로 쓰는 것은 걸리지 않고, 그 지역 고유 문자로 옮겨 쓰려다 실패한 경우만 잡힙니다.
`newsSectionSystemPrompt`(규칙 7번)와 `newsTranslationSystemPrompt`(규칙 0번)에도 "인도·
중동·동남아·러시아 등 비영어권 지명·인명에 그 지역 고유 문자를 섞지 말고 한글 또는 영어
원문만 쓰라"는 지침을 구체적인 예시(Ahmedabad/अहमदाबाद)와 함께 추가했습니다 — 두 프롬프트가
상수를 공유하지 않으므로 위 한자 혼입 사례와 마찬가지로 양쪽에 동일하게 반영했고,
`TestNewsSectionSystemPromptCoversNonHangulScriptPlaceNames`/
`TestNewsTranslationSystemPromptCoversNonHangulScriptPlaceNames`가 이를 회귀 테스트로
고정합니다. 규칙 추가로 뉴스 프롬프트 토큰 수가 다시 늘어(실측 2,113토큰)
`briefingNewsPromptTokenBudget`을 1950에서 2150으로 올렸습니다 — 6,000 TPM 한도까지는
여전히 3,800토큰 이상의 여유가 있습니다.

- **존댓말 강제** (hardFailure): 날씨/환율/뉴스 공통 프롬프트 규칙("항상 합니다체로 작성하세요")에
  더해, 뉴스 문단은 원문(NewsData.io)이 기사체(~했다)라도 반드시 합니다체로 재작성하라는 전용
  규칙이 추가돼 있습니다. `findInformalSentenceEnding`이 반말/기사체 종결을 정규식으로
  탐지하는데, 경계는 "문장부호(.!?) 다음" 또는 "문자열 끝"으로만 인정합니다 — 예전에는 그냥
  공백만 뒤따라도 경계로 봤는데, 그러면 "바다"(sea)처럼 "다"로 끝나는 평범한 명사가 문장
  중간에 있을 때도 문장 종결로 오인해 실제로는 정상 합쇼체인 문장("…위협받고 있습니다")까지
  반말로 오탐하는 사례가 있었습니다.
- **금칙어 필터** (softFailure): 인터넷 은어(ㅋㅋ, 대박, 헐 등)를 쓰지 말라는 프롬프트 지시와
  `findBannedPhrase` 후처리 검사가 있습니다. 네 가지 검사 중 유일하게, 재시도까지 소진한 뒤에도
  검출되면 결과를 그대로 내보냅니다.
- **환각 방지** (hardFailure, 5종): `findUngroundedNumber`(원본에 없는 숫자),
  `findFabricatedPercentage`(% 기호 조작), `findUngroundedProperNoun`(근거 없는 고유명사),
  `findTopicMismatch`(토큰 중복도 기반 주제 이탈), `findLeakedEnglish`/`findForeignScript`(번역 누락·
  외국어 잔존)이 각각 실제로 관측됐던 환각 사례를 회귀 테스트로 고정해두고 있습니다. 우산 필요
  여부(날씨)·상승/하락 판단(환율)처럼 숫자 해석이 필요한 판단은 애초에 LLM에 맡기지 않고 Go가
  미리 계산해(`computeUmbrellaAdvice`, `computeExchangeTrend`) 프롬프트에 답으로 제공합니다 —
  판단 자체를 LLM에서 걷어내 환각 여지를 구조적으로 없앤 것입니다. 뉴스 title/description을
  프롬프트 토큰 예산에 맞춰 자를 때도 `truncateForPrompt`가 단어 중간이 아니라 마지막 공백에서
  자르고 말줄임표(…)를 붙입니다 — 단순 하드컷으로 문장이 중간에서 뚝 끊기면, 모델이 그 조각을
  완결된 문장처럼 취급해 억지로 문법을 짜맞추려다 서로 무관한 사실을 하나로 뒤섞어 붙이는
  사례(예: "…총으로 쏘려고 시도하여 17년에서 무기징역을" 같은 비문)가 있었습니다. 다만 이
  "마지막 공백까지 되돌아가기"는 원래 하드컷 지점이 실제로 단어 중간이었을 때만 필요한
  동작인데, 예전에는 이 조건 없이 항상 되돌아갔습니다 — 실제 보고된 사례: NewsData.io
  description "...a record $540.2 million grant..."가
  `briefingNewsDescriptionMaxRunes`(80)에서 공교롭게도 "million" 바로 뒤에서 깔끔하게
  잘렸는데도, 이미 온전했던 "million"이라는 단어 전체를 불필요하게 잘라내 "...a record
  $540.2…"만 남겼습니다. 그러면 `annotateNumericUnits`가 매칭할 단위(million)가 사라져
  "$540.2"가 변환되지 않은 채 그대로 프롬프트에 남았고, 모델이 단위 없는 이 숫자를 스스로
  어림잡다 "5억"(정답 5.4억과 약 7.4% 차이)을 만들어내 `findUngroundedNumber`에 근거 없는
  숫자로 걸렸습니다("근거 없는 숫자 감지(5e+08)") — 검증기나 숫자 변환 계산 자체의 버그가
  아니라, 하드컷 지점이 이미 단어 경계였는지 확인하지 않고 무조건 되돌아가던 것이 진짜
  원인이었습니다. 이제 잘린 지점 바로 다음 글자가 공백일 때는(이미 완전한 단어에서 끝났을
  때는) 되돌아가지 않습니다. 그런데 이 수정 뒤에도 실제 Groq 응답을 반복 관찰해보니, 모델이
  정확히 변환된 "5.4억 달러"를 그대로 쓰지 않고 "5억 400만 달러"(정답은 "5억 4000만"이어야
  함)처럼 억/만 단위로 다시 쪼개 표현하려다 자릿수를 틀려 같은 오류가 또 발생할 수 있었습니다
  — `newsSectionSystemPrompt`에 "K/M/B는 이미 한국어로 환산되어 있으니 그 표기 그대로 쓰세요
  — 억/만으로 다시 쪼개지 마세요"라는 지침을 추가해, 애초에 모델이 이미 정확한 값을 다시
  계산하려 들지 않도록 했습니다. 실제 Groq API로 8회 반복 생성해 7회가 매번 정확히 "5.4억
  달러"로 성공하는 것을 확인했습니다(예전에는 재시도·모델 승격·헤드라인 제외까지 전부
  실패해 `stale_fallback`으로 넘어갔습니다).
  `findUngroundedNumber`는 `sportsRoundExceptions`에 등록된 스포츠 대회 라운드 용어("quarters"/
  "quarterfinal"→8강, "semifinal"/"semis"→4강, "round of 16"→16강, "round of 32"→32강)만
  예외로 허용합니다 — 영어 원문은 라운드를 숫자 없이 표현하는데 한국어는 관용적으로 숫자를 붙이는
  차이 때문에, "reach Montreal quarters"를 정확히 "몬트리올 8강에 진출"로 옮긴 결과까지 근거 없는
  숫자로 오탐해 재시도만 반복하다 실패 처리되던 사례가 있었습니다. 이 예외는 숫자가 일치할 뿐만
  아니라 groundingText(원문)에 해당 영어 용어가 실제로 등장할 때만 적용되므로, 목록에 없는 진짜
  지어낸 숫자(예: 원문에 없는 "3연승", "5번째 우승")는 여전히 걸러집니다.
  통화 단위("£25bn", "$6.6bn", "25 million", "£16m")도 근본 원인은 검증이 아니라 사전 변환
  쪽이었습니다 — `annotateNumericUnits`(`news_number_annotate.go`)의 `numericUnitPattern`이
  원래 대문자 K/M/B와 `$` 기호만 인식해서, `£25bn`처럼 통화 기호가 £/€이거나 단위가 소문자·철자
  표기("bn", "million")인 경우는 원문 그대로 모델에 전달됐고 모델이 직접 계산하다 "2500억"(10배
  과다)으로 틀렸습니다. `bn`은 예외 처리했지만 소문자 단일 글자 `m`("£16m")을 빠뜨려 같은 오탐이
  재발한 적도 있습니다 — 이제 `parseNumericUnitMatch` 함수 하나로 k/m/b(및 철자·bn/mn 표기)를
  전부 관리해서, 새 단위 하나를 한쪽에만 추가하고 빠뜨리는 재발 자체가 구조적으로 어렵습니다.
  통화 기호(`$/£/€`)가 있으면 소문자 단일 글자도 안전하게 금액으로 인정하고(예: `£16m` → "1600만
  파운드"), 통화 기호 없이 소문자 단일 글자만 있으면("100m") 100미터 같은 다른 단위와 헷갈릴
  여지가 있어 여전히 변환하지 않습니다. `annotateNumericUnits`(사전 변환)와
  `findUngroundedNumber`가 쓰는 `extractEnglishUnitNumbers`(사후 검증, 이중 방어선)가 이 함수
  하나를 함께 참조하므로, 두 곳의 판단이 어긋나 정상 변환을 오탐하는 문제가 재발하기 어렵습니다.
  물론 "2500억"처럼 잘못 계산된 값이나 원문과 무관한 금액은 여전히 걸러집니다.
  이 사가에는 한 화가 더 있습니다: 위 "$540.2 million" 사례는 하드컷 지점이 이미 "million" 바로
  뒤(단어 전체가 끝난 지점)였던 경우였는데, 실제로는 하드컷 지점이 숫자와 단위 사이의 *공백*에
  걸리는 경우도 있었습니다 — 예: "...announces $100 million..."이
  `briefingNewsTitleMaxRunes`에서 "$100"과 " million" 사이 공백에서 잘리면, 그 지점은 여전히
  "이미 완전한 단어 경계"로 보여 위 수정된 로직도 손대지 않고 그대로 "$100…"만 남겨 "million"
  전체가 사라졌습니다. 게다가 이때는 title 자체가 잘린 것이었는데(옛 `briefingNewsTitleMaxRunes`
  80은 description과 똑같았습니다), 모델이 재시도(8B → 70B 승격)마다 서로 다른 크기로 추측해
  "1억"과 "10억" 사이를 오가며 두 시도 모두 검증에 실패했습니다. `extendCutToPreserveNumericToken`
  (`news_number_annotate.go`)이 이 경우를 구조적으로 막습니다 — 하드컷 지점이
  `numericUnitPattern`(단위 축약형이 있는 숫자) 또는 `bareCurrencyAmountPattern`(단위 없이 통화
  기호만 있는 금액, 예: "$1,204,000") 중 하나의 매치 중간을 가로지르면, 그 표현 전체가 포함되도록
  잘리는 위치를 매치 끝까지 뒤로 늘립니다 — 완전히 안 자르는 것보다 숫자 표현 하나만큼만 더
  자르는 편이 토큰 비용 대비 효율적입니다. 별개로 title 상한도 description(80자)과 똑같이
  취급되던 것을 120으로 올렸습니다 — title은 description과 달리 정보 일부 소실을 감수하는
  설계가 아니라 그 자체로 기사 핵심 사실을 담고 있어서, 실측 최장 헤드라인(약 118자) 수준까지는
  사실상 전혀 잘리지 않아야 합니다(240처럼 더 크게 잡으면
  `TestNewsBriefingPromptFitsWithinTokenBudget`의 최악 시나리오 기준 예산 여유가 지나치게
  줄어듭니다 — 그래서 이 테스트가 쓰는 토큰 예산도 1650→1950으로 함께 올렸습니다). 그래도
  description은 여전히 80자에서 잘릴 수 있으므로(문장이 통째로 잘려 세부 정보가 소실되는 것
  자체는 막을 수 없습니다), `newsSectionSystemPrompt`에 "title/description이 말줄임표(…)로
  끝나 불완전하면 그 안의 숫자·세부 정보를 추측해서 채우지 말라"는 규칙을 추가해, 검증기가 못
  잡는 유형(모델이 우연히 그럴듯한 값을 만들어 `findUngroundedNumber`를 통과해버리는 경우)까지
  생성 단계에서부터 예방합니다. 마지막으로, "재시도마다 검증 실패 사유는 같은데 감지된 숫자
  자체가 다름"이라는 패턴(`extractUngroundedNumberFromReason`으로 `generateSectionText`의 재시도
  루프 안에서 비교)이 나타나면 "검증기나 프롬프트 문제가 아니라 원문 입력 자체가 잘려서
  불완전할 가능성이 있다"는 경고를 로그에 남겨, 다음에 비슷한 사고가 나면 두 번째 원인(잘못된
  검증 로직)부터 의심하며 헤매지 않고 원문 truncate 지점부터 바로 확인할 수 있게 했습니다.
  `findUngroundedProperNoun`은 두 종류의 실패를 구분합니다: `newsContractCounterpartyPattern`이
  잡아내는 "계약 상대방 날조"(예: 두산에너빌리티가 실제로는 존재하지 않는 "노블리스 오일 앤
  가스"와 계약했다고 지어낸 사례)는 절대 완화되지 않고 항상 hard failure로 남습니다. 반면 그 외의
  일반 고유명사 실패는, 재시도까지 실패해도 원문의 실제 핵심 개체(`hasGroundedCoreProperNoun`이
  확인)가 응답에 그대로 남아있으면 안전 문구로 대체하지 않고 생성된 문장을 그대로 사용합니다 —
  원문에 등장한 "Panthers"가 NFL 소속이라는 것은 상식적인 보충 설명이지 지어낸 사실이 아닌데도,
  원문에 literal한 "NFL"이 없다는 이유만으로 hallucination 취급돼 재시도만 반복하다 폐기된 사례가
  있었습니다. 완벽한 판별은 아니지만(원문 개체 하나가 살아있다고 문장의 다른 내용까지 사실이라는
  보장은 없습니다), 화이트리스트를 관리하는 비용보다 저렴하면서도 계약 상대방 날조처럼 실제로
  위험한 유형은 별도 패턴으로 여전히 걸러냅니다.
  `findTopicMismatch`는 원래 후보 헤드라인 전체를 하나로 합친 텍스트를 분모로 토큰 중복도를
  계산했는데, 헤드라인이 여러 개이고 모델이 그중 하나만 정확히 요약하는 것은(프롬프트가 여러
  후보 중 가장 다루기 좋은 것을 고르라고 지시하므로) 정상적인 동작인데도, 나머지 헤드라인의
  토큰이 분모에만 더해져 비율이 항목 수만큼 옅어져 정상 사례까지 오탐하는 문제가 있었습니다 —
  실제 보고된 사례: 원유/엔화/CodeRabbit 투자유치라는 서로 무관한 헤드라인 3개가 입력됐고,
  모델이 그중 CodeRabbit 하나만 정확히 요약했는데 전체 헤드라인 대비 토큰 중복도가 6%로 나와
  hallucination으로 오판됐습니다. `newsGroundingText`가 헤드라인 사이를 줄바꿈("\n")으로
  구분해두고(다른 검사들에게는 공백과 동등한 경계일 뿐이라 영향이 없습니다), `findTopicMismatch`가
  이 구분자로 groundingText를 다시 헤드라인 단위로 쪼갠 뒤 생성문과의 중복도를 헤드라인별로 개별
  계산해 그 중 최댓값을 최종 점수로 씁니다 — "생성문이 입력된 헤드라인 중 적어도 하나와는 충분히
  일치하는가"로 판단 기준을 바꾼 것입니다. 헤드라인 하나를 골라 요약하는 정상 사례는 그 하나와
  개별 비교했을 때 여전히 높은 비율이 나와 정상 통과하고, 반면 어떤 헤드라인과도 무관한 진짜
  hallucination은 모든 헤드라인에 대해 낮은 비율로 나오므로 여전히 잡힙니다. 매칭된 헤드라인
  하나에 대한 계산식(overlap ÷ 그 헤드라인의 토큰 수) 자체는 예전과 동일하므로, 기존에 실측해
  정한 임계값(`topicOverlapMinRatio`, 0.1)도 헤드라인 개수와 무관하게 그대로 유효합니다 — 이보다
  올리면 위에서 실측한 정상 의역의 최저치(KLA 사례, 14%)가 다시 오탐 대상이 됩니다.
- **반복 감지** (hardFailure): `findRepeatedPhrase`가 두 단계로 "모델이 루프에 빠졌다"를
  검사합니다 — ①`findRepeatedSentence`: 마침표로 나눈 완전한 문장이 통째로 두 번 이상 그대로
  등장하는 경우, ②길이 10자 이상인 부분 문자열이 8자 이내의 간격을 두고 바로 이어 다시 등장하는
  "말더듬" 패턴. 원래는 부분 문자열 길이만 봤는데, "Mesa Laboratories"처럼 회사명이 서로 다른
  두 문장에서 각각 자연스럽게 한 번씩 언급된 것까지 반복 루프로 오탐하는 사례가 있었습니다.
  실측 결과 진짜 루프(예: "60.42%의 지분을 보유한 60.42%의 지분을 보유한")는 반복 사이 간격이
  한 자릿수인 반면, 자연스러운 재언급은 30자 이상 떨어져 있어 이 간격 기준으로 명확히
  구분됩니다. 단순히 최소 길이를 20자로 늘리는 방식은 검토했지만, 그러면 위 60.42% 반복
  사례(반복 구절 자체가 16자)가 더는 안 잡히는 회귀가 생겨 채택하지 않았습니다. 이전에 캐시된
  브리핑과 비교하지는 않습니다 — 매 생성 결과 "내부"의 반복만 잡을 뿐, 여러 번의 생성에 걸쳐
  비슷한 문구가 되풀이되는 것은 감지 대상이 아닙니다.

새로운 실패 유형을 발견했을 때는 프롬프트에 규칙 문장을 추가하기보다 먼저 위 검사기 중 하나를
추가/확장해 해결할 수 없는지부터 검토하세요 — 검사기는 실행 시점에만 비용이 들지만, 프롬프트
문구는 캐시가 미스될 때마다 세 섹션(날씨/환율/뉴스) 각각에서 토큰 비용이 듭니다. 실제로 뉴스
섹션 프롬프트는 규칙이 하나둘 늘어날 때마다 총 토큰 수가 커져 (당시 기본 모델이던)
`llama-3.1-8b-instant`의 분당 한도(6,000 TPM)를 두 차례(6,148토큰, 이후 2,464토큰) 위협한 전례가
있습니다(현재 기본 모델 `openai/gpt-oss-20b`의 분당 한도는 8,000 TPM으로 오히려 더 여유롭습니다 —
아래 "Groq API 키 발급" 문단 참고). 재발을 막기 위해
`TestWeatherBriefingPromptFitsWithinTokenBudget`/`TestExchangeBriefingPromptFitsWithinTokenBudget`/
`TestNewsBriefingPromptFitsWithinTokenBudget`(`backend/briefing_section_test.go`)이 세 섹션
프롬프트의 추정 토큰 수 예산을 각각 고정해두고 있으니, 프롬프트를 수정했다면 반드시 이 테스트를
통과하는지 확인하세요.

## AI 생성 문장 줄바꿈 (프론트엔드, 브리핑/로또 인사이트 공용)

Groq가 생성한 여러 문장짜리 텍스트를 화면에서 문장 단위로 줄바꿈해 보여주기 위해
`frontend/src/utils/sentenceSplit.ts`의 `splitSentences`를 AI 브리핑(`BriefingCard.tsx`)과
로또 AI 인사이트(`LottoCard.tsx`)가 함께 씁니다. `.`을 문장 경계로 보되, 다음 두 경우는
예외로 취급해 그 온점 뒤에서는 줄바꿈하지 않습니다.

- **소수점**: 온점 양쪽이 모두 숫자인 경우(예: "1.5%", "1470.11")
- **영문 약어**: 온점 직전 단어가 `pvt`/`ltd`/`inc`/`co`/`corp`/`llc`/`llp`/`jr`/`sr`/`mr`/
  `mrs`/`dr`/`vs`/`etc`/`no`/`st`(대소문자 무관, 단어 경계 기준으로 매칭 — 예: "Post"의 "st"처럼
  다른 단어 끝부분과 우연히 겹치는 경우는 매칭되지 않습니다) 중 하나인 경우

두 번째 예외는 실제 보고된 오탐을 고친 것입니다: "Vantage Integrated Securities Solution
Pvt. Ltd."라는 회사명이 "Pvt."와 "Ltd." 뒤에서 각각 줄바꿈되어 회사명이 세 줄로 쪼개져
보였습니다. 완벽한 약어 사전은 아니지만(뉴스/법률/기업명에 자주 등장하는 것 위주로만
구성했습니다), "Dr. Kim은 발표했습니다. 이는..."처럼 약어와 진짜 문장 종결이 함께 있는
경우에도 약어 뒤는 건너뛰고 그다음 실제 문장 끝에서는 정상적으로 줄바꿈됩니다.

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

NewsData.io 응답에는 공식 문서에 없는 한도 관련 헤더가 실제로 실려 옵니다(직접 호출해서 확인함):
`X-RateLimit-Limit`/`-Remaining`/`-Reset`(15분 단위 요청 속도 제한)과 `X-API-Limit-Remaining`(오늘
남은 일일 크레딧). `logNewsDataIORateLimitHeaders`(`backend/news.go`)가 매 호출마다 이 값을 로그로
남겨서, 실패가 재발했을 때 크레딧 소진 때문인지 아닌지를 바로 확인할 수 있게 합니다. 뉴스 조회
타임아웃(`newsSectionTimeout`/`newsTimeout`)은 "context deadline exceeded" 실패가 실제로 보고된
뒤 8초에서 12초로 늘렸습니다 — 진단 결과 크레딧 소진이나 8초라는 값 자체가 원인은 아니었고(그
시점 남은 크레딧은 충분했고, 같은 조합으로 즉시 재시도하니 0.9초 만에 정상 응답) 원인을 알 수
없는 일시적 외부 지연 쪽에 무게가 실려서, 그런 순간적 지연을 흡수할 여지를 조금 늘리는 선택을
했습니다. 환율은 여전히 8초(`sectionTimeout`)를 그대로 씁니다 — 뉴스만 외부 API 쪽에서 이런
증상이 실제로 보고됐기 때문입니다.

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
않습니다. 성공한(빈 문자열이 아닌) 번역은 그대로 `translated_title`에 저장되고,
`failure_reason`/`retry_after`는 비워둡니다(`''`/`NULL`).

번역이 실패하면 `translated_title`은 일부러 빈 문자열로 남겨서(프론트엔드가 원문
표시로 폴백하게 하기 위해) 실패 사유(`failure_reason`)와 재시도 가능 시각
(`retry_after`)을 함께 저장합니다(`news_translation.go`의
`recordNewsTranslationFailure`) — 실패 사유에 따라 재시도 속도를 다르게 가져가기
위해서입니다.

- `rate_limit`(Groq TPM 한도 초과로 배치 전체가 실패): 쿨다운 45초.
  Groq의 분당 토큰 예산은 그 다음 분(minute) 버킷이면 대개 다시 열리므로, 짧게
  기다렸다가 빠르게 재시도하는 편이 유리합니다.
- `validation_failed`(한자/영어 혼입 등 검증 실패로 개별 항목만 비워진 경우)와
  `api_error`(그 외 일반 API 오류)는 기존처럼 쿨다운 5분을 유지합니다 — 같은
  콘텐츠를 당장 재시도해도 모델이 비슷한 결과를 낼 가능성이 있기 때문입니다.

실패 사유는 `classifyNewsTranslationFailureReason`이 에러 메시지에 "rate
limit"/"tokens per minute"/"(tpm)"가 있는지로 판별하고(`briefing.go`의
`classifyBriefingFailureReason`과 같은 방식), 검증 실패(`validation_failed`)는
에러가 아니라 성공 응답 안에서 개별 항목의 `translatedTitle`만 비어있는 경우라서
`translateNewsItems`가 직접 분류합니다. 재시도 여부는 `recentlyFailedNewsTranslation`
이 `retry_after`가 지났는지만 확인하면 되므로, DB에 실패를 영구히 남겨도 안전합니다
— 예전에는 실패를 DB에 전혀 남기지 않고(빈 문자열 행이 "캐시 성공"으로 오인되는
버그가 있었기 때문) 프로세스 메모리에만 5분간 기록했는데, 그러면 모든 실패 사유가
구분 없이 똑같은 5분을 기다려야 했습니다. `retry_after`라는 만료 시각 자체를
저장값에 포함시켜두면 "빈 문자열 행 = 캐시 성공"으로 오인하는 예전 버그와 같은
문제 없이도 DB에 영구히 남길 수 있습니다(`lookupNewsTranslation`이 빈 문자열 행을
캐시 히트로 취급하지 않도록 조건을 명시했습니다).

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

### 2. 자동 수집 — 서버 시작 시 밀린 회차 전체 채우기 + 평상시 최신 회차 1개만

데이터 수집은 화면의 "🔄 매주 자동 업데이트: ON/OFF" 토글로 켜고 끌 수
있습니다(`POST /api/lotto/collection/start` / `/stop`,
`GET /api/lotto/collection/status`). **서버가 시작할 때 기본으로 이미
켜져 있습니다** — `LOTTO_AUTO_COLLECTION_DEFAULT=off`로 설정하면 예전처럼
항상 꺼진 상태로 시작하며, 토글을 직접 눌러야만 켜집니다.

자동 수집이 켜지면(서버 시작 시 또는 토글을 켤 때) 백그라운드 goroutine이
먼저 **밀린 회차 전체**를 확인합니다: DB에 저장된 최신 회차와, 2002-12-07
(1회차)부터 매주 토요일 추첨되는 주기로 계산한 이론적 최신 회차 사이에
몇 개나 비어있는지 계산하고, 있다면 오래된 순서대로 전부 순차 조회해
채웁니다(`catchUpMissingLottoRounds`, `backend/lotto.go`). 회차 사이에는
무료 서비스에 대한 예의로 약 300ms 간격만 두며, 회차 하나 조회에
실패해도(그 회차 파일이 데이터셋에 없는 경우 등) 짧게 재시도한 뒤 건너뛰고
나머지 회차는 계속 처리합니다 — 한 회차의 실패가 이후 회차 전부를 막지
않습니다. 밀린 회차를 전부 채운 뒤로는 `time.Ticker`로 24시간마다 딱 한
가지만 확인하는 평상시 점검(`checkForNewLottoRound`)으로 전환됩니다 — 실제로
새 회차는 일주일에 한 번만 생기므로 그걸로 충분합니다.

밀린 회차를 채우는 동안 `GET /api/lotto/collection/status`는
`catchingUp: true`와 함께 `totalPendingCount`(밀린 개수)/
`processedCount`(지금까지 처리한 개수)를 반환하고, 화면은 "N회차 밀려있어
순차적으로 채우는 중입니다 (처리한 개수/전체)"를 보여줍니다 — 다 채워지면
`catchingUp`이 다시 `false`가 되면서 문구도 사라집니다.

**원래는 dhlottery(`https://www.dhlottery.co.kr/common.do?method=getLottoNumber`)를
서버가 직접 호출했습니다.** 초기 50회 데이터를 채우려고 여러 회차를 동시에
(세마포어로 병렬) 긁어왔는데, 이렇게 짧은 시간에 요청이 몰리자 dhlottery가
이를 봇으로 판단해 이 서버의 IP 자체를 차단해버렸고, 그 뒤로는 재시도해도
계속 차단된 상태라 자동 수집이 완전히 멈춰 있었습니다. 이를 우회하기
위해, 커뮤니티가 유지 관리하는 공개 GitHub 저장소
[`smok95/lotto`](https://github.com/smok95/lotto)에서 회차 데이터를
가져오도록 수집 경로를 갈아탔습니다(`fetchLottoDrawFromGitHub`,
`backend/lotto.go`) — 이 저장소는 매주 토요일 추첨 직후(실측: 2026-07-25/
08-01/08-08 모두 KST 20:41~21:00 사이 커밋)에 회차별 JSON 파일
(`https://raw.githubusercontent.com/smok95/lotto/main/results/{회차}.json`)을
자동 커밋해 그대로 서빙하므로, dhlottery를 전혀 두드리지 않고도 최신 회차를
얻을 수 있습니다. 이 GitHub 소스는 dhlottery와 달리 봇 차단 위험이 낮은
일반 정적 파일 서빙이라, 서버 시작 시 밀린 회차 전체를 한 번에 몰아서
조회해도 안전합니다.

dhlottery를 직접 호출하던 원래 코드(`fetchLottoDraw`/
`fetchLottoDrawWithShortRetry`, `backend/lotto.go`)는 **삭제하지 않고
그대로 남겨뒀지만, 자동 수집 경로(`checkForNewLottoRound`/
`catchUpMissingLottoRounds`)는 이 둘을 전혀 호출하지 않습니다** — 이미 이
서버의 IP가 차단된 상태라 다시 두드려봐야 실패만 반복될 뿐이고, 그
재시도 자체가 차단을 더 굳히는 원인이 될 수도 있기 때문입니다. GitHub
소스가 실패하면(파일이 아직 없거나 형식이 바뀐 경우) dhlottery로
폴백하지 않고 그대로 실패로 취급합니다 — 평상시 점검은 다음 정기
점검까지 조용히 기다리고, 밀린 회차 채우기는 그 회차만 건너뛰고 나머지를
계속 진행합니다. 나중에 dhlottery 접근이 다시 가능해지거나(IP 차단이
풀리거나) 이 GitHub 저장소가 더 이상 유지되지 않게 되면, 남겨둔 코드를
다시 연결해서 쓸 수 있습니다 — 되살릴 때는 예전처럼 회차를 동시에
병렬로 긁어오지 말고 반드시 순차적으로만 호출해야 같은 차단을 반복하지
않습니다.

GitHub 소스가 계속 실패하면 아래 "관리자 API"로 수동 입력할 수 있습니다. 상태
응답은 `lastCollectedAt`(마지막으로 신규 회차를 저장한 시각),
`lastCheckedAt`/`nextCheckAt`(마지막/다음 점검 시각), `savedCount`를 담습니다.

### 3. 관리자 API — GitHub 소스가 막혔을 때의 수동 대체 수단

자동 수집이 계속 실패한다면(예: GitHub 저장소가 더 이상 유지되지 않거나
일시적으로 접근할 수 없는 경우), 회차를 수동으로 채워 넣을 수 있는 관리자
전용 API가 있습니다. **프론트엔드 화면 어디에도 이 기능으로 연결되는
버튼이나 메뉴가 없습니다** — 순수하게 API로만 사용합니다.

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

### 5. 지난주 추천 결과 — 재미용 사후 비교, 순위가 아님

새 회차(다음 주 실제 당첨번호)가 저장되면(자동 수집 성공 또는 관리자
수동 입력, `backend/lotto_recommendation_history.go`), 그 회차가 속한
직전 사이클에 대해 `trend`/`regression`/`uniform` **세 모드 모두**의
추천 번호가 실제 당첨번호와 몇 개 겹쳤는지 계산해 저장합니다.

- 사용자가 지난주에 실제로 조회한 모드가 하나(예: uniform)뿐이었어도,
  나머지 두 모드는 그 시점의 데이터로 4단계 파이프라인을 그대로
  재사용해 사후 계산합니다(`ensureLottoRecommendationForPastCycle`) —
  `computeNumberStats`/`generateRecommendationSet`이 "현재 시각"을
  하드코딩하지 않고 frequency/history를 파라미터로 받는 순수 함수라
  가능한 일입니다. 이렇게 사후 계산된 행은 `is_retroactive=1`로
  표시됩니다.
- `GET /api/lotto` 응답의 `previousRecommendationResult` 배열은 항상
  `trend → regression → uniform` **고정 순서**로 옵니다. 일치 개수가
  큰 순서로 정렬하지 않습니다 — 이 순서 고정 자체가 "어떤 방식이 더
  우수하다"는 인상을 주지 않기 위한 설계입니다. 프론트엔드
  (`LottoPreviousResult.tsx`)도 이 순서를 그대로 렌더링할 뿐 절대
  재정렬하지 않습니다.
- 겹친 번호 개수는 보너스 번호를 제외한 순수 6개 대 6개 교집합입니다.
- 문구는 의도적으로 "적중률"/"명중률" 같은 성적표 표현이나 다른 모드와
  비교하는 문장을 쓰지 않습니다("N개 일치했네요 🎉" / "이번엔 하나도 안
  맞았어요 😅" 정도의 담백한 톤). 화면 하단에는 항상 "일치 개수는
  순전히 우연이며 세 방식 모두 당첨 확률에 차이가 없다"는 disclaimer가
  붙습니다. `lottoAISystemPrompt`(`backend/lotto_ai.go`)에도 이 일치
  결과를 근거로 "어떤 방식이 더 잘 맞았다"는 식의 비교 문장을 생성하지
  말라는 금지 규칙이 있습니다 — 현재는 AI 인사이트 프롬프트에 이 데이터
  자체를 넘기지 않지만, 나중에 참고 자료로 추가되더라도 이 규칙이
  먼저 막아줍니다.
- **레거시 포맷 자가 치유 시 주의(실제로 겪은 버그)**: `numbers`
  컬럼은 JSON 배열 인코딩이 도입되기 전에 저장된 행이 일부 남아있어
  순수 CSV("1,5,8,20,21,30")로 저장된 경우가 있습니다. 이런 행은
  `decodeRecommendationSet`이 실패해 `lookupLottoRecommendation`이
  `found=true, set=nil`을 반환합니다 — 처음에는 이 경우를 "행이
  없음"과 똑같이 취급해 `INSERT OR IGNORE`로 사후 계산 결과를 저장하려
  했는데, 행이 이미 있어 PK 충돌로 조용히 무시되고 아무것도 고쳐지지
  않아 매 요청마다 새로 계산한(uniform처럼 무작위성이 있는 모드는 매번
  다른) 세트를 반환하는 버그가 실제 운영 DB에서 발생했습니다. 지금은
  `found` 여부로 분기해, 행이 이미 있으면 `reencodeLottoRecommendation`
  으로 실제 `UPDATE`를 하고(이때 `is_retroactive`는 원래 값을 그대로
  보존 — 사후 계산을 다시 했다고 실제 사용자 조회 기록이 사후 계산으로
  둔갑하면 안 됩니다), 이제 막 numbers가 바뀌었으니 기존
  `matched_count`/`matched_numbers`도 함께 NULL로 리셋해 다음 조회 때
  새 numbers 기준으로 재계산되게 합니다.

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
-- 흔적으로 더 이상 쓰지 않지만 컬럼 자체는 남아있다. matched_count/
-- matched_numbers는 다음 회차 발표 후 "지난주 추천 결과"에서 몇 개
-- 일치했는지 계산해 채워진다(위 "5. 지난주 추천 결과" 참고) — 아직 대조
-- 전이면 NULL이다. is_retroactive는 이 행이 사용자가 실제로 조회해서
-- 생겼는지(0), 다른 모드를 보는 사이 뒤늦게 사후 계산으로 채워졌는지(1)
-- 를 구분한다.
CREATE TABLE lotto_recommendation (
  cycle_start_date TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'uniform',
  based_on_drw_no INTEGER NOT NULL DEFAULT 0,
  based_on_data_hash TEXT NOT NULL DEFAULT '',
  numbers TEXT NOT NULL,
  number_groups TEXT NOT NULL DEFAULT '',
  stats_json TEXT NOT NULL DEFAULT '{}',
  generated_at TEXT DEFAULT CURRENT_TIMESTAMP,
  matched_count INTEGER,
  matched_numbers TEXT,
  is_retroactive INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (cycle_start_date, mode)
);

-- 날씨/환율/뉴스 AI 브리핑 문단 캐시. section 값은 "weather", "exchange",
-- "news:{region}:{category}" 형태다. is_fallback은 detailed_text가
-- hallucinationFallback(제목 기반 안전 문구)이었는지를 기록한다 — 위 "AI
-- 브리핑 콘텐츠 검증" 단락 참고.
CREATE TABLE briefing_section_cache (
  section TEXT PRIMARY KEY,
  data_hash TEXT NOT NULL,
  detailed_text TEXT NOT NULL,
  generated_at TEXT DEFAULT CURRENT_TIMESTAMP,
  is_fallback INTEGER NOT NULL DEFAULT 0
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
  `translatedTitle`이 비어 있고 마침 그 사유가 news_translation_cache에 아직 남아있으면
  `translationFailureReason`(`rate_limit`/`validation_failed`/`api_error`)도 함께 채워집니다 —
  화면에 직접 노출하는 값은 아니고, 프론트엔드(`NewsCard.tsx`)가 브라우저 콘솔에 사유별로
  그룹 지어 로그를 남기는 데만 쓰입니다(`console.groupCollapsed`) — 서버 로그를 볼 수 없는
  상황에서도 "왜 이 헤드라인이 원문으로 보이는지"를 개발자 도구만으로 확인할 수 있게
  하기 위해서입니다. `annotateNewsTranslationFailureReasons`(`news_translation.go`)가
  응답 직전에 매번 새로 조회하는 이유는, 뉴스 원본 자체는 30분 TTL 캐시(`raw_data_cache`)로
  서빙되는 경우가 많아 그 안에 박제된 `translatedTitle`은 오래된 값일 수 있지만, 실패
  사유/쿨다운은 그보다 훨씬 짧은 주기(45초~5분)로 계속 바뀌기 때문입니다.
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
- `previousRecommendationResult?`: 직전 사이클(지난주)의 세 모드 결과
  배열, 항상 `trend → regression → uniform` 고정 순서입니다(위 "5.
  지난주 추천 결과" 참고). 각 항목은
  `{ mode, numbers, matchedCount, matchedNumbers, isRetroactive,
  actualDrwNo, actualNumbers }` 형태이며, 대조할 과거 데이터 자체가
  없는 아주 이른 회차에서는 필드 자체가 생략됩니다.

`GET /healthz`는 DB/외부 API를 전혀 건드리지 않고 프로세스가 요청을 받을 수
있으면 즉시 200을 반환합니다 — Render 등 플랫폼 헬스체크 전용이며(위
`render.yaml`의 `healthCheckPath` 참고), 로또 GitHub 데이터셋/Groq/NewsData.io
장애가 헬스체크 실패로 오인되어 불필요한 재시작이 도는 것을 막습니다.

## 동시성 설계

- 날씨 / 환율 / 뉴스: `sync.WaitGroup` + 섹션별 독립 `context.WithTimeout` 로 병렬 처리 —
  환율은 8초(`sectionTimeout`), 뉴스는 12초(`newsSectionTimeout`/`newsTimeout`), 날씨만
  21초(`weatherSectionTimeout`, 바로 아래 참고). 뉴스만 8초에서 12초로 늘린 것은 "context
  deadline exceeded" 실패가 실제로 보고됐기 때문입니다 — 원인이 크레딧 소진이나 8초라는 값
  자체는 아니었지만(위 "NewsData.io API 키 발급" 참고), 원인 불명의 일시적 외부 지연을 흡수할
  여지를 조금 늘려뒀습니다. 한 섹션이 실패하거나 타임아웃되어도 나머지 섹션은 정상적으로
  응답합니다. 이 raw 데이터 조회 단계와 별개로, 그 이후 순차적으로 실행되는 AI 브리핑
  생성 단계(`getBriefing`, 날씨/환율/뉴스 3섹션의 Groq 호출을 공유하는 하나의 컨텍스트)는
  `briefingGenerationTimeout`(15초, `handler.go`)을 씁니다 — 예전에는 이 단계도 그냥
  `sectionTimeout`(8초)을 재사용했는데, Groq rate limit 재시도 대기 시간이 실제로 8초를
  넘는 사례(8.18초)가 보고돼 별도 상수로 분리하고 늘렸습니다(위 "Groq TPM rate limit
  재시도" 문단 참고).
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
  헤드라인 5개를 한 번의 JSON 모드 호출로 배치 번역하며, 성공한 번역 결과만 기사 id
  기준으로 별도 캐싱되어 같은 기사가 남아있는 동안 재번역하지 않습니다
  (`news_translation_cache`). 검증 실패(CJK/영어 혼입)나 API 오류로 번역이 비어버린
  경우는 실패 사유(`rate_limit`/`validation_failed`/`api_error`)와 재시도 가능
  시각을 이 테이블에 함께 기록해뒀다가(`recentlyFailedNewsTranslation`) 그 안에
  재요청되면 즉시 원문 표시로 폴백하고 쿨다운이 지나면 자동으로 재시도합니다 —
  그렇지 않으면 한 번 실패한 기사가 노출되는 내내 "번역 실패"로 굳어버립니다.
  `rate_limit`만 45초로 훨씬 짧은 쿨다운을 쓰고 나머지는 5분을 유지합니다(위
  "원본 데이터 캐시" 단락 참고) — rate limit은 Groq TPM 예산이 다음 분(minute)
  버킷이면 대개 풀려있어 빠르게 재시도하는 편이 유리한 반면, 검증 실패는 같은
  콘텐츠를 당장 재시도해도 비슷한 결과가 나올 가능성이 있기 때문입니다.
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
- 로또: `GET /api/lotto`는 외부 API를 전혀 건드리지 않고 DB에 있는 데이터만
  읽어서 보여줍니다 — 수집(초기 시드 + 서버 시작 시 밀린 회차 전체 채우기 +
  이후 매주 최대 1회 자동 확인)은 완전히 분리된 백그라운드 경로입니다(위
  "로또 섹션" 참고). 밀린 회차를 채울 때도 여러 회차를 동시에 병렬로
  조회하지 않고 항상 하나씩 순차적으로만 처리하므로 별도의 동시성 제어
  (세마포어 등)가 필요 없습니다.
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
  보내지 않습니다. 뉴스 카테고리/지역 변경은 `retrySection('news')`를 호출하며,
  이는 브리핑 카드 전체가 아니라 `briefingSectionPending.news`만 true로 만들어
  뉴스 문단 하나만 개별적으로 pending 처리합니다(`useDashboard.ts`) — 날씨/환율
  문단까지 스켈레톤으로 덮을 이유가 없기 때문입니다. 응답이 오면 날씨/환율
  문단(과 그 `briefingMeta`)은 이전 값을 그대로 유지하고 뉴스 문단만 새 값으로
  교체합니다.
- 뉴스 카드(`NewsCard.tsx`) 자체의 스켈레톤도 최초 로드뿐 아니라 카테고리/지역이
  바뀔 때마다(재조회 중에는 항상) 다시 나타납니다 — 예전에는 렌더 조건이
  `loading && !section`이라 이미 데이터가 있는 상태(재조회)에서는 `!section`이
  거짓이 되어 스켈레톤이 뜨지 않고, 응답이 올 때까지 이전 목록이 그대로 얼어붙어
  보이는 문제가 있었습니다(국내→해외처럼 번역이 끼어 응답이 느린 전환에서 특히
  두드러졌습니다). 조건을 `loading`만으로 단순화하고 목록/안내 문구 쪽에
  `!loading`을 추가해, 로딩 중엔 항상 스켈레톤이 목록을 대체하도록 했습니다.
  스켈레톤↔목록 전환에는 `.briefing__text`와 같은 `fade-in 200ms ease-out`을
  재사용해 뚝 끊기지 않게 했습니다. 스켈레톤 자체는 원(circle) + 가로줄
  조합의 전용 마크업 대신, `BriefingCard`의 "문단 갱신 중" 스켈레톤과
  완전히 같은 `briefing__skeleton-line`(회색 반투명 shimmer 가로 막대)을
  그대로 재사용합니다 — 헤드라인 5개 자리에 맞춰 5개를 두되, 실제
  제목처럼 길이가 들쭉날쭉해 보이도록 너비를 조금씩 다르게 줘서(전부
  같은 너비면 오히려 "막대 목록"처럼 보여 헤드라인이라는 인상이 덜
  듭니다), 두 카드의 로딩 표현이 시각적으로 통일되게 했습니다.
- `useNews.ts`의 `load`에 실제로 보고된 경쟁 상태(race condition) 버그가
  있었습니다: category/region이 바뀌어 이전 요청을
  `abortRef.current?.abort()`로 취소하고 새 요청을 시작해도, 취소된
  이전 요청의 `try/catch/finally` 블록은 여전히 끝까지 실행됩니다 —
  `catch`는 `AbortError`를 보고 조용히 `return`하지만, `finally`는
  `return` 이후에도 항상 실행되어 `setLoading(false)`를 호출합니다.
  이 abort는 비동기로 처리되므로, 새 요청이 `setLoading(true)`를 호출한
  *뒤에* 옛 요청의 이 `finally`가 뒤늦게 실행되어 방금 켜진 로딩
  상태를 도로 꺼버릴 수 있었습니다 — 그러면 새 요청이 여전히 진행
  중인데도 `loading`이 `false`로 잘못 표시되어, 스켈레톤도 목록도
  아닌 완전히 빈 본문이 보이는 문제로 나타났습니다. 이제 `finally`에서
  `abortRef.current === controller`(이 호출이 여전히 "현재" 요청인지)를
  먼저 확인해, 이미 다른 요청으로 대체된 옛 요청은 `loading` 상태를
  건드리지 않습니다.
- 뉴스 카테고리 버튼과 국내/해외 토글(`NewsCard.tsx`)은 `loading`(이 카드
  자체의 `/api/news` 조회) **또는** `briefingInFlight`(날씨/환율/뉴스
  AI 브리핑 3섹션 중 하나라도 아직 생성 중)가 `true`이면 `disabled`
  처리되어 클릭이 무시되고, `opacity: 0.6` + `cursor: not-allowed`로
  흐리게 표시되며 버튼 아래에 "브리핑 생성 중에는 잠시 후 이용해주세요"
  안내 문구가 나타납니다 — 카테고리/지역 탭을 빠르게 연달아 전환하면
  이 카드 자체의 재조회뿐 아니라 브리핑의 뉴스 문단(과 그 번역)까지
  함께 재생성되는데, 둘 다 Groq를 호출하는 별개의 비동기 흐름이라
  `loading` 하나만으로 잠그면 카드 자체 조회는 먼저 끝나고 브리핑
  쪽만 아직 진행 중인 틈에 탭을 또 바꿔 Groq 호출이 겹쳐 쌓일 수
  있었습니다(`loading`만 보던 이전 구현에서 실제로 이런 이유로 rate
  limit이 반복됐습니다). `briefingInFlight`는 `useDashboard.ts`에서
  `briefingPending`(최초 로드/전체 재시도처럼 3섹션이 캐시 없이
  한꺼번에 새로 생성될 때 켜짐)과 `briefingSectionPending`의
  weather/exchange/news 세 필드(도시/통화/카테고리 변경으로 인한 부분
  재생성)를 모두 OR로 묶은 파생값입니다 — `briefingSectionPending`
  세 필드만으로는 최초 로드나 "재시도" 버튼처럼 3섹션이 한꺼번에
  캐시 없이 생성되는(가장 Groq 호출이 몰리는) 경우를 놓치기 때문에
  `briefingPending`도 포함시켰습니다. 두 플래그 모두 응답이 오든
  실패하든 각 훅의 `finally`에서 항상 해제되므로, 브리핑과 뉴스
  카드 조회가 모두 끝나는 즉시 버튼은 다시 클릭 가능한 상태로
  돌아옵니다.
- 로또 "이번 주 추천 번호"의 추천 방식(핫넘버/콜드넘버/완전 무작위)
  드롭다운도 같은 문제가 있었습니다: `useLotto.ts`의 `load`는 `mode`가
  바뀌면 `/api/lotto?mode=...`를 다시 통째로 fetch하는데, 카드 전체
  스켈레톤 조건이 `loading && !section`이라(뉴스 카드가 예전에 겪은 것과
  똑같은 패턴) `section`이 이미 있는 상태(모드 전환)에서는 로딩 표시가
  전혀 없이 이전 추천 번호가 그대로 보이다가 새 응답이 오면 갑자기
  바뀌었습니다. `useLotto.ts`에 `recommendationPending`을 추가했는데,
  이 플래그는 초기 로드나 "재시도"가 아니라 오직 추천 방식을 바꿀
  때만(전용 `changeRecommendationMode` 함수를 거칠 때만) 켜집니다.
  `LottoRecommendation.tsx`는 이 플래그가 true인 동안 추천 번호 뱃지
  6개와 홀짝비/합계/직전회차 중복/구간분포 통계를 `BriefingCard`의
  "문단 갱신 중" 스켈레톤과 완전히 같은 스타일(번호 뱃지는
  `skeleton-circle` 원형, 통계는 `briefing__skeleton-line` 가로 막대)로
  대체하고, 드롭다운 자체도 `disabled` 처리해 응답이 오기 전 추가
  전환으로 요청이 겹치는 것을 막습니다. 이 작업 중에 `useNews.ts`에서
  고쳤던 것과 완전히 같은 경쟁 상태 버그가 `useLotto.ts`의 `load`에도
  있는 것을 발견해 함께 고쳤습니다 — 이전 요청이 abort된 뒤에도 그
  요청의 `finally { setLoading(false) }`가 무조건 실행되어, 새 요청이
  이미 `setLoading(true)`를 호출한 뒤에 뒤늦게 그 값을 도로 꺼버릴 수
  있었습니다. `abortRef.current === controller`로 "여전히 현재
  요청인지" 확인한 뒤에만 `loading`/`recommendationPending`을 갱신하도록
  고쳤습니다. 서버 캐시가 있어(같은 사이클 안에서 이미 조회한 모드로
  되돌아가면 재계산 없이 즉시 응답) 왕복이 매우 짧게 끝날 수 있는데,
  그러면 스켈레톤이 깜빡이는 것처럼 보일 수 있어 `recommendationPending`
  이 최소 300ms(`MIN_RECOMMENDATION_PENDING_MS`)는 유지되도록
  했습니다 — 응답이 그보다 먼저 와도 `setTimeout`으로 나머지 시간만큼
  더 기다렸다가 끕니다. 스켈레톤↔실제 세트 전환에는 다른 카드들과 같은
  `fade-in 200ms ease-out`을 재사용합니다.
