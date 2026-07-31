package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 로또는 1회차부터 예외 없이 매주 토요일에 추첨해왔으므로, "회차 번호"와
// "날짜"는 단순 산술 연산만으로 서로 변환할 수 있다.
var kst = mustLoadLocation("Asia/Seoul")

var lottoFirstDrawDate = time.Date(2002, 12, 7, 0, 0, 0, 0, kst)

const (
	lottoHistoryWindow           = 50                     // 화면에 표시/집계하는 회차 수
	lottoRecentWindow            = 10                     // "최근 출현"에 사용하는 회차 수
	lottoFetchConcurrencyDefault = 2                      // dhlottery API 동시 호출 상한 기본값 — 짧은 시간에 요청이 몰리면 dhlottery가 이후 요청을 응답 없이 드롭하는 것으로 보여, 사실상 순차에 가깝게 대폭 낮췄다 (LOTTO_FETCH_CONCURRENCY로 조정 가능)
	lottoDrawHourKST             = 21                     // 추첨은 KST 20:45경 진행되며, 그 다음 시각부터 "완료"로 간주
	lottoFetchTimeout            = 8 * time.Second        // 개별 회차 조회 1회 시도당 타임아웃
	lottoRequestInterval         = 400 * time.Millisecond // 회차 요청 사이 전역 최소 간격 — 사람이 브라우저로 하나씩 조회하는 속도에 가깝게 맞춰 dhlottery의 차단 로직을 피한다
	lottoCollectionPassInterval  = 30 * time.Second       // 한 pass에서 일부 회차가 실패해 남았을 때, 다음 pass 전에 쉬는 시간

	// dhlottery가 기본 Go 클라이언트의 User-Agent("Go-http-client/...")를
	// 보고 봇으로 판단해 차단할 가능성을 줄이기 위해, 일반 브라우저처럼
	// 보이는 값을 명시적으로 지정한다.
	lottoUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// lottoFetchBackoffSchedule은 회차 조회 실패 시 재시도 전 대기 시간이다.
// 실패 직후 곧바로(수 초 간격) 다시 두드리면 이미 차단된 상태를 악화시킬
// 수 있으므로, 5초 → 15초 → 40초로 점점 크게 벌린다. 이 스케줄을 모두
// 소진하고도 실패한 회차는 이번 pass에서는 건너뛰고, DB에는 여전히
// "빠진 회차"로 남는다 — runLottoCollectionLoop가 잠시 쉬었다가 다음
// pass에서 computeLottoSyncRange를 통해 자동으로 다시 집어 든다.
var lottoFetchBackoffSchedule = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	40 * time.Second,
}

// lottoHTTPClient는 dhlottery 호출 전용 클라이언트다. Timeout을 명시적으로
// 두어, 혹시라도 컨텍스트 취소가 누락되는 경우에도 개별 호출이 무한정
// 걸리지 않도록 이중으로 방어한다.
var lottoHTTPClient = &http.Client{Timeout: lottoFetchTimeout}

