package main

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	dbMaxOpenConns    = 10
	dbMaxIdleConns    = 5
	dbConnMaxLifetime = 3 * time.Minute
)

// connectDB는 DB_* 환경변수로부터 MySQL 커넥션 풀을 연다. DB_USE_TLS는
// 연결 자체를 암호화할지 여부를 제어하는데, 이는 중요하다. 클라우드 MySQL
// 제공업체(Aiven 등)는 대개 평문 연결을 아예 거부하는 반면, 로컬 Docker
// MySQL은 검증할 인증서가 없기 때문이다.
func connectDB() (*sql.DB, error) {
	host := envOrDefault("DB_HOST", "127.0.0.1")
	port := envOrDefault("DB_PORT", "3306")
	user := envOrDefault("DB_USER", "root")
	password := os.Getenv("DB_PASSWORD")
	name := envOrDefault("DB_NAME", "dashboard")

	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%s", host, port)
	cfg.User = user
	cfg.Passwd = password
	cfg.DBName = name
	cfg.ParseTime = true
	cfg.Loc = time.Local
	cfg.Collation = "utf8mb4_general_ci"

	if os.Getenv("DB_USE_TLS") == "true" {
		const tlsConfigName = "dashboard"
		tlsConfig := &tls.Config{ServerName: host}

		if caPath := os.Getenv("DB_CA_CERT_PATH"); caPath != "" {
			caCert, err := os.ReadFile(caPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read DB_CA_CERT_PATH: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse CA certificate at %s", caPath)
			}
			tlsConfig.RootCAs = pool
		} else {
			// CA 인증서가 제공되지 않은 경우: 연결은 암호화하되 서버 인증서 검증은
			// 건너뛴다. 이 프로젝트 규모에서는 이 정도로 충분하다; 완전히 검증된
			// 연결을 원한다면 DB_CA_CERT_PATH를 설정할 것(Aiven 콘솔에서 내려받을 수
			// 있는 CA 인증서).
			log.Println("경고: DB_USE_TLS=true 이지만 DB_CA_CERT_PATH가 없어 서버 인증서 검증 없이 암호화만 적용합니다")
			tlsConfig.InsecureSkipVerify = true
		}

		if err := mysql.RegisterTLSConfig(tlsConfigName, tlsConfig); err != nil {
			return nil, fmt.Errorf("failed to register TLS config: %w", err)
		}
		cfg.TLSConfig = tlsConfigName
	}

	conn, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}

	// 클라우드 MySQL 제공업체는 유휴 연결을 몇 분 후에 끊어버리는 경우가 많아,
	// 다음 쿼리에서 broken-pipe 에러가 발생하기 전에 미리 커넥션 풀이 연결을
	// 재활용하도록 한다.
	conn.SetMaxOpenConns(dbMaxOpenConns)
	conn.SetMaxIdleConns(dbMaxIdleConns)
	conn.SetConnMaxLifetime(dbConnMaxLifetime)

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

