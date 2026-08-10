package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// 로또는 1회차부터 예외 없이 매주 토요일에 추첨해왔으므로, "회차 번호"와
// "날짜"는 단순 산술 연산만으로 서로 변환할 수 있다.
var kst = mustLoadLocation("Asia/Seoul")

var lottoFirstDrawDate = time.Date(2002, 12, 7, 0, 0, 0, 0, kst)

const (
	lottoHistoryWindow = 50              // 화면에 표시/집계하는 회차 수
	lottoRecentWindow  = 10              // "최근 출현"에 사용하는 회차 수
	lottoDrawHourKST   = 21              // 추첨은 KST 20:45경 진행되며, 그 다음 시각부터 "완료"로 간주
	lottoFetchTimeout  = 8 * time.Second // 개별 회차 조회 1회 시도당 타임아웃

	// dhlottery가 기본 Go 클라이언트의 User-Agent("Go-http-client/...")를
	// 보고 봇으로 판단해 차단할 가능성을 줄이기 위해, 일반 브라우저처럼
	// 보이는 값을 명시적으로 지정한다.
	lottoUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// lottoCheckInterval은 자동 수집이 "새 회차가 나왔는지" 확인하는 주기다.
// 실제로 새 회차는 일주일에 한 번(토요일 추첨)만 생기므로, 이보다 훨씬
// 자주 확인해봐야 dhlottery에 불필요한 요청만 늘어난다. 하루 1번으로
// 잡아서 서버가 하루 정도 다운되었다 다시 뜨는 경우도 금방 따라잡게
// 하면서도, "여러 회차를 동시에 병렬로 긁어오는" 예전 방식과 달리 확인
// 자체는 항상 가볍다(DB 조회 한 번 + 필요하면 dhlottery 요청 최대
// 1개뿐).
const lottoCheckInterval = 24 * time.Hour

// lottoAutoCheckRetryDelays는 자동 점검이 "이미 발표됐어야 할" 회차를
// 못 가져왔을 때 짧게만 재시도하는 간격이다. 예전에는 실패한 회차마다
// 5초→15초→40초로 촘촘하게 재시도하고, 그마저 실패하면 30초 뒤 다음
// pass에서 남은 회차 전부를 다시 시도했다 — 이런 반복적인 두드림 자체가
// dhlottery의 차단을 유발하는 것으로 보였다. 이제는 회차 하나만 다루므로
// 넉넉한 간격으로 딱 2번만 더 시도하고(총 3회), 그래도 안 되면 억지로
// 더 두드리지 않고 다음 정기 점검(lottoCheckInterval 뒤, 보통 내일)까지
// 조용히 기다린다.
var lottoAutoCheckRetryDelays = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
}

// lottoHTTPClient는 dhlottery 호출 전용 클라이언트다. Timeout을 명시적으로
// 두어, 혹시라도 컨텍스트 취소가 누락되는 경우에도 개별 호출이 무한정
// 걸리지 않도록 이중으로 방어한다.
var lottoHTTPClient = &http.Client{Timeout: lottoFetchTimeout}

// lottoCollectionState는 로또 주간 자동 점검 goroutine의 실행 상태를
// 서버 메모리에 둔다 — 화면의 토글 버튼이 POST
// /api/lotto/collection/{start,stop}으로 이 상태를 직접 제어한다. cancel은
// 실행 중일 때만 설정되며, STOP은 이 cancel을 호출해 다음 정기 점검부터는
// 시작하지 않게 한다. 서버 시작 시 기본값은 꺼짐(running=false)이다 —
// main.go는 시작 시점에 자동으로 켜지 않는다.
var lottoCollectionState struct {
	mu              sync.Mutex
	running         bool
	cancel          context.CancelFunc
	lastCollectedAt time.Time // 마지막으로 신규 회차를 성공적으로 저장한 시각
	lastCheckedAt   time.Time // 마지막으로 점검을 실행한 시각(성공/실패/아직 발표 전 모두 포함)
	nextCheckAt     time.Time // 다음 정기 점검 예정 시각
}

func lottoIsCollecting() bool {
	lottoCollectionState.mu.Lock()
	defer lottoCollectionState.mu.Unlock()
	return lottoCollectionState.running
}