func lottoFetchConcurrencyLimit() int {
	if v := os.Getenv("LOTTO_FETCH_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return lottoFetchConcurrencyDefault
}

// lottoRequestPacer는 세마포어(동시 실행 개수)와는 별개로, dhlottery로 나가는
// 모든 요청(재시도 포함)이 전역적으로 최소 lottoRequestInterval만큼 간격을
// 두고 시작하도록 강제한다. 동시성 설정이 2 이상이어도 실제 "초당 요청 수"는
// 이 페이서가 직접 통제한다.
var lottoRequestPacer struct {
	mu   sync.Mutex
	next time.Time
}

func lottoWaitForTurn(ctx context.Context) error {
	lottoRequestPacer.mu.Lock()
	now := time.Now()
	start := lottoRequestPacer.next
	if start.Before(now) {
		start = now
	}
	lottoRequestPacer.next = start.Add(lottoRequestInterval)
	lottoRequestPacer.mu.Unlock()

	wait := time.Until(start)
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// lottoCollectionState는 로또 수집 goroutine의 실행 상태를 서버 메모리에
// 둔다 — 예전의 환경변수(LOTTO_ENABLED) 기반 on/off 대신, 화면의 토글
// 버튼이 POST /api/lotto/collection/{start,stop}으로 이 상태를 직접
// 제어한다. cancel은 실행 중일 때만 설정되며, STOP은 이 cancel을 호출해
// 진행 중이던 개별 회차 요청은 마무리하고(각 요청은 이미 자기 몫의
// 짧은 타임아웃을 가지므로 곧 끝난다) 다음 회차부터는 시작하지 않게
// 한다. 서버 시작 시 기본값은 꺼짐(running=false)이다 — main.go는 더
// 이상 시작 시점에 수집을 자동으로 걸지 않는다.
var lottoCollectionState struct {
	mu              sync.Mutex
	running         bool
	cancel          context.CancelFunc
	lastCollectedAt time.Time // 마지막으로 회차 하나를 성공적으로 저장한 시각
}

func lottoIsCollecting() bool {
	lottoCollectionState.mu.Lock()
	defer lottoCollectionState.mu.Unlock()
	return lottoCollectionState.running
}

// lottoStartCollection은 이미 실행 중이 아니라면 백그라운드 goroutine에서
// runLottoCollectionLoop를 시작하고 true를 반환한다. 이미 실행 중이면
// 아무 것도 하지 않고 false를 반환한다(POST /start를 중복으로 눌러도
// 안전). 이 goroutine은 context.Background()에서 파생된, 이를 호출한
// HTTP 요청과는 완전히 무관한 취소 가능(cancellable) 컨텍스트를 쓰므로,
// 이 요청이 끝나거나 타임아웃되어도 수집 자체는 STOP을 누르거나 더 이상
// 채울 회차가 없을 때까지 계속된다.
func lottoStartCollection(conn *sql.DB) bool {
	lottoCollectionState.mu.Lock()
	if lottoCollectionState.running {
		lottoCollectionState.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	lottoCollectionState.running = true
	lottoCollectionState.cancel = cancel
	lottoCollectionState.mu.Unlock()

	log.Println("로또: 수집 시작 (POST /api/lotto/collection/start)")

	go func() {
		defer func() {
			lottoCollectionState.mu.Lock()
			lottoCollectionState.running = false
			lottoCollectionState.cancel = nil
			lottoCollectionState.mu.Unlock()
		}()

		runLottoCollectionLoop(ctx, conn)
	}()
	return true
}

// lottoStopCollection은 실행 중이면 그 컨텍스트를 취소하고 true를
// 반환한다. 실행 중이 아니면 아무 것도 하지 않고 false를 반환한다.
// 취소 자체는 즉시 반환되지만, running이 실제로 false로 바뀌는 것은
// runLottoCollectionLoop가 ctx.Done()을 알아채고 진행 중이던 회차를
// 마무리한 뒤 goroutine이 종료되는 시점이다(보통 수 초 이내).
func lottoStopCollection() bool {
	lottoCollectionState.mu.Lock()
	defer lottoCollectionState.mu.Unlock()
	if !lottoCollectionState.running || lottoCollectionState.cancel == nil {
		return false
	}
	log.Println("로또: 수집 중단 요청 (POST /api/lotto/collection/stop)")
	lottoCollectionState.cancel()
	return true
}

// LottoCollectionStatus는 GET /api/lotto/collection/status의 응답이다.
type LottoCollectionStatus struct {
	Running         bool   `json:"running"`
	LastCollectedAt string `json:"lastCollectedAt,omitempty"`
	SavedCount      int    `json:"savedCount"`
	WindowSize      int    `json:"windowSize"`
}

// lottoCollectionStatusSnapshot은 현재 실행 상태와, DB에 실제로 저장된
// 회차 수를 함께 반환한다 — 프론트엔드가 "42/50 회차 수집됨" 같은 진행
// 상황을 보여주는 데 쓴다.
func lottoCollectionStatusSnapshot(ctx context.Context, conn *sql.DB) (LottoCollectionStatus, error) {
	lottoCollectionState.mu.Lock()
	running := lottoCollectionState.running
	lastAt := lottoCollectionState.lastCollectedAt
	lottoCollectionState.mu.Unlock()

	status := LottoCollectionStatus{Running: running, WindowSize: lottoHistoryWindow}
	if !lastAt.IsZero() {
		status.LastCollectedAt = lastAt.Format(time.RFC3339)
	}

	if conn == nil {
		return status, nil
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM lotto_draws`).Scan(&status.SavedCount); err != nil {
		return status, fmt.Errorf("count lotto_draws: %w", err)
	}
	return status, nil
}

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("KST", 9*60*60)
	}
	return loc
}

// theoreticalLatestDrwNo는 매주 추첨된다는 주기성만으로 지금 시점에 존재해야
// 할 최신 회차를 추정한다. 이 값은 조회 범위의 상한일 뿐이다: collectLottoRounds는
// dhlottery API 자체의 성공/실패 응답을 실제 기준으로 삼기 때문에, 여기서
// 발생한 오차(예: 추첨 시각 전후)는 잘못된 데이터를 삽입하지 않고 자연스럽게
// 스스로 보정된다.
func theoreticalLatestDrwNo(now time.Time) int {
	now = now.In(kst)
	days := int(now.Sub(lottoFirstDrawDate).Hours() / 24)
	drwNo := days/7 + 1

	if now.Weekday() == time.Saturday && now.Hour() < lottoDrawHourKST {
		drwNo--
	}
	if drwNo < 1 {
		drwNo = 1
	}
	return drwNo
}

type dhlotteryResponse struct {
	ReturnValue string `json:"returnValue"`
	DrwNo       int    `json:"drwNo"`
	DrwNoDate   string `json:"drwNoDate"`
	DrwtNo1     int    `json:"drwtNo1"`
	DrwtNo2     int    `json:"drwtNo2"`
	DrwtNo3     int    `json:"drwtNo3"`
	DrwtNo4     int    `json:"drwtNo4"`
	DrwtNo5     int    `json:"drwtNo5"`
	DrwtNo6     int    `json:"drwtNo6"`
	BnusNo      int    `json:"bnusNo"`
}

func fetchLottoDraw(ctx context.Context, drwNo int) (*dhlotteryResponse, error) {
	// 실제 요청을 보내기 전에 전역 페이서 차례를 기다린다 — 세마포어로 몇 개가
	// "동시에" 도는지와 무관하게, 실제 발신 간격 자체를 사람이 하나씩 조회하는
	// 속도에 가깝게 강제로 벌려 놓기 위함이다.
	if err := lottoWaitForTurn(ctx); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://www.dhlottery.co.kr/common.do?method=getLottoNumber&drwNo=%d", drwNo)

	// 개별 호출은 부모 컨텍스트(수집 전체의 취소 가능한 컨텍스트)와 무관하게,
	// 그 자체로 짧은 lottoFetchTimeout을 갖는다 — 외부 API가
	// 응답을 미루는 경우에도 한 회차 때문에 나머지 회차들의 재시도 기회까지
	// 잡아먹지 않도록 하기 위함이다.
	callCtx, cancel := context.WithTimeout(ctx, lottoFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", lottoUserAgent)

	resp, err := lottoHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dhlottery returned status %d for round %d", resp.StatusCode, drwNo)
	}

	var parsed dhlotteryResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

// fetchLottoDrawWithRetry는 타임아웃을 포함한 일시적 오류에 곧바로 포기하지
// 않고, lottoFetchBackoffSchedule에 따라 점점 길게 대기한 뒤 재시도한다.
// 부모 ctx가 이미 취소된 상태라면(예: STOP 요청) 대기 없이 즉시 중단한다.
// 스케줄을 모두 소진해도 실패하면 이 회차는 이번 pass에서 포기하고 다음
// pass에 다시 맡긴다 — collectLottoRounds/runLottoCollectionLoop 참고.
func fetchLottoDrawWithRetry(ctx context.Context, drwNo int) (*dhlotteryResponse, error) {
	maxAttempts := len(lottoFetchBackoffSchedule) + 1
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := lottoFetchBackoffSchedule[attempt-1]
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		data, err := fetchLottoDraw(ctx, drwNo)
		if err == nil {
			return data, nil
		}
		lastErr = err
		log.Printf("로또: 회차 %d 조회 실패 (%d/%d회 시도): %v", drwNo, attempt+1, maxAttempts, err)
	}
	return nil, lastErr
}

// computeLottoSyncRange는 DB 조회만으로(외부 API 호출 없이) 지금 채워야 할
// 회차 목록을 빠르게 계산한다. 대상 구간은 항상 [theoretical-lottoHistoryWindow+1,
// theoretical]이며, 그 구간 안에서 DB에 "실제로 없는" 회차만 반환한다 —
// 예전에는 MAX(drw_no)+1부터만 봐서 구간이 항상 빈틈없이(contiguous) 채워져
// 있다고 가정했지만, 회차별 개별 저장으로 바뀌면서 일부만 성공하고 일부는
// 실패해 중간에 구멍(gap)이 남는 상황이 정상적으로 발생할 수 있다 — 이
// 함수가 그 구멍까지 정확히 찾아내야 다음 사이클이 그 회차만 다시 시도할 수
// 있다. 이 함수는 순수 DB 쿼리라 요청 스코프의 짧은 컨텍스트로 호출해도
// 안전하다.
func computeLottoSyncRange(ctx context.Context, conn *sql.DB) (toFetch []int, alreadyCount int, err error) {
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM lotto_draws`).Scan(&alreadyCount); err != nil {
		return nil, 0, fmt.Errorf("count lotto_draws: %w", err)
	}

	theoretical := theoreticalLatestDrwNo(time.Now())
	windowStart := theoretical - lottoHistoryWindow + 1
	if windowStart < 1 {
		windowStart = 1
	}

	rows, err := conn.QueryContext(ctx, `SELECT drw_no FROM lotto_draws WHERE drw_no >= ? AND drw_no <= ?`, windowStart, theoretical)
	if err != nil {
		return nil, alreadyCount, fmt.Errorf("query existing drw_no in range: %w", err)
	}
	defer rows.Close()

	existing := make(map[int]bool, theoretical-windowStart+1)
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, alreadyCount, err
		}
		existing[n] = true
	}
	if err := rows.Err(); err != nil {
		return nil, alreadyCount, err
	}

	toFetch = make([]int, 0, theoretical-windowStart+1)
	for n := windowStart; n <= theoretical; n++ {
		if !existing[n] {
			toFetch = append(toFetch, n)
		}
	}
	return toFetch, alreadyCount, nil
}

// lottoInsertTimeout은 회차 하나를 저장하는 INSERT 자체의 타임아웃이다.
// context.Background()에서 독립적으로 파생시키므로(아래 collectLottoRounds
// 참고), 수집 전체의 ctx가 마침 그 순간 취소되더라도(STOP 요청 등) 이미
// 성공적으로 조회해둔 데이터를 저장하는 마지막 한 단계는 항상 자기 몫의
// 시간을 보장받는다.
const lottoInsertTimeout = 5 * time.Second

// runLottoCollectionLoop는 lottoStartCollection이 시작한 백그라운드
// goroutine의 본체다. 빠진 회차가 더 이상 없을 때까지(자동 완료) 또는
// ctx가 취소될 때까지(STOP 요청) 계속 pass를 반복한다 — 한 pass에서 일부
// 회차가 재시도를 모두 소진하고도 실패하면, 잠시(lottoCollectionPassInterval)
// 쉬었다가 computeLottoSyncRange로 다시 확인해 그 회차들을 처음부터 다시
// 시도한다.
func runLottoCollectionLoop(ctx context.Context, conn *sql.DB) {
	for {
		toFetch, count, err := computeLottoSyncRange(ctx, conn)
		if err != nil {
			log.Printf("로또: 동기화 범위 계산 실패, 수집 중단: %v", err)
			return
		}
		if len(toFetch) == 0 {
			log.Printf("로또: 이미 저장된 회차 %d개, 신규 회차 없음 — 수집 완료로 자동 종료", count)
			return
		}

		total := len(toFetch)
		log.Printf("로또: 이미 저장된 회차 %d개, 신규 %d개 수집 시작", count, total)
		inserted, failed := collectLottoRounds(ctx, conn, toFetch)
		log.Printf("로또: 이번 pass에서 %d개 중 %d개 저장 완료, %d개 실패", total, inserted, failed)

		if ctx.Err() != nil {
			log.Println("로또: 수집이 중단 요청으로 종료됨")
			return
		}

		if failed > 0 {
			select {
			case <-ctx.Done():
				log.Println("로또: 수집이 중단 요청으로 종료됨")
				return
			case <-time.After(lottoCollectionPassInterval):
			}
		}
	}
}

// collectLottoRounds는 toFetch에 있는 회차들을 병렬로(세마포어/페이서로
// 속도 제한) 조회하고, 각 회차는 서로 완전히 독립적으로 처리된다 — 조회에
// 성공하는 즉시 그 회차 하나만 곧바로 저장하며, 다른 회차들이 아직
// 진행 중이거나 실패하는 것과는 무관하다. 예전에는 전체 회차의 조회가 다
// 끝난 뒤에야 한꺼번에 저장했는데, 그러면 전체 배치가 취소될 때 이미
// 성공적으로 조회해둔 회차까지 함께 저장하지 못한 채 유실됐다(그 시점엔
// 배치의 ctx 자체가 이미 만료된 상태라 저장 시도 자체가 실패했다) —
// "신규 50개 회차 중 0개 저장 완료" 로그가 그 증상이었다.
func collectLottoRounds(ctx context.Context, conn *sql.DB, toFetch []int) (inserted, failed int) {
	total := len(toFetch)
	sem := make(chan struct{}, lottoFetchConcurrencyLimit())
	var wg sync.WaitGroup
	var completed, insertedCount, failedCount atomic.Int32

	for _, drwNo := range toFetch {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := fetchLottoDrawWithRetry(ctx, n)

			done := completed.Add(1)
			if done%5 == 0 || int(done) == total {
				log.Printf("로또: %d/%d 회차 처리 진행", done, total)
			}

			if err != nil {
				log.Printf("로또: 회차 %d 최종 조회 실패(재시도 소진): %v", n, err)
				failedCount.Add(1)
				return
			}
			if data.ReturnValue != "success" {
				// 아직 추첨되지 않은 회차 — 이론상 추정치가 실제보다 앞서
				// 나간 경우다(예: 이번 주 추첨 직전). 실패로 세지 않는다 —
				// 다음 pass에 이론상 최신 회차가 갱신되면 자연히 다시
				// 확인된다.
				return
			}

			insertCtx, cancel := context.WithTimeout(context.Background(), lottoInsertTimeout)
			insertErr := insertLottoDraw(insertCtx, conn, data)
			cancel()
			if insertErr != nil {
				log.Printf("로또: 회차 %d 저장 실패: %v", n, insertErr)
				failedCount.Add(1)
				return
			}

			insertedCount.Add(1)
			log.Printf("로또: 회차 %d 저장 완료", n)

			lottoCollectionState.mu.Lock()
			lottoCollectionState.lastCollectedAt = time.Now()
			lottoCollectionState.mu.Unlock()
		}(drwNo)
	}
	wg.Wait()

	return int(insertedCount.Load()), int(failedCount.Load())
}