const createLottoDrawsTable = `
CREATE TABLE IF NOT EXISTS lotto_draws (
	drw_no INT PRIMARY KEY,
	drw_date DATE NOT NULL,
	num1 TINYINT NOT NULL,
	num2 TINYINT NOT NULL,
	num3 TINYINT NOT NULL,
	num4 TINYINT NOT NULL,
	num5 TINYINT NOT NULL,
	num6 TINYINT NOT NULL,
	bonus_no TINYINT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// insight_text가 utf8mb4인 이유는 AI 인사이트가 한글 텍스트이기 때문이다 —
// 일부 MySQL 환경은 서버/데이터베이스 기본 charset이 latin1이라, 멀티바이트
// insert를 값이 깨지는 정도가 아니라 아예 거부해버린다.
const createAIInsightCacheTable = `
CREATE TABLE IF NOT EXISTS ai_insight_cache (
	latest_drw_no INT PRIMARY KEY,
	insight_text TEXT NOT NULL,
	generated_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// section은 'weather'/'exchange'/'news:{region}:{category}' 값을 가지며
// 기본키이므로, 섹션마다 정확히 한 행만 유지된다 — 가장 최근에 생성된
// 텍스트와, 그 생성에 쓰인 입력값의 해시를 함께 담는다. news는 weather/
// exchange처럼 단순히 "news"가 아니라 카테고리와 지역에 따라 달라지므로
// (briefing.go의 newsBriefingCacheKey 참고), 고정된 섹션명보다 더 넉넉한
// 길이가 필요해서 VARCHAR(20)이 아니라 VARCHAR(50)을 쓴다. text가 utf8mb4인
// 이유는 ai_insight_cache와 동일하다: latin1을 기본값으로 쓰는 서버에서는
// 한글 텍스트를 위해 명시적으로 지정해야 한다.
// simple_text는 더 이상 쓰이지 않는다 — 브리핑이 simple(1문장)/detailed
// (1~2문장) 두 버전 대신 detailed 하나만 생성하도록 단순화됐다(Groq 출력
// 토큰 절감 목적). 컬럼 자체는 기존 배포와의 호환을 위해 남겨두되(굳이
// DROP COLUMN까지 할 필요는 없다), NOT NULL이면 새로 값을 안 채워도 되게
// nullable로 둔다 — makeSimpleTextNullable 참고.
const createBriefingSectionCacheTable = `
CREATE TABLE IF NOT EXISTS briefing_section_cache (
	section VARCHAR(50) PRIMARY KEY,
	data_hash VARCHAR(64) NOT NULL,
	simple_text TEXT NULL,
	detailed_text TEXT NOT NULL,
	generated_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// widenBriefingSectionCacheColumn은 news 캐시 키가 복합 형태로 커지기
// 전에(section VARCHAR(20)로는 "news:international:entertainment"를 담을
// 수 없었음) briefing_section_cache를 생성한 설치본을 위한 일회성 ALTER다.
// MODIFY COLUMN은 매 시작마다 실행해도 안전하다 — 컬럼이 이미 VARCHAR(50)이면
// 아무 일도 하지 않는다.
const widenBriefingSectionCacheColumn = `
ALTER TABLE briefing_section_cache MODIFY COLUMN section VARCHAR(50) NOT NULL`

// makeSimpleTextNullable은 simple_text가 아직 NOT NULL이던 시절(브리핑이
// simple/detailed 두 버전을 함께 생성하던 때) briefing_section_cache를
// 생성한 기존 설치본을 위한 일회성 ALTER다. MODIFY COLUMN은 매 시작마다
// 실행해도 안전하다 — 컬럼이 이미 NULL 허용이면 아무 일도 하지 않는다.
const makeSimpleTextNullable = `
ALTER TABLE briefing_section_cache MODIFY COLUMN simple_text TEXT NULL`

// cycle_start_date는 "이번 주" 추천을 식별하는 일요일 06:00 KST 값이며
// (lotto_recommendation.go 참고) 기본키다 — 사이클마다 정확히 한 행만
// 존재하고, 같은 사이클 안에서 다시 요청하면 새 번호를 생성하는 대신 같은
// 행을 그대로 읽어온다.
//
// number_groups는 원래 요구사항에는 없었지만, 캐시 히트 시 그룹 배지
// (🔥/⚖️/❄️)를 올바르게 표시하려면 필요하다: 각 번호가 어느 그룹에서
// 뽑혔는지를 저장해두지 않으면, 그 정보는 생성 요청이 끝나는 순간 사라져
// 버리고, 나중에 그 시점의 빈도 데이터로 다시 유추하면 번호가 처음
// 생성되었을 때 실제로 보여준 내용과 달라질 수 있다.
const createLottoRecommendationTable = `
CREATE TABLE IF NOT EXISTS lotto_recommendation (
	cycle_start_date DATE PRIMARY KEY,
	numbers VARCHAR(20) NOT NULL,
	number_groups VARCHAR(30) NOT NULL,
	generated_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// weather_slot_cache는 기상청 getVilageFcst가 당일 이미 지난 시각에 대해
// 더 이상 값을 반환하지 않게 된 후에도 예보 슬롯(08:00/14:00)을 보존한다
// (weather_slot_cache.go 참고) — (city, slot_date, slot_time)을 키로 하여
// 도시/날짜가 겹치는 일이 없도록 한다. description은 weather_code로부터
// 결정되는 순수한 함수 값이므로(weathercodeDescription이 이를 도출함)
// 따로 저장하지 않으며, 그래서 서로 어긋날 여지도 없다.
const createWeatherSlotCacheTable = `
CREATE TABLE IF NOT EXISTS weather_slot_cache (
	city VARCHAR(20) NOT NULL,
	slot_date DATE NOT NULL,
	slot_time VARCHAR(5) NOT NULL,
	temperature FLOAT NOT NULL,
	weather_code INT NOT NULL,
	precipitation_probability INT NOT NULL,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
	PRIMARY KEY (city, slot_date, slot_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// deleteOldWeatherSlotCache는 매 요청이 아니라 시작 시 한 번만 실행된다 —
// 일주일치 (도시 × 하루 4슬롯) 행 수는 매우 적으므로, 이는 정확성이나
// 성능을 위한 것이 아니라 그저 정리 차원일 뿐이다.
const deleteOldWeatherSlotCache = `
DELETE FROM weather_slot_cache WHERE slot_date < DATE_SUB(CURDATE(), INTERVAL 7 DAY)`

// raw_data_cache는 날씨/환율/뉴스 "원본" API 응답 전체를 JSON 문자열
// 그대로 저장한다(raw_data_cache.go 참고) — 예전에는 프로세스 메모리
// TTL 캐시였는데, Render 무료 티어처럼 인스턴스가 잠들었다 재시작되면
// 메모리가 초기화돼 캐시도 함께 사라졌다. cache_key 예시:
// "weather:daegu", "exchange:USD:KRW", "news:domestic:top" — 세 데이터
// 종류가 값 하나의 테이블을 공유하지만 키 접두사로 절대 섞이지 않는다.
const createRawDataCacheTable = `
CREATE TABLE IF NOT EXISTS raw_data_cache (
	cache_key VARCHAR(100) PRIMARY KEY,
	data_json TEXT NOT NULL,
	fetched_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	expires_at DATETIME NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// migrate는 lotto/briefing 관련 테이블이 없으면 생성한다.
// 매 시작마다 실행해도 안전하다.
func migrate(conn *sql.DB) error {
	if _, err := conn.Exec(createLottoDrawsTable); err != nil {
		return fmt.Errorf("create lotto_draws: %w", err)
	}
	if _, err := conn.Exec(createAIInsightCacheTable); err != nil {
		return fmt.Errorf("create ai_insight_cache: %w", err)
	}
	if _, err := conn.Exec(createBriefingSectionCacheTable); err != nil {
		return fmt.Errorf("create briefing_section_cache: %w", err)
	}
	if _, err := conn.Exec(widenBriefingSectionCacheColumn); err != nil {
		return fmt.Errorf("widen briefing_section_cache.section: %w", err)
	}
	if _, err := conn.Exec(makeSimpleTextNullable); err != nil {
		return fmt.Errorf("make briefing_section_cache.simple_text nullable: %w", err)
	}
	if _, err := conn.Exec(createLottoRecommendationTable); err != nil {
		return fmt.Errorf("create lotto_recommendation: %w", err)
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
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