// lottoStartCollection은 이미 실행 중이 아니라면 백그라운드 goroutine에서
// runLottoWeeklyCheckLoop를 시작하고 true를 반환한다. 이미 실행 중이면
// 아무 것도 하지 않고 false를 반환한다(POST /start를 중복으로 눌러도
// 안전). 이 goroutine은 context.Background()에서 파생된, 이를 호출한
// HTTP 요청과는 완전히 무관한 취소 가능(cancellable) 컨텍스트를 쓰므로,
// 이 요청이 끝나거나 타임아웃되어도 점검 자체는 STOP을 누를 때까지
// 계속된다.
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

	log.Println("로또: 매주 자동 업데이트 시작 (POST /api/lotto/collection/start)")

	go func() {
		defer func() {
			lottoCollectionState.mu.Lock()
			lottoCollectionState.running = false
			lottoCollectionState.cancel = nil
			lottoCollectionState.mu.Unlock()
		}()

		runLottoWeeklyCheckLoop(ctx, conn)
	}()
	return true
}

// lottoStopCollection은 실행 중이면 그 컨텍스트를 취소하고 true를
// 반환한다. 실행 중이 아니면 아무 것도 하지 않고 false를 반환한다.
func lottoStopCollection() bool {
	lottoCollectionState.mu.Lock()
	defer lottoCollectionState.mu.Unlock()
	if !lottoCollectionState.running || lottoCollectionState.cancel == nil {
		return false
	}
	log.Println("로또: 매주 자동 업데이트 중단 요청 (POST /api/lotto/collection/stop)")
	lottoCollectionState.cancel()
	return true
}

// LottoCollectionStatus는 GET /api/lotto/collection/status의 응답이다.
// 예전에는 "42/50 회차 수집됨" 같은 배치 진행률을 보여줬지만, 이제는
// 목표치라는 개념 자체가 없다(시드로 채워진 뒤로는 매주 최대 1개씩만
// 늘어난다) — 대신 "다음 자동 확인은 언제고, 마지막으로 언제 성공했는지"를
// 보여준다.
type LottoCollectionStatus struct {
	Running         bool   `json:"running"`
	LastCollectedAt string `json:"lastCollectedAt,omitempty"`
	LastCheckedAt   string `json:"lastCheckedAt,omitempty"`
	NextCheckAt     string `json:"nextCheckAt,omitempty"`
	SavedCount      int    `json:"savedCount"`
}

