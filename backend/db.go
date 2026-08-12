package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/tursodatabase/go-libsql"
)

const (
	// Turso(원격)에 연결할 때 쓰는 커넥션 풀 설정이다. Turso 연결은 로컬
	// 파일이 아니라 HTTP 기반 클라이언트/서버 접속이라, 예전 Aiven MySQL과
	// 마찬가지로 여러 커넥션을 동시에 열어도 안전하다 — 유휴 연결을
	// 끊어버리는 클라우드 프로바이더 앞에서 "connection reset"을 피하려고
	// 짧은 ConnMaxLifetime을 두는 이유도 그대로 유지한다.
	remoteDBMaxOpenConns    = 10
	remoteDBMaxIdleConns    = 5
	remoteDBConnMaxLifetime = 3 * time.Minute

	// 로컬 파일 모드(libSQL == SQLite 호환 파일 포맷)는 사정이 다르다 —
	// 동시에 여러 쓰기 연결이 열리면 SQLite/libSQL 특유의 단일 writer
	// 제약 때문에 "database is locked" 에러가 나기 쉽다. 커넥션 풀
	// 자체를 1개로 강제해 모든 쿼리(읽기 포함)를 자연히 직렬화하면 이
	// 문제가 원천적으로 사라진다 — 이 프로젝트는 개인용 대시보드
	// 수준의 트래픽이라 그 정도 직렬화로 인한 성능 손해는 무시할 만하다.
	localDBMaxOpenConns = 1

	defaultLocalDBPath = "data/dashboard.db"
)

// connectDB는 TURSO_DATABASE_URL 환경변수가 설정되어 있으면 원격
// Turso(libSQL) 데이터베이스에, 없으면 로컬 파일(기본값
// backend/data/dashboard.db)에 연결한다 — 이 분기 하나로 로컬 개발과
// 프로덕션 배포를 모두 커버한다. 로컬 개발자는 Turso 계정이나 별도 서버
// 없이 `go run .`만으로 바로 로또/캐시 테이블이 있는 환경을 얻고, 배포
// 시에는 환경변수 하나만 채우면 그대로 원격 DB로 전환된다.
//
// 두 브랜치 모두 등록된 "libsql" 드라이버(github.com/tursodatabase/go-libsql,
// 위 blank import가 init()에서 등록한다)를 사용한다 — libSQL은 SQLite와
// 완전히 호환되는 파일 포맷이자 SQL 문법이므로, 로컬 파일과 원격 Turso가
// 정확히 같은 쿼리로 동작한다(로컬에서만 통하고 배포하면 깨지는 방언
// 차이가 없다).
func connectDB() (*sql.DB, error) {
	dbURL := os.Getenv("TURSO_DATABASE_URL")
	if dbURL == "" {
		return connectLocalDB()
	}
	return connectRemoteDB(dbURL, os.Getenv("TURSO_AUTH_TOKEN"))
}

// dbConnectMaxAttempts/dbConnectRetryInterval은 connectDBWithRetry가
// connectDB+migrate 전체를 몇 차례, 얼마의 간격으로 재시도할지 정한다.
// 마이그레이션은 서버가 뜰 때 딱 한 번만 실행되던 일회성 작업이라, 그
// 시도 시점에 Turso 인프라가 잠깐(몇 초~십수 초) 흔들리고 있으면
// dbErrorTypeTursoOutage로 확정되어 로또 섹션이 서버 재배포 전까지
// 계속 비활성화된 채로 남았다 — 실제로는 그 장애가 대개 짧게 끝나므로,
// 확정 전에 몇 초 간격으로 몇 번 더 시도해볼 여유를 둔다. 4회 × 3초는
// 최악의 경우 서버 시작을 약 9초 지연시키는데, 이는 시작 시 단 한 번만
// 감수하는 비용이라 감내할 만하다.
const dbConnectMaxAttempts = 4
const dbConnectRetryInterval = 3 * time.Second

