package main

import (
	"bufio"
	"database/sql"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
)

//go:embed static
var embeddedStatic embed.FS

// db는 로또/캐시 테이블에서 사용하는, 프로세스 전체에서 공유하는 DB(Turso/
// libSQL, 로컬 개발 시에는 파일 기반 libSQL) 커넥션 풀이다. 시작 시 DB에
// 연결할 수 없으면 nil로 남겨두어 나머지 대시보드(날씨/환율/뉴스/브리핑)는
// 그와 상관없이 계속 동작하도록 한다 — lottoHandler는 nil 여부를 확인해서
// 로또 섹션만 실패로 보고하고, 원본 데이터/브리핑/뉴스 번역 캐시들은 db가
// nil이면 그냥 캐싱 없이(매번 다시 계산/호출하며) 동작한다.
var db *sql.DB

func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}

// healthzHandler는 프로세스가 요청을 받아 응답할 수 있는 상태인지만
// 확인한다 — DB, dhlottery, Groq, NewsData.io 등 어떤 외부 의존성도
// 건드리지 않는다. 그런 의존성이 죽었다고 해서 로드밸런서/플랫폼이
// 정상적으로 살아있는 프로세스를 재시작하는 것을 원치 않기 때문이다.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func newStaticHandler() http.Handler {
	staticFS, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		log.Fatalf("failed to load embedded static files: %v", err)
	}
	return http.FileServer(http.FS(staticFS))
}

// loadDotEnv는 .env 파일의 KEY=VALUE 줄을 읽어서 프로세스 환경변수로
// 설정한다. 실제 환경변수가 이미 설정되어 있으면 파일 내용보다 항상
// 우선한다.
func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)

		if _, alreadySet := os.LookupEnv(key); !alreadySet {
			os.Setenv(key, value)
		}
	}
}

func main() {
	loadDotEnv(".env")

	if conn, err := connectDB(); err != nil {
		log.Printf("경고: 데이터베이스 연결 실패, 로또 섹션은 비활성화됩니다: %v", err)
	} else if err := migrate(conn); err != nil {
		log.Printf("경고: 테이블 마이그레이션 실패, 로또 섹션은 비활성화됩니다: %v", err)
		conn.Close()
	} else {
		db = conn
		log.Println("데이터베이스 연결 및 마이그레이션 완료")
		// 로또 수집은 더 이상 서버 시작 시 자동으로 걸리지 않는다 — 화면의
		// ON/OFF 토글이 POST /api/lotto/collection/start를 통해 명시적으로
		// 시작해야 한다(기본값은 꺼짐).
	}

	mux := http.NewServeMux()

	// /healthz는 DB 연결이나 외부 API 호출 없이 곧바로 200을 반환한다 —
	// Render 등 플랫폼의 헬스체크가 "프로세스가 요청을 받을 수 있는
	// 상태인지"만 판단하도록, 다른 엔드포인트처럼 외부 의존성(DB, dhlottery,
	// Groq 등)의 상태에 좌우되지 않게 하기 위함이다.
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/api/dashboard", withCORS(dashboardHandler))
	mux.HandleFunc("/api/news", withCORS(newsHandler))
	mux.HandleFunc("/api/lotto", withCORS(lottoHandler))
	mux.HandleFunc("/api/lotto/collection/start", withCORS(lottoCollectionStartHandler))
	mux.HandleFunc("/api/lotto/collection/stop", withCORS(lottoCollectionStopHandler))
	mux.HandleFunc("/api/lotto/collection/status", withCORS(lottoCollectionStatusHandler))
	mux.HandleFunc("/api/debug/groq-usage", withCORS(groqUsageHandler))
	mux.Handle("/", newStaticHandler())

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if os.Getenv("GROQ_API_KEY") == "" {
		log.Println("경고: GROQ_API_KEY 환경변수가 설정되지 않았습니다. AI 브리핑/로또 인사이트/해외 뉴스 번역이 비활성화됩니다.")
	}
	if os.Getenv("NEWSDATA_API_KEY") == "" {
		log.Println("경고: NEWSDATA_API_KEY 환경변수가 설정되지 않았습니다. 뉴스 섹션은 비활성화됩니다.")
	}
	if os.Getenv("KMA_SERVICE_KEY") == "" {
		log.Println("정보: KMA_SERVICE_KEY 환경변수가 설정되지 않았습니다. 국내 도시 날씨도 Open-Meteo로 조회합니다.")
	}

	log.Printf("서버 시작: http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