// lottoCollectionStatusSnapshot은 현재 실행 상태와, DB에 실제로 저장된
// 회차 수를 함께 반환한다.
func lottoCollectionStatusSnapshot(ctx context.Context, conn *sql.DB) (LottoCollectionStatus, error) {
	lottoCollectionState.mu.Lock()
	running := lottoCollectionState.running
	lastCollectedAt := lottoCollectionState.lastCollectedAt
	lastCheckedAt := lottoCollectionState.lastCheckedAt
	nextCheckAt := lottoCollectionState.nextCheckAt
	lottoCollectionState.mu.Unlock()

	status := LottoCollectionStatus{Running: running}
	if !lastCollectedAt.IsZero() {
		status.LastCollectedAt = lastCollectedAt.Format(time.RFC3339)
	}
	if !lastCheckedAt.IsZero() {
		status.LastCheckedAt = lastCheckedAt.Format(time.RFC3339)
	}
	if !nextCheckAt.IsZero() {
		status.NextCheckAt = nextCheckAt.Format(time.RFC3339)
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
// 할 최신 회차를 추정한다. 이 값은 "이 회차가 이미 발표됐어야 하는가"를
// 판단하는 기준일 뿐이다: dhlottery API 자체의 성공/실패 응답을 실제
// 기준으로 삼기 때문에, 여기서 발생한 오차(예: 추첨 시각 전후)는 잘못된
// 데이터를 삽입하지 않고 자연스럽게 스스로 보정된다.
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

// lottoGitHubDatasetBaseURL은 자동 수집이 회차를 조회하는 유일한 대상이다.
// dhlottery가 이 서버의 IP를 차단해(자동 요청을 봇으로 판단한 것으로
// 보인다 — lottoUserAgent 주석 참고) 직접 호출이 계속 실패했었다. 이를
// 우회하기 위해 커뮤니티가 유지 관리하는 공개 GitHub 저장소
// smok95/lotto로 갈아탔다 — 매주 토요일 추첨 직후(실측:
// 2026-07-25/08-01/08-08 모두 KST 20:41~21:00 사이 커밋)에 회차별 JSON
// 파일을 자동 커밋해 raw.githubusercontent.com으로 그대로 서빙하므로,
// dhlottery를 전혀 두드리지 않고도 최신 회차를 얻을 수 있다. 이 소스가
// 실패하면 checkForNewLottoRound는 dhlottery로 다시 폴백하지 않고 다음
// 정기 점검까지 기다린다 — fetchLottoDraw 문서 주석 참고.
const lottoGitHubDatasetBaseURL = "https://raw.githubusercontent.com/smok95/lotto/main/results"

// githubLottoDraw는 lottoGitHubDatasetBaseURL이 회차별로 제공하는 JSON의
// 부분집합이다 — divisions/total_sales_amount/winners_combination 등
// 이 대시보드가 쓰지 않는 필드는 그대로 무시된다(encoding/json은 구조체에
// 없는 필드를 조용히 건너뛴다).
type githubLottoDraw struct {
	DrawNo  int    `json:"draw_no"`
	Numbers []int  `json:"numbers"`
	BonusNo int    `json:"bonus_no"`
	Date    string `json:"date"` // RFC3339, 예: "2026-08-08T00:00:00Z"
}

// parseGitHubLottoDraw는 lottoGitHubDatasetBaseURL/{drwNo}.json의 원본
// 바이트를 dhlotteryResponse와 동일한 모양으로 변환한다 — 호출하는 쪽
// (checkForNewLottoRound)과 저장하는 쪽(insertLottoDraw) 모두 데이터
// 출처가 dhlottery인지 이 GitHub 데이터셋인지 신경 쓸 필요가 없다.
// fetchLottoDrawFromGitHub의 네트워크 호출과 분리해뒀다 — 이 변환/검증
// 로직(회차 번호 불일치, 번호 개수, 날짜 형식)은 네트워크 없이 단위
// 테스트로 고정해둘 가치가 있기 때문이다.
func parseGitHubLottoDraw(body []byte, wantDrwNo int) (*dhlotteryResponse, error) {
	var parsed githubLottoDraw
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse github lotto dataset response for round %d: %w", wantDrwNo, err)
	}
	if parsed.DrawNo != wantDrwNo || len(parsed.Numbers) != 6 {
		return nil, fmt.Errorf("unexpected github lotto dataset response shape for round %d (draw_no=%d, numbers=%d개)",
			wantDrwNo, parsed.DrawNo, len(parsed.Numbers))
	}

	drawDate, err := time.Parse(time.RFC3339, parsed.Date)
	if err != nil {
		return nil, fmt.Errorf("parse github lotto dataset date %q for round %d: %w", parsed.Date, wantDrwNo, err)
	}

	return &dhlotteryResponse{
		ReturnValue: "success",
		DrwNo:       parsed.DrawNo,
		DrwNoDate:   drawDate.Format("2006-01-02"),
		DrwtNo1:     parsed.Numbers[0],
		DrwtNo2:     parsed.Numbers[1],
		DrwtNo3:     parsed.Numbers[2],
		DrwtNo4:     parsed.Numbers[3],
		DrwtNo5:     parsed.Numbers[4],
		DrwtNo6:     parsed.Numbers[5],
		BnusNo:      parsed.BonusNo,
	}, nil
}

// fetchLottoDrawFromGitHub는 lottoGitHubDatasetBaseURL/{drwNo}.json을 조회해
// parseGitHubLottoDraw로 변환한다. 아직 그 회차 파일이 없으면(다음 추첨
// 전) 404가 오는데, 이는 dhlottery의 ReturnValue!="success"와 같은 뜻이지만
// 별도로 구분하지 않는다 — 어느 쪽이든 이 함수가 에러를 반환하면
// checkForNewLottoRound는 그대로 dhlottery 폴백으로 넘어가고, dhlottery
// 쪽에서 "아직 발표 전"과 "진짜 실패"를 다시 한번 정확히 구분한다.
func fetchLottoDrawFromGitHub(ctx context.Context, drwNo int) (*dhlotteryResponse, error) {
	url := fmt.Sprintf("%s/%d.json", lottoGitHubDatasetBaseURL, drwNo)

	callCtx, cancel := context.WithTimeout(ctx, lottoFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := lottoHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github lotto dataset returned status %d for round %d", resp.StatusCode, drwNo)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read github lotto dataset response for round %d: %w", drwNo, err)
	}

	return parseGitHubLottoDraw(body, drwNo)
}