// connectDBWithRetry는 connectDB로 연결한 뒤 곧바로 migrate까지 실행하는
// 전체 시퀀스를, 실패할 때마다 dbConnectRetryInterval만큼 쉬고
// dbConnectMaxAttempts번까지 다시 시도한다. 연결에는 성공했지만
// 마이그레이션에서 실패한 경우 그 커넥션을 닫고 처음부터(재연결부터) 다시
// 시도한다 — 마이그레이션 실패가 커넥션 자체의 문제(예: 연결이 그 사이에
// 끊김)일 수도 있기 때문이다. 마지막 시도까지 실패하면 그 마지막 에러를
// 반환하며, 호출자(main)가 classifyDBErrorType으로 분류해 상태를 확정한다.
func connectDBWithRetry() (*sql.DB, error) {
	var lastErr error
	for attempt := 1; attempt <= dbConnectMaxAttempts; attempt++ {
		conn, err := connectDB()
		if err == nil {
			if migrateErr := migrate(conn); migrateErr != nil {
				conn.Close()
				lastErr = migrateErr
			} else {
				return conn, nil
			}
		} else {
			lastErr = err
		}

		if attempt < dbConnectMaxAttempts {
			log.Printf("DB 연결/마이그레이션 시도 %d/%d 실패, %s 후 재시도: %v", attempt, dbConnectMaxAttempts, dbConnectRetryInterval, lastErr)
			time.Sleep(dbConnectRetryInterval)
		}
	}
	return nil, lastErr
}

const (
	// dbErrorTypeTursoOutage는 Turso 인프라 자체(엣지/프록시 레이어)가
	// 요청을 처리하지 못하는 것으로 보이는 실패다 — 우리 쪽 자격증명이나
	// 설정과 무관하며, 보통 시간이 지나면 스스로 복구된다.
	dbErrorTypeTursoOutage = "turso_outage"
	// dbErrorTypeConnectionFailed는 그 외 일반적인 연결 실패다(인증 토큰
	// 오류, DNS/타임아웃, 잘못된 URL 등) — 원인이 우리 쪽 설정에 있을 수
	// 있어 자동 복구를 기대하기 어렵다.
	dbErrorTypeConnectionFailed = "connection_failed"
)

// dbOutageStatusPattern은 Turso 인프라 장애를 시사하는 에러 메시지의 특징을
// 정규식 하나로 잡아낸다: "upstream forward failed"(Turso 엣지 프록시에서
// 실제로 관측된 바 있는 문구)나, HTTP 5xx 계열 상태 코드(502 Bad Gateway를
// 포함해 500~599 전체)가 메시지에 포함되어 있으면 우리 쪽이 아니라 Turso
// 쪽 인프라 문제로 본다 — 502 Bad Gateway는 "502"라는 세 자리 숫자
// 자체가 이미 이 5xx 패턴에 들어맞으므로 별도 문구로 다시 매칭할 필요가
// 없다. \b로 단어 경계를 확인해, 숫자 앞뒤에 다른 숫자가 더 붙어있는
// 상황(예: 포트 번호 5000, "1500토큰")까지 우연히 걸리지 않게 한다.
var dbOutageStatusPattern = regexp.MustCompile(`upstream forward failed|\b5\d{2}\b`)

// classifyDBErrorType은 DB 연결/마이그레이션 실패 err를
// dbErrorTypeTursoOutage 또는 dbErrorTypeConnectionFailed로 분류한다. err가
// nil이면(연결이 정상이라는 뜻이므로) 빈 문자열을 반환한다 — lottoHandler는
// 이 값을 그대로 LottoSection.DBErrorType에 실어 보내는데, omitempty
// 덕분에 빈 문자열은 응답 JSON에서 필드 자체가 사라져 "정상" 상태를
// null/없음으로 표현한다.
func classifyDBErrorType(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if dbOutageStatusPattern.MatchString(msg) {
		return dbErrorTypeTursoOutage
	}
	return dbErrorTypeConnectionFailed
}

// dbErrorState는 마이그레이션 재시도가 모두 소진된 뒤 확정된 DB 에러
// 종류를 보관한다 — main이 서버 시작 시 딱 한 번만 기록하고, 이후
// lottoHandler 등 요청 핸들러가 여러 goroutine에서 동시에 읽을 수 있으므로
// (net/http는 요청마다 별도 goroutine을 쓴다) 읽기/쓰기 모두 뮤텍스로
// 보호한다.
var dbErrorState struct {
	mu   sync.RWMutex
	kind string
}

func setDBErrorType(kind string) {
	dbErrorState.mu.Lock()
	defer dbErrorState.mu.Unlock()
	dbErrorState.kind = kind
}

// currentDBErrorType은 db가 nil인 동안 lottoHandler가 응답에 실어 보낼
// dbErrorType 값을 읽어온다. setDBErrorType이 아직 한 번도 호출되지
// 않았다면(이론상 db==nil인데 이 값도 비어있는 상태는 있을 수 없지만,
// 방어적으로) dbErrorTypeConnectionFailed로 안전하게 폴백한다 — 빈
// 문자열을 그대로 내보내면 프론트엔드가 "정상"으로 오인할 수 있기
// 때문이다.
func currentDBErrorType() string {
	dbErrorState.mu.RLock()
	defer dbErrorState.mu.RUnlock()
	if dbErrorState.kind == "" {
		return dbErrorTypeConnectionFailed
	}
	return dbErrorState.kind
}