// insertLottoDraw는 drwNoDate를 time.Parse로 한 번 검증만 하고(형식이
// 잘못된 응답을 조용히 저장하지 않기 위해), 실제 바인딩에는 재파싱한
// time.Time이 아니라 원본 문자열을 그대로 쓴다 — go-libsql은 bind
// 파라미터가 time.Time이면 RFC3339Nano로 재포맷해서 저장하므로(예:
// "2002-12-07"이 "2002-12-07T00:00:00Z"가 됨), 원본 문자열을 그대로
// 바인딩해야 drw_date가 깔끔한 "YYYY-MM-DD" 형태로 저장된다.
//
// ON CONFLICT는 DO NOTHING이다(MySQL 시절의 `ON DUPLICATE KEY UPDATE
// drw_date = drw_date`도 사실상 아무것도 갱신하지 않는 no-op이었다) —
// 한 번 저장된 회차의 당첨 번호는 절대 바뀌지 않으므로, 이미 있는
// drw_no로 다시 들어오면 그냥 무시한다.
func insertLottoDraw(ctx context.Context, conn *sql.DB, d *dhlotteryResponse) error {
	if _, err := time.Parse("2006-01-02", d.DrwNoDate); err != nil {
		return fmt.Errorf("parse drwNoDate %q: %w", d.DrwNoDate, err)
	}

	_, err := conn.ExecContext(ctx, `
		INSERT INTO lotto_draws (drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(drw_no) DO NOTHING`,
		d.DrwNo, d.DrwNoDate, d.DrwtNo1, d.DrwtNo2, d.DrwtNo3, d.DrwtNo4, d.DrwtNo5, d.DrwtNo6, d.BnusNo,
	)
	return err
}