// fetchLottoDraw/fetchLottoDrawWithShortRetry는 dhlottery를 직접 호출해
// 회차를 조회하던 원래 방식이다. 지금은 checkForNewLottoRound가 이 둘을
// 전혀 호출하지 않는다 — dhlottery가 이 서버의 IP를 차단해 자동 수집이
// 계속 실패했고, 그걸 우회하려고 GitHub 데이터셋(lottoGitHubDatasetBaseURL
// 주석 참고)으로 갈아탔기 때문이다. 나중에 dhlottery 접근이 다시 가능해지거나
// GitHub 소스가 더 이상 유지되지 않는 경우를 대비해 백업으로 코드에 남겨
// 둔다 — 실제로 되살리려면 checkForNewLottoRound에서 다시 호출하도록
// 연결해야 한다.
func fetchLottoDraw(ctx context.Context, drwNo int) (*dhlotteryResponse, error) {
	url := fmt.Sprintf("https://www.dhlottery.co.kr/common.do?method=getLottoNumber&drwNo=%d", drwNo)

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

// fetchLottoDrawWithShortRetry는 lottoAutoCheckRetryDelays에 따라 넉넉한
// 간격으로 최대 2번만 더 재시도한다(총 3회 시도). 부모 ctx가 이미 취소된
// 상태라면(예: STOP 요청) 대기 없이 즉시 중단한다. 여기서도 실패하면 이번
// 정기 점검은 포기하고 다음 lottoCheckInterval 주기에 다시 시도한다 —
// runLottoWeeklyCheckLoop 참고.
func fetchLottoDrawWithShortRetry(ctx context.Context, drwNo int) (*dhlotteryResponse, error) {
	maxAttempts := len(lottoAutoCheckRetryDelays) + 1
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := lottoAutoCheckRetryDelays[attempt-1]
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
		log.Printf("로또: %d회차 조회 실패 (%d/%d회 시도): %v", drwNo, attempt+1, maxAttempts, err)
	}
	return nil, lastErr
}

// lottoInsertTimeout은 회차 하나를 저장하는 INSERT 자체의 타임아웃이다.
// context.Background()에서 독립적으로 파생시키므로, 수집 전체의 ctx가
// 마침 그 순간 취소되더라도(STOP 요청 등) 이미 성공적으로 조회해둔
// 데이터를 저장하는 마지막 한 단계는 항상 자기 몫의 시간을 보장받는다 —
// raw_data_cache.go의 rawCacheUpsertTimeout과 같은 이유다.
const lottoInsertTimeout = 5 * time.Second

// runLottoWeeklyCheckLoop는 lottoStartCollection이 시작한 백그라운드
// goroutine의 본체다. 토글을 켠 즉시 한 번 확인하고(응답성을 위해 다음
// 정기 주기까지 기다리지 않는다), 이후로는 lottoCheckInterval마다
// checkForNewLottoRound를 반복 호출한다. ctx가 취소되면(STOP 요청) 즉시
// 종료한다.
//
// 예전의 runLottoCollectionLoop/collectLottoRounds(여러 회차를 세마포어로
// 동시에 병렬 스크래핑)는 완전히 제거됐다 — dhlottery가 짧은 시간에
// 몰리는 요청을 차단하는 것으로 보여, "한 번에 최대한 많이 채운다"는
// 접근 자체가 근본적으로 문제였다. 이제는 한 번에 최대 회차 하나만
// 조회한다.
func runLottoWeeklyCheckLoop(ctx context.Context, conn *sql.DB) {
	checkForNewLottoRound(ctx, conn)

	ticker := time.NewTicker(lottoCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("로또: 매주 자동 업데이트가 중단 요청으로 종료됨")
			return
		case <-ticker.C:
			checkForNewLottoRound(ctx, conn)
		}
	}
}