// connectLocalDB는 로컬 파일에 연결한다. "file:" 접두사는 SQLite/libSQL이
// 공통으로 인식하는 URI 형태다(예전 파일 경로를 그대로 이어붙인 것과
// 결과적으로 같은 파일을 열지만, 향후 "?mode=ro" 같은 URI 쿼리 옵션을
// 붙일 여지를 남겨둔다). LOCAL_DB_PATH로 경로를 바꿀 수 있게 해둔 것은
// 주로 테스트/디버깅 편의를 위함이다.
func connectLocalDB() (*sql.DB, error) {
	path := envOrDefault("LOCAL_DB_PATH", defaultLocalDBPath)
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create local db directory %s: %w", dir, err)
		}
	}

	conn, err := sql.Open("libsql", "file:"+path)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(localDBMaxOpenConns)

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	log.Printf("로컬 DB 파일에 연결됨: %s (TURSO_DATABASE_URL이 설정되지 않아 자동으로 폴백)", path)
	return conn, nil
}

// connectRemoteDB는 원격 Turso 데이터베이스에 연결한다. authToken은 DSN에
// 쿼리 파라미터로 실어 보낸다 — go-libsql의 libsql:// 스킴 처리가 바로 이
// 쿼리 파라미터에서 인증 토큰을 읽기 때문이다. url.Values를 통해 붙이는
// 이유는 dbURL에 혹시 이미 쿼리스트링이 있는 경우에도(정상적인 Turso
// URL은 없지만) "?"가 중복되어 잘못된 DSN이 되는 사고를 피하기 위함이다.
func connectRemoteDB(dbURL, authToken string) (*sql.DB, error) {
	dsn := dbURL
	if authToken != "" {
		u, err := url.Parse(dbURL)
		if err != nil {
			return nil, fmt.Errorf("invalid TURSO_DATABASE_URL: %w", err)
		}
		q := u.Query()
		q.Set("authToken", authToken)
		u.RawQuery = q.Encode()
		dsn = u.String()
	}

	conn, err := sql.Open("libsql", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(remoteDBMaxOpenConns)
	conn.SetMaxIdleConns(remoteDBMaxIdleConns)
	conn.SetConnMaxLifetime(remoteDBConnMaxLifetime)

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	log.Println("Turso(원격 libSQL) 데이터베이스에 연결됨")
	return conn, nil
}

const createLottoDrawsTable = `
CREATE TABLE IF NOT EXISTS lotto_draws (
	drw_no INTEGER PRIMARY KEY,
	drw_date TEXT NOT NULL,
	num1 INTEGER NOT NULL,
	num2 INTEGER NOT NULL,
	num3 INTEGER NOT NULL,
	num4 INTEGER NOT NULL,
	num5 INTEGER NOT NULL,
	num6 INTEGER NOT NULL,
	bonus_no INTEGER NOT NULL,
	created_at TEXT DEFAULT CURRENT_TIMESTAMP
)`

// data_hash는 이 인사이트를 생성할 때 실제로 모델에 넘긴 통계 입력
// (frequency + recentAppeared)의 해시다(lotto_ai.go의 hashLottoInsightInput
// 참고). latest_drw_no만으로 캐시 유효성을 판단하면, 관리자가 기존 회차의
// 오타를 나중에 정정해도(회차 번호 자체는 그대로라서) 그 정정이 반영되지
// 않은 낡은 인사이트가 계속 재사용된다 — data_hash가 이런 경우까지 잡아서
// 실제 통계가 바뀌면 latest_drw_no가 그대로라도 캐시를 무효화한다.
const createAIInsightCacheTable = `
CREATE TABLE IF NOT EXISTS ai_insight_cache (
	latest_drw_no INTEGER PRIMARY KEY,
	insight_text TEXT NOT NULL,
	data_hash TEXT NOT NULL DEFAULT '',
	prompt_version TEXT NOT NULL DEFAULT '',
	generated_at TEXT DEFAULT CURRENT_TIMESTAMP
)`

// section은 'weather'/'exchange'/'news:{region}:{category}' 값을 가지며
// 기본키이므로, 섹션마다 정확히 한 행만 유지된다 — 가장 최근에 생성된
// 텍스트와, 그 생성에 쓰인 입력값의 해시를 함께 담는다. simple_text
// 컬럼(브리핑이 simple/detailed 두 버전을 생성하던 시절의 흔적)은 이제
// Turso로 새로 만드는 스키마에는 아예 포함하지 않는다 — MySQL 시절에는
// 이미 배포된 테이블을 건드리지 않으려고 nullable 컬럼으로 남겨뒀지만
// (widenBriefingSectionCacheColumn/makeSimpleTextNullable 참고), 이번
// 마이그레이션은 완전히 새 데이터베이스에 처음부터 스키마를 만드는
// 것이므로 더 이상 쓰이지 않는 컬럼을 굳이 다시 만들 이유가 없다.
// is_fallback은 detailed_text가 hallucinationFallback(제목 기반 안전
// 문구, briefing.go 참고)이었는지를 기록한다 — 실제 보고된 사례: 검증
// 실패로 안전 문구("가장 인기 있는 뉴스: A 3.6-ton mirror...")가 반환돼도
// 그 반환값 자체는 err == nil인 "성공"이라, 이 플래그가 없던 시절에는
// 다른 정상 생성 결과와 구분 없이 data_hash 기준으로 영구 캐싱됐다.
// resolveBriefingSection은 이 값이 true인 행을 data_hash가 일치해도
// "재사용 가능한 캐시"로 보지 않고 매번 재생성을 다시 시도한다.
const createBriefingSectionCacheTable = `
CREATE TABLE IF NOT EXISTS briefing_section_cache (
	section TEXT PRIMARY KEY,
	data_hash TEXT NOT NULL,
	detailed_text TEXT NOT NULL,
	generated_at TEXT DEFAULT CURRENT_TIMESTAMP,
	is_fallback INTEGER NOT NULL DEFAULT 0
)`

// (cycle_start_date, mode)가 복합 기본키다 — 사이클마다, 그리고 그
// 사이클 안에서 선택된 모드마다 정확히 한 행만 존재한다. 이 복합키
// 덕분에 사용자가 화면에서 uniform -> trend -> uniform으로 오가도 각
// 모드의 캐시가 서로 다른 행에 독립적으로 남아있다가 그대로 재사용된다
// (예전에는 cycle_start_date 단독 기본키라 한 사이클에 모드 하나의
// 세트만 저장할 수 있어서, 모드를 바꿀 때마다 이전 모드의 캐시를
// 덮어쓸 수밖에 없었다).
//
// 같은 (사이클, 모드) 조합을 다시 요청했을 때 캐시를 그대로 재사용할지는
// based_on_data_hash(그 행의 통계 계산에 실제로 쓰인 frequency 입력의
// 해시)가 지금의 frequency와 같은지로 판단한다 — 정상적인 주간 흐름
// (매주 새 사이클 시작 시점에 최신 회차도 함께 바뀜)에서는 사실상 항상
// 같지만, 다음 두 예외 상황에서는 어긋날 수 있어 이 컬럼이 필요하다:
// (1) 자동 수집이 막혀 있다가 같은 사이클 도중에 관리자가 새 회차를
// 수동 입력하는 경우, (2) 새 회차 없이 이미 저장된 회차의 오타를 나중에
// 정정하는 경우(latest_drw_no는 그대로라 based_on_drw_no 비교만으로는
// 못 잡아내지만, 정정으로 frequency 자체가 바뀌므로 해시는 달라진다).
// 이 해시는 모드와 무관하게 frequency만으로 계산되므로(모드는 이미 기본키
// 자체가 구분해준다), 회차가 갱신되면 캐시된 모든 모드가 (각자 다음에
// 조회될 때) 독립적으로 무효화된다. based_on_drw_no는 캐시 유효성
// 판단에는 더 이상 쓰이지 않고, 로그에 "어느 회차 기준으로 계산됐는지"를
// 보여주는 용도로만 남아있다.
//
// numbers/stats_json은 세트 1개(번호 6개와 그 통계)를 담는다 —
// lotto_recommendation.go의 encodeRecommendationSet 참고. numbers는
// "[1,5,12,23,34,41]" 같은 단순 JSON 배열이고, stats_json은 같은 세트의
// 통계(홀짝비/합계/구간분포/직전회차중복) JSON 객체다. 캐시 히트 시에도
// 이 통계를 다시 계산하지 않고 그대로 재사용하기 위해 stats_json이
// 필요하다 — 재계산하면 based_on_data_hash가 가리키는 시점의 frequency가
// 아니라 그 이후 바뀐 최신 frequency를 참고해 실제로 보여준 세트와
// 어긋나는 통계가 나올 수 있다.
//
// number_groups는 예전 "빈도 상/중/하 구간에서 2개씩 뽑기" 방식에서 각
// 번호가 어느 그룹(hot/mid/cold)이었는지 저장하던 컬럼이다 — 지금의
// 4단계 파이프라인(가중 샘플링 + 패턴 필터)에는 "번호 하나하나의 그룹"이라는
// 개념 자체가 없으므로 더 이상 쓰지 않는다. 이미 배포된 테이블에서 이
// 컬럼만 안전하게 지울 방법이 마땅치 않아(NOT NULL 제약을 다른 방식으로
// 되돌리는 등 번거로움만 있고 이득은 없다) 컬럼 자체는 남겨두되, 새로
// 쓰는 행에는 항상 빈 문자열을 넣는다.
// matched_count/matched_numbers/is_retroactive는 "지난주 추천 결과 보기"
// 기능(lotto_recommendation_history.go)이 쓴다. matched_count가 NULL이면
// 아직 실제 당첨번호와 대조해본 적이 없다는 뜻이고(그 사이클이 아직
// 진행 중이거나, 새 회차 저장 훅이 아직 한 번도 안 탄 경우), 값이
// 채워지면 그 회차의 실제 당첨번호(보너스 제외)와 몇 개나 겹쳤는지를
// 뜻한다. matched_numbers는 그 겹친 번호를 "5,17" 형태로 담는다.
// is_retroactive는 사용자가 그 주에 실제로 그 모드를 조회해서 생긴 행이
// 아니라, 나중에(새 회차가 저장된 시점에) "그때 조회했다면 무엇이
// 나왔을지"를 사후 계산해 채운 행이라는 뜻이다 — 실제 그 주의 추천
// 대상이었던 것처럼 혼동되지 않도록 프론트엔드가 이 값으로 구분해
// 안내 문구를 붙인다.
const createLottoRecommendationTable = `
CREATE TABLE IF NOT EXISTS lotto_recommendation (
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
)`

// lottoRecommendationHasCompositePrimaryKey는 lotto_recommendation의
// PRIMARY KEY가 이미 (cycle_start_date, mode) 복합키인지 확인한다 —
// PRAGMA table_info가 각 컬럼의 pk 순번(기본키가 아니면 0, 기본키면
// 1부터 시작하는 순번)을 알려주므로, pk > 0인 컬럼이 2개 이상이면
// 복합키다. 브랜드 뉴 데이터베이스는 createLottoRecommendationTable이
// 이미 복합키로 테이블을 만들어두므로 항상 true다 — 이 함수가 false를
// 반환하는 경우는 오직 이 마이그레이션 이전에 cycle_start_date 단독
// 기본키로 만들어진 이미 배포된 테이블뿐이다.
func lottoRecommendationHasCompositePrimaryKey(conn *sql.DB) (bool, error) {
	rows, err := conn.Query(`PRAGMA table_info(lotto_recommendation)`)
	if err != nil {
		return false, fmt.Errorf("read lotto_recommendation schema: %w", err)
	}
	defer rows.Close()

	pkColumns := 0
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("scan lotto_recommendation schema: %w", err)
		}
		if pk > 0 {
			pkColumns++
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read lotto_recommendation schema: %w", err)
	}
	return pkColumns >= 2, nil
}