func queryLottoHistory(ctx context.Context, conn *sql.DB, limit int) ([]LottoDraw, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no
		FROM lotto_draws ORDER BY drw_no DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []LottoDraw
	for rows.Next() {
		var d LottoDraw
		var drwDate time.Time
		var n1, n2, n3, n4, n5, n6, bonus int

		if err := rows.Scan(&d.DrwNo, &drwDate, &n1, &n2, &n3, &n4, &n5, &n6, &bonus); err != nil {
			return nil, err
		}

		d.DrwDate = drwDate.Format("2006-01-02")
		d.Numbers = []int{n1, n2, n3, n4, n5, n6}
		d.Bonus = bonus
		result = append(result, d)
	}
	return result, rows.Err()
}

// queryFrequency는 최근 `window`개 회차의 num1..num6 중 1~45 각 번호가 몇 번
// 나왔는지 센다. WITH(CTE) + UNION ALL + GROUP BY를 써서 카운팅 자체를 Go가
// 아니라 DB가 하도록 한다 — 이 문법은 표준 SQL이라 SQLite/libSQL에서도 MySQL과
// 완전히 동일하게 동작한다. 한 번도 나오지 않은 번호도 count 0으로 맵에 그대로
// 남겨서, 프론트엔드가 45개 슬롯을 전부 그릴 수 있게 한다.
func queryFrequency(ctx context.Context, conn *sql.DB, window int) (map[int]int, error) {
	freq := make(map[int]int, 45)
	for n := 1; n <= 45; n++ {
		freq[n] = 0
	}

	rows, err := conn.QueryContext(ctx, `
		WITH recent AS (
			SELECT num1, num2, num3, num4, num5, num6
			FROM lotto_draws ORDER BY drw_no DESC LIMIT ?
		),
		nums AS (
			SELECT num1 AS num FROM recent
			UNION ALL SELECT num2 FROM recent
			UNION ALL SELECT num3 FROM recent
			UNION ALL SELECT num4 FROM recent
			UNION ALL SELECT num5 FROM recent
			UNION ALL SELECT num6 FROM recent
		)
		SELECT num, COUNT(*) FROM nums GROUP BY num`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var num, count int
		if err := rows.Scan(&num, &count); err != nil {
			return nil, err
		}
		freq[num] = count
	}
	return freq, rows.Err()
}

// queryRecentAppeared는 최근 `window`개 회차에서 나온 본번호(보너스 제외)를
// 중복 없이 오름차순으로 반환한다.
func queryRecentAppeared(ctx context.Context, conn *sql.DB, window int) ([]int, error) {
	rows, err := conn.QueryContext(ctx, `
		WITH recent AS (
			SELECT num1, num2, num3, num4, num5, num6
			FROM lotto_draws ORDER BY drw_no DESC LIMIT ?
		)
		SELECT DISTINCT num FROM (
			SELECT num1 AS num FROM recent
			UNION ALL SELECT num2 FROM recent
			UNION ALL SELECT num3 FROM recent
			UNION ALL SELECT num4 FROM recent
			UNION ALL SELECT num5 FROM recent
			UNION ALL SELECT num6 FROM recent
		) t ORDER BY num`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []int
	for rows.Next() {
		var num int
		if err := rows.Scan(&num); err != nil {
			return nil, err
		}
		result = append(result, num)
	}
	return result, rows.Err()
}