// checkForNewLottoRound는 DB의 최신 회차 다음 번호가 이미 발표되었을
// 시점인지 확인하고, 그렇다면 그 회차 하나만 조회를 시도한다. 아직
// 발표 전이거나(정상 상황), 재시도를 모두 소진하고도 실패했다면
// (dhlottery 차단 등 예외 상황) 아무 것도 하지 않고 다음 정기 점검까지
// 조용히 기다린다 — 이 함수 자체가 실패해도 절대 즉시 재시도하지 않는다.
func checkForNewLottoRound(ctx context.Context, conn *sql.DB) {
	now := time.Now()
	lottoCollectionState.mu.Lock()
	lottoCollectionState.lastCheckedAt = now
	lottoCollectionState.nextCheckAt = now.Add(lottoCheckInterval)
	lottoCollectionState.mu.Unlock()

	if conn == nil {
		log.Println("로또: DB에 연결되어 있지 않아 이번 점검을 건너뜁니다")
		return
	}

	var latestInDB int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(drw_no), 0) FROM lotto_draws`).Scan(&latestInDB); err != nil {
		log.Printf("로또: 최신 저장 회차 조회 실패, 이번 점검을 건너뜁니다: %v", err)
		return
	}

	nextDrwNo := latestInDB + 1
	theoretical := theoreticalLatestDrwNo(now)
	if nextDrwNo > theoretical {
		log.Printf("로또: %d회차는 아직 발표 전으로 추정됨(현재 저장된 최신 회차 %d) — 다음 점검까지 대기", nextDrwNo, latestInDB)
		return
	}

	log.Printf("로또: %d회차가 이미 발표되었을 시점 — 조회 시도", nextDrwNo)

	// GitHub 데이터셋만 사용한다 — dhlottery 직접 호출(fetchLottoDraw/
	// fetchLottoDrawWithShortRetry)로의 폴백은 의도적으로 두지 않았다.
	// dhlottery가 이 서버의 IP를 차단해 자동 수집이 계속 실패했던 것이
	// 애초에 이 GitHub 소스로 갈아탄 이유였으므로, 여기서 실패했다고 다시
	// dhlottery로 넘어가 봐야 마찬가지로 차단당해 시간만 낭비하고 오히려
	// 추가 차단 사유를 만들 뿐이다. 실패하면 다음 정기 점검까지 조용히
	// 기다린다 — 아래 fetchLottoDraw류 함수는 dhlottery 접근이 다시
	// 가능해지는 경우를 대비한 백업으로 코드에 남겨뒀을 뿐, 이 경로에서는
	// 호출하지 않는다.
	data, err := fetchLottoDrawFromGitHub(ctx, nextDrwNo)
	if err != nil {
		log.Printf("로또: GitHub 데이터셋에서 %d회차 조회 실패 — 다음 정기 점검까지 대기: %v", nextDrwNo, err)
		return
	}
	log.Printf("로또: GitHub 데이터셋에서 %d회차 조회 성공", nextDrwNo)

	insertCtx, cancel := context.WithTimeout(context.Background(), lottoInsertTimeout)
	defer cancel()
	if err := insertLottoDraw(insertCtx, conn, data); err != nil {
		log.Printf("로또: %d회차 저장 실패: %v", nextDrwNo, err)
		return
	}

	lottoCollectionState.mu.Lock()
	lottoCollectionState.lastCollectedAt = time.Now()
	lottoCollectionState.mu.Unlock()
	log.Printf("로또: %d회차 저장 완료", nextDrwNo)
}

// insertLottoDraw는 자동 점검 전용 저장 경로다. drwNoDate를 time.Parse로
// 한 번 검증만 하고(형식이 잘못된 응답을 조용히 저장하지 않기 위해),
// 실제 바인딩에는 재파싱한 time.Time이 아니라 원본 문자열을 그대로 쓴다
// — go-libsql은 bind 파라미터가 time.Time이면 RFC3339Nano로 재포맷해서
// 저장하므로(예: "2002-12-07"이 "2002-12-07T00:00:00Z"가 됨), 원본
// 문자열을 그대로 바인딩해야 drw_date가 깔끔한 "YYYY-MM-DD" 형태로
// 저장된다.
//
// ON CONFLICT는 DO NOTHING이다 — 한 번 저장된 회차의 당첨 번호는 절대
// 바뀌지 않으므로, 이미 있는 drw_no로 다시 들어오면 그냥 무시한다. 오타
// 정정이 필요한 경우(수동 입력)는 lotto_admin.go의
// upsertLottoDrawManual(ON CONFLICT DO UPDATE)을 대신 쓴다 — 이 함수를
// 공유하지 않는 이유는, 자동 수집 경로에서는 dhlottery의 실제 응답을
// 실수로 다른 값으로 덮어쓸 여지 자체를 원천 차단하고 싶기 때문이다.
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