// migrateLottoRecommendationToCompositeKey는 lotto_recommendation이 아직
// 예전의 cycle_start_date 단독 기본키를 쓰고 있다면 (cycle_start_date,
// mode) 복합 기본키로 다시 만든다. SQLite/libSQL은 ALTER TABLE로 기본키를
// 바로 바꿀 수 없으므로, 표준적인 "새 테이블 생성 -> 데이터 복사 -> 옛
// 테이블 삭제 -> 이름 변경" 절차를 트랜잭션으로 묶어서 쓴다 — 중간에
// 실패해도 옛 테이블과 새 테이블이 동시에 존재하거나 데이터가 유실되는
// 상태로 남지 않는다.
//
// 예전 테이블은 cycle_start_date당 최대 한 행만 가질 수 있었으므로(그게
// 그때의 기본키였다), 그 행을 그대로 복사해 넣어도 새 복합키 제약을
// 위반할 수 없다 — 어떤 모드였든 그 모드 하나로 유일한 행이 된다.
func migrateLottoRecommendationToCompositeKey(conn *sql.DB) error {
	hasComposite, err := lottoRecommendationHasCompositePrimaryKey(conn)
	if err != nil {
		return err
	}
	if hasComposite {
		return nil
	}

	log.Println("로또: lotto_recommendation을 (cycle_start_date, mode) 복합 기본키로 마이그레이션합니다")

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin lotto_recommendation PK migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE lotto_recommendation_new (
			cycle_start_date TEXT NOT NULL,
			mode TEXT NOT NULL DEFAULT 'uniform',
			based_on_drw_no INTEGER NOT NULL DEFAULT 0,
			based_on_data_hash TEXT NOT NULL DEFAULT '',
			numbers TEXT NOT NULL,
			number_groups TEXT NOT NULL DEFAULT '',
			stats_json TEXT NOT NULL DEFAULT '{}',
			generated_at TEXT DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (cycle_start_date, mode)
		)`); err != nil {
		return fmt.Errorf("create lotto_recommendation_new: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO lotto_recommendation_new
			(cycle_start_date, mode, based_on_drw_no, based_on_data_hash, numbers, number_groups, stats_json, generated_at)
		SELECT cycle_start_date, mode, based_on_drw_no, based_on_data_hash, numbers, number_groups, stats_json, generated_at
		FROM lotto_recommendation`); err != nil {
		return fmt.Errorf("copy lotto_recommendation rows: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE lotto_recommendation`); err != nil {
		return fmt.Errorf("drop old lotto_recommendation: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE lotto_recommendation_new RENAME TO lotto_recommendation`); err != nil {
		return fmt.Errorf("rename lotto_recommendation_new: %w", err)
	}

	return tx.Commit()
}

// ensureColumnExists는 `table`에 `column`이 없으면 alterColumnDDL(예:
// "based_on_drw_no INTEGER NOT NULL DEFAULT 0")로 ALTER TABLE ADD COLUMN을
// 실행한다. 이미 배포된 Turso 테이블에는 CREATE TABLE IF NOT EXISTS만으로는
// 나중에 스키마에 추가된 컬럼이 반영되지 않으므로 쓰는 공용 헬퍼다 —
// PRAGMA table_info로 먼저 존재 여부를 확인해서, 이미 컬럼이 있는
// 데이터베이스(신규 로컬 DB 포함)에 다시 실행해도 안전하다(ALTER TABLE
// ADD COLUMN을 중복 실행하면 에러가 나므로). table/column은 항상 이
// 파일 안의 상수 문자열만 넘어오므로 SQL 인젝션 우려 없이 그대로
// Sprintf에 끼워 넣는다.
func ensureColumnExists(conn *sql.DB, table, column, alterColumnDDL string) error {
	rows, err := conn.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return fmt.Errorf("read %s schema: %w", table, err)
	}
	defer rows.Close()

	hasColumn := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan %s schema: %w", table, err)
		}
		if name == column {
			hasColumn = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s schema: %w", table, err)
	}
	if hasColumn {
		return nil
	}

	if _, err := conn.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s`, table, alterColumnDDL)); err != nil {
		return fmt.Errorf("add %s.%s column: %w", table, column, err)
	}
	return nil
}

// weather_slot_cache는 기상청 getVilageFcst가 당일 이미 지난 시각에 대해
// 더 이상 값을 반환하지 않게 된 후에도 예보 슬롯(08:00/14:00)을 보존한다
// (weather_slot_cache.go 참고) — (city, slot_date, slot_time)을 키로 하여
// 도시/날짜가 겹치는 일이 없도록 한다. description은 weather_code로부터
// 결정되는 순수한 함수 값이므로(weathercodeDescription이 이를 도출함)
// 따로 저장하지 않으며, 그래서 서로 어긋날 여지도 없다.
// source는 이 슬롯 값이 제때(그 시각이 지나기 전에) 확보된 것인지
// ('observed') 아니면 이미 지난 뒤 단기예보(getVilageFcst)를 그 시각
// 이전 발표분으로 소급 조회해서 복구한 것인지('forecast')를 구분한다 —
// models.go의 PeriodForecast.Source 문서 주석 참고. MySQL의 ENUM은
// SQLite/libSQL에 없으므로 TEXT + CHECK 제약으로 옮긴다.
// updated_at의 "ON UPDATE CURRENT_TIMESTAMP"도 SQLite 문법에는 없다 —
// 대신 upsertWeatherSlot의 ON CONFLICT ... DO UPDATE SET 절에서
// updated_at = CURRENT_TIMESTAMP를 명시적으로 지정해 같은 효과를 낸다.
const createWeatherSlotCacheTable = `
CREATE TABLE IF NOT EXISTS weather_slot_cache (
	city TEXT NOT NULL,
	slot_date TEXT NOT NULL,
	slot_time TEXT NOT NULL,
	temperature REAL NOT NULL,
	weather_code INTEGER NOT NULL,
	precipitation_probability INTEGER NOT NULL,
	source TEXT NOT NULL DEFAULT 'observed' CHECK (source IN ('observed', 'forecast')),
	updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (city, slot_date, slot_time)
)`

// deleteOldWeatherSlotCache는 매 요청이 아니라 시작 시 한 번만 실행된다 —
// 일주일치 (도시 × 하루 4슬롯) 행 수는 매우 적으므로, 이는 정확성이나
// 성능을 위한 것이 아니라 그저 정리 차원일 뿐이다. slot_date는
// "YYYY-MM-DD" ISO8601 형식의 TEXT라서 date('now', '-7 days')가 만드는
// 같은 형식의 문자열과 사전식(lexicographic) 비교를 해도 날짜 순서와
// 정확히 일치한다.
const deleteOldWeatherSlotCache = `
DELETE FROM weather_slot_cache WHERE slot_date < date('now', '-7 days')`

// raw_data_cache는 날씨/환율/뉴스 "원본" API 응답 전체를 JSON 문자열
// 그대로 저장한다(raw_data_cache.go 참고) — 예전에는 프로세스 메모리
// TTL 캐시였는데, Render 무료 티어처럼 인스턴스가 잠들었다 재시작되면
// 메모리가 초기화돼 캐시도 함께 사라졌다. cache_key 예시:
// "weather:daegu", "exchange:USD:KRW", "news:domestic:top" — 세 데이터
// 종류가 값 하나의 테이블을 공유하지만 키 접두사로 절대 섞이지 않는다.
const createRawDataCacheTable = `
CREATE TABLE IF NOT EXISTS raw_data_cache (
	cache_key TEXT PRIMARY KEY,
	data_json TEXT NOT NULL,
	fetched_at TEXT DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL
)`

// news_translation_cache는 NewsData.io의 article_id를 그 한국어 번역
// 제목에 매핑한다(news_translation.go 참고) — 예전에는 프로세스 메모리
// map이었지만(재시작되면 사라져도 몇 시간 안에 헤드라인 자체가 바뀌니
// 큰 문제는 아니었다), 다른 캐시들과 마찬가지로 DB에 옮겨두면 서버가
// 재시작돼도 같은 기사에 대해 다시 Groq를 호출하지 않는다.
const createNewsTranslationCacheTable = `
CREATE TABLE IF NOT EXISTS news_translation_cache (
	article_id TEXT PRIMARY KEY,
	translated_title TEXT NOT NULL,
	cached_at TEXT DEFAULT CURRENT_TIMESTAMP
)`

// deleteOldNewsTranslationCache는 weather_slot_cache와 같은 이유로 시작
// 시 한 번만 실행되는 정리용 삭제다 — raw_data_cache와 달리 이 테이블은
// 만료 시각(expires_at)이 없는 순수 append-only 캐시라서, 이 정리가
// 없으면 기사 id가 무한히 쌓이기만 한다. 뉴스 헤드라인은 하루 이틀
// 안에 사실상 전부 교체되므로 30일이면 재사용 가능성이 있는 기간을
// 충분히 넉넉하게 잡은 것이다.
const deleteOldNewsTranslationCache = `
DELETE FROM news_translation_cache WHERE cached_at < datetime('now', '-30 days')`

// deleteEmptyNewsTranslationCache는 일회성 정리 쿼리다. news_translation.go가
// 예전에는 검증 실패(CJK/영어 혼입) 항목을 빈 문자열("")로 캐시에 그대로
// 저장했는데, 그러면 이후 lookupNewsTranslation이 "행이 존재하니 캐시
// 성공"으로 잘못 판단해서 해당 기사가 노출되는 동안 계속 "번역 실패"(원문
// 표시)로 고정되는 문제가 있었다. translateNewsItems가 이제는 빈 결과를
// 캐시에 쓰지 않도록 고쳐졌으니, 이 마이그레이션이 배포되는 순간 이미
// 박혀 있던 빈 문자열 행들을 지워서 다음 요청부터 새 쿨다운 로직으로
// 재시도되게 한다. CREATE TABLE IF NOT EXISTS처럼 매 시작마다 실행해도
// 안전하다 — 지울 빈 문자열 행이 없으면 그냥 0행 삭제로 끝난다.
const deleteEmptyNewsTranslationCache = `
DELETE FROM news_translation_cache WHERE translated_title = ''`

// migrate는 로또/브리핑/캐시 관련 테이블이 없으면 생성한다. 매 시작마다
// 실행해도 안전하다(CREATE TABLE IF NOT EXISTS). MySQL 시절에 있던
// widenBriefingSectionCacheColumn/makeSimpleTextNullable/
// addWeatherSlotCacheSourceColumn 같은 ALTER 기반 일회성 마이그레이션은
// 여기 없다 — 그 마이그레이션들은 모두 "이미 배포된 MySQL 테이블을
// 나중에 바뀐 스키마에 맞게 조정"하기 위한 것이었는데, Turso로 넘어오며
// 완전히 새 데이터베이스에서 시작하므로 CREATE TABLE 자체가 이미 최종
// 형태를 담고 있어 그런 조정이 필요 없다.
func migrate(conn *sql.DB) error {
	if _, err := conn.Exec(createLottoDrawsTable); err != nil {
		return fmt.Errorf("create lotto_draws: %w", err)
	}
	if _, err := conn.Exec(createAIInsightCacheTable); err != nil {
		return fmt.Errorf("create ai_insight_cache: %w", err)
	}
	if err := ensureColumnExists(conn, "ai_insight_cache", "data_hash", "data_hash TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate ai_insight_cache: %w", err)
	}
	// 기존 행은 이 ALTER로 prompt_version = ''을 갖게 되는데, 이는
	// lotto_ai.go의 insightPromptVersion(현재 "v3")과 항상 다르므로
	// system prompt를 바꾼 뒤 처음 배포될 때 기존 캐시가 자동으로
	// 무효화되어 재생성된다(수동으로 캐시 행을 지울 필요가 없다).
	if err := ensureColumnExists(conn, "ai_insight_cache", "prompt_version", "prompt_version TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate ai_insight_cache: %w", err)
	}
	if _, err := conn.Exec(createBriefingSectionCacheTable); err != nil {
		return fmt.Errorf("create briefing_section_cache: %w", err)
	}
	if err := ensureColumnExists(conn, "briefing_section_cache", "is_fallback", "is_fallback INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate briefing_section_cache: %w", err)
	}
	if _, err := conn.Exec(createLottoRecommendationTable); err != nil {
		return fmt.Errorf("create lotto_recommendation: %w", err)
	}
	if err := ensureColumnExists(conn, "lotto_recommendation", "based_on_drw_no", "based_on_drw_no INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate lotto_recommendation: %w", err)
	}
	if err := ensureColumnExists(conn, "lotto_recommendation", "based_on_data_hash", "based_on_data_hash TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate lotto_recommendation: %w", err)
	}
	if err := ensureColumnExists(conn, "lotto_recommendation", "mode", "mode TEXT NOT NULL DEFAULT 'uniform'"); err != nil {
		return fmt.Errorf("migrate lotto_recommendation: %w", err)
	}
	if err := ensureColumnExists(conn, "lotto_recommendation", "stats_json", "stats_json TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("migrate lotto_recommendation: %w", err)
	}
	// 반드시 위 ensureColumnExists 호출들 뒤에 온다 — 이 마이그레이션이
	// 옛 테이블의 모든 컬럼(mode, stats_json 포함)을 새 테이블로 그대로
	// 복사하므로, 그 컬럼들이 이미 존재해야 한다.
	if err := migrateLottoRecommendationToCompositeKey(conn); err != nil {
		return fmt.Errorf("migrate lotto_recommendation to composite key: %w", err)
	}
	// matched_count/matched_numbers/is_retroactive는 이 복합키 마이그레이션이
	// 만드는 lotto_recommendation_new에는 포함되어 있지 않으므로(그
	// 마이그레이션은 그 시점의 예전 컬럼만 그대로 복사한다), 반드시 이
	// 마이그레이션 뒤에 확인해야 레거시 DB에서도 확실히 추가된다.
	if err := ensureColumnExists(conn, "lotto_recommendation", "matched_count", "matched_count INTEGER"); err != nil {
		return fmt.Errorf("migrate lotto_recommendation: %w", err)
	}
	if err := ensureColumnExists(conn, "lotto_recommendation", "matched_numbers", "matched_numbers TEXT"); err != nil {
		return fmt.Errorf("migrate lotto_recommendation: %w", err)
	}
	if err := ensureColumnExists(conn, "lotto_recommendation", "is_retroactive", "is_retroactive INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate lotto_recommendation: %w", err)
	}
	if _, err := conn.Exec(createWeatherSlotCacheTable); err != nil {
		return fmt.Errorf("create weather_slot_cache: %w", err)
	}
	if _, err := conn.Exec(deleteOldWeatherSlotCache); err != nil {
		return fmt.Errorf("clean up old weather_slot_cache rows: %w", err)
	}
	if _, err := conn.Exec(createRawDataCacheTable); err != nil {
		return fmt.Errorf("create raw_data_cache: %w", err)
	}
	if _, err := conn.Exec(createNewsTranslationCacheTable); err != nil {
		return fmt.Errorf("create news_translation_cache: %w", err)
	}
	if _, err := conn.Exec(deleteOldNewsTranslationCache); err != nil {
		return fmt.Errorf("clean up old news_translation_cache rows: %w", err)
	}
	if _, err := conn.Exec(deleteEmptyNewsTranslationCache); err != nil {
		return fmt.Errorf("clean up empty news_translation_cache rows: %w", err)
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
