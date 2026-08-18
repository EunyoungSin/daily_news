package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
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
	// 보이는 값을 명시적으로 지정한다. fetchLottoDraw(현재 호출되지 않는
	// 백업 코드, 아래 참고)에서만 쓰인다.
	lottoUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// lottoCheckInterval은 밀린 회차를 전부 채운 뒤, 평상시 "새 회차가
// 나왔는지"를 확인하는 정기 점검 주기다. 실제로 새 회차는 일주일에 한 번
// (토요일 추첨)만 생기므로, 이보다 훨씬 자주 확인해봐야 불필요한 요청만
// 늘어난다. 하루 1번으로 잡아서 서버가 하루 정도 다운되었다 다시 뜨는
// 경우도 금방 따라잡게 하면서도, 평상시 점검 자체는 항상 가볍다(DB 조회
// 한 번 + 필요하면 GitHub 데이터셋 요청 최대 1개뿐) — catchUpMissingLottoRounds
// 문서 주석 참고.
const lottoCheckInterval = 24 * time.Hour

// lottoAutoCollectionDefaultEnvVar는 서버가 시작할 때 자동 수집을 곧바로
// 켤지 결정하는 환경변수다. 기본값은 "on"이다 — 자동 수집이 GitHub
// 데이터셋(정적 파일 서빙)만 사용하도록 바뀐 뒤로는 dhlottery 차단 같은
// 위험이 없어 서버가 뜨자마자 켜둬도 안전하기 때문이다. "off"로 설정하면
// 예전처럼 화면의 "매주 자동 업데이트" 토글을 직접 눌러야만 시작된다.
const lottoAutoCollectionDefaultEnvVar = "LOTTO_AUTO_COLLECTION_DEFAULT"

// lottoAutoCollectionDefaultOn은 lottoAutoCollectionDefaultEnvVar 값을
// 읽는다 — 명시적으로 "off"가 아니면(값이 없는 기본 상태 포함) true다.
func lottoAutoCollectionDefaultOn() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(lottoAutoCollectionDefaultEnvVar)))
	return v != "off"
}

// lottoHTTPClient는 GitHub 데이터셋 호출과, 현재는 호출되지 않는 백업
// 경로인 dhlottery 직접 호출(fetchLottoDraw)이 함께 쓰는 클라이언트다.
// Timeout을 명시적으로 두어, 혹시라도 컨텍스트 취소가 누락되는 경우에도
// 개별 호출이 무한정 걸리지 않도록 이중으로 방어한다.
var lottoHTTPClient = &http.Client{Timeout: lottoFetchTimeout}

// lottoDhlotteryRetryDelays는 (현재는 호출되지 않는 백업 경로인)
// fetchLottoDrawWithShortRetry가 dhlottery 조회 실패 시 재시도하는
// 간격이다(총 3회 시도). dhlottery는 짧은 시간에 요청이 몰리면 이후
// 요청을 차단하는 것으로 보였으므로, GitHub 데이터셋용
// lottoCatchUpRetryDelays보다 훨씬 넉넉하게(1분/5분) 잡아뒀다 — 아래
// fetchLottoDraw 문서 주석 참고.
var lottoDhlotteryRetryDelays = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
}

// lottoCollectionState는 로또 자동 점검 goroutine의 실행 상태를 서버
// 메모리에 둔다 — 화면의 토글 버튼이 POST
// /api/lotto/collection/{start,stop}으로 이 상태를 직접 제어한다. cancel은
// 실행 중일 때만 설정되며, STOP은 이 cancel을 호출해 다음 정기 점검부터는
// 시작하지 않게 한다. 서버 시작 시 기본값은 lottoAutoCollectionDefaultOn에
// 따른다 — main.go가 이 값을 보고 시작 시점에 자동으로 켤지 결정한다.
var lottoCollectionState struct {
	mu              sync.Mutex
	running         bool
	cancel          context.CancelFunc
	lastCollectedAt time.Time // 마지막으로 신규 회차를 성공적으로 저장한 시각
	lastCheckedAt   time.Time // 마지막으로 점검을 실행한 시각(성공/실패/아직 발표 전 모두 포함)
	nextCheckAt     time.Time // 다음 정기 점검 예정 시각

	// catchingUp/totalPendingCount/processedCount는 catchUpMissingLottoRounds가
	// 밀린 회차 전부를 순차 처리하는 동안만 의미가 있다 — 평상시 정기 점검
	// (checkForNewLottoRound)은 이 필드들을 건드리지 않는다. catchingUp이
	// true인 동안 화면은 "N회차 밀려있어 순차적으로 채우는 중입니다
	// (processedCount/totalPendingCount)"를 보여준다(CollectionToggle.tsx).
	catchingUp        bool
	totalPendingCount int
	processedCount    int
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
// 평상시(밀린 회차가 없을 때)는 "다음 자동 확인은 언제고, 마지막으로 언제
// 성공했는지"만 보여준다 — 시드로 채워진 뒤로는 매주 최대 1개씩만
// 늘어나므로 진행률이라는 개념이 없기 때문이다. 서버 시작 시(또는 토글을
// 켤 때) 밀린 회차가 있으면 catchingUp이 true가 되고, totalPendingCount/
// processedCount로 "N개 중 몇 번째까지 처리했는지"를 보여준다
// (catchUpMissingLottoRounds 참고).
type LottoCollectionStatus struct {
	Running           bool   `json:"running"`
	CatchingUp        bool   `json:"catchingUp"`
	TotalPendingCount int    `json:"totalPendingCount,omitempty"`
	ProcessedCount    int    `json:"processedCount,omitempty"`
	LastCollectedAt   string `json:"lastCollectedAt,omitempty"`
	LastCheckedAt     string `json:"lastCheckedAt,omitempty"`
	NextCheckAt       string `json:"nextCheckAt,omitempty"`
	SavedCount        int    `json:"savedCount"`
}

// lottoCollectionStatusSnapshot은 현재 실행 상태와, DB에 실제로 저장된
// 회차 수를 함께 반환한다.
func lottoCollectionStatusSnapshot(ctx context.Context, conn *sql.DB) (LottoCollectionStatus, error) {
	lottoCollectionState.mu.Lock()
	running := lottoCollectionState.running
	catchingUp := lottoCollectionState.catchingUp
	totalPendingCount := lottoCollectionState.totalPendingCount
	processedCount := lottoCollectionState.processedCount
	lastCollectedAt := lottoCollectionState.lastCollectedAt
	lastCheckedAt := lottoCollectionState.lastCheckedAt
	nextCheckAt := lottoCollectionState.nextCheckAt
	lottoCollectionState.mu.Unlock()

	status := LottoCollectionStatus{
		Running:           running,
		CatchingUp:        catchingUp,
		TotalPendingCount: totalPendingCount,
		ProcessedCount:    processedCount,
	}
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
// 보인다) 직접 호출이 계속 실패했었다. 이를 우회하기 위해 커뮤니티가
// 유지 관리하는 공개 GitHub 저장소 smok95/lotto로 갈아탔다 — 매주 토요일
// 추첨 직후(실측: 2026-07-25/08-01/08-08 모두 KST 20:41~21:00 사이 커밋)에
// 회차별 JSON 파일을 자동 커밋해 raw.githubusercontent.com으로 그대로
// 서빙하므로, dhlottery를 전혀 두드리지 않고도 최신 회차를 얻을 수 있다.
// 이 소스가 실패하면 dhlottery로 폴백하지 않고 그대로 실패로 취급한다
// (checkForNewLottoRound/catchUpMissingLottoRounds 참고). var로 둔 이유는
// 테스트가 httptest 서버를 가리키도록 바꿔치기할 수 있어야 하기 때문이다.
var lottoGitHubDatasetBaseURL = "https://raw.githubusercontent.com/smok95/lotto/main/results"

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
// 전) 404가 오는데, 호출하는 쪽(checkForNewLottoRound/
// catchUpMissingLottoRounds)이 "아직 발표 전"과 "진짜 실패"를 각자의
// 맥락에 맞게 구분해서 처리한다.
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

// lottoCatchUpRoundDelay는 catchUpMissingLottoRounds가 밀린 회차를 순차
// 조회하는 사이에 두는 간격이다. GitHub 데이터셋은 dhlottery와 달리 봇
// 차단 위험이 낮은 일반 정적 파일 서빙(raw.githubusercontent.com)이라
// 하루 처리 개수 상한을 두지 않고 밀린 회차 전부를 한 번의 점검에서
// 채워도 되지만, 그래도 무료 서비스이니 예의상 짧은 간격만 둔다. var로
// 둔 이유는 테스트가 이 값을 아주 짧게 바꿔 실행 시간을 줄일 수 있어야
// 하기 때문이다.
var lottoCatchUpRoundDelay = 300 * time.Millisecond

// lottoCatchUpRetryDelays는 catchUpMissingLottoRounds가 회차 하나 조회에
// 실패했을 때 짧게 재시도하는 간격이다(총 3회 시도). dhlottery 차단을
// 피하려고 1분/5분씩 기다리던 예전 방식과 달리 훨씬 짧게 잡아도 된다 —
// GitHub 정적 파일은 차단 위험이 낮고, 실패의 대부분은 "그 회차 파일이
// 데이터셋에 아예 없음"이라 오래 기다려도 소용없기 때문이다.
var lottoCatchUpRetryDelays = []time.Duration{
	2 * time.Second,
	5 * time.Second,
}

// fetchLottoDrawFromGitHubWithRetry는 fetchLottoDrawFromGitHub를 delays에
// 따라 짧게 재시도한다(총 len(delays)+1회 시도). 부모 ctx가 이미 취소된
// 상태라면(예: STOP 요청) 대기 없이 즉시 중단한다.
func fetchLottoDrawFromGitHubWithRetry(ctx context.Context, drwNo int, delays []time.Duration) (*dhlotteryResponse, error) {
	maxAttempts := len(delays) + 1
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := delays[attempt-1]
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		data, err := fetchLottoDrawFromGitHub(ctx, drwNo)
		if err == nil {
			return data, nil
		}
		lastErr = err
		log.Printf("로또: GitHub 데이터셋에서 %d회차 조회 실패 (%d/%d회 시도): %v", drwNo, attempt+1, maxAttempts, err)
	}
	return nil, lastErr
}

// fetchLottoDraw/fetchLottoDrawWithShortRetry는 dhlottery
// (https://www.dhlottery.co.kr/common.do?method=getLottoNumber)를 직접
// 호출해 회차를 조회하던 원래 방식이다. **현재 자동 수집 경로
// (checkForNewLottoRound/catchUpMissingLottoRounds)는 이 둘을 전혀
// 호출하지 않는다** — 예전에 초기 50회 데이터를 채우려고 여러 회차를
// 동시에(세마포어로 병렬) 긁어왔더니, 짧은 시간에 요청이 몰린 것을
// dhlottery가 봇으로 판단해 이 서버의 IP 자체를 차단해버렸다. 그 뒤로는
// 재시도해봐야 계속 차단된 상태라 실패만 반복되므로, 커뮤니티가 유지
// 관리하는 공개 GitHub 데이터셋(lottoGitHubDatasetBaseURL 참고)으로 수집
// 경로를 완전히 갈아탔다.
//
// 이 두 함수는 삭제하지 않고 백업으로 코드에 남겨뒀다 — 나중에 dhlottery
// 접근이 다시 가능해지거나(IP 차단이 풀리거나) GitHub 데이터셋 저장소가
// 더 이상 유지되지 않게 되면, checkForNewLottoRound/
// catchUpMissingLottoRounds에서 다시 호출하도록 연결해 되살릴 수 있다.
// 되살릴 때는 예전처럼 회차를 동시에 병렬로 긁어오지 말고, 지금 GitHub
// 경로가 하듯 회차 사이에 충분한 간격을 두고 순차적으로만 호출해야
// 같은 차단을 반복하지 않는다.
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

// fetchLottoDrawWithShortRetry는 lottoDhlotteryRetryDelays에 따라 넉넉한
// 간격으로 최대 2번만 더 재시도한다(총 3회 시도). 부모 ctx가 이미 취소된
// 상태라면(예: STOP 요청) 대기 없이 즉시 중단한다. fetchLottoDraw와
// 마찬가지로 현재는 어디서도 호출되지 않는 백업 코드다.
func fetchLottoDrawWithShortRetry(ctx context.Context, drwNo int) (*dhlotteryResponse, error) {
	maxAttempts := len(lottoDhlotteryRetryDelays) + 1
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := lottoDhlotteryRetryDelays[attempt-1]
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
		log.Printf("로또: dhlottery에서 %d회차 조회 실패 (%d/%d회 시도): %v", drwNo, attempt+1, maxAttempts, err)
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
// 동시에 병렬 스크래핑)는 dhlottery를 직접 호출하던 시절 완전히
// 제거됐었다 — dhlottery가 짧은 시간에 몰리는 요청을 차단하는 것으로
// 보였기 때문이다. GitHub 데이터셋(정적 파일 서빙)으로 갈아탄 뒤로는 그
// 위험이 없으므로, 시작 시(또는 토글을 켤 때) catchUpMissingLottoRounds가
// 밀린 회차 전부를 순차적으로 한 번에 채운다 — 이후 ticker가 도는
// 평상시 정기 점검(checkForNewLottoRound)은 여전히 한 번에 최대 회차
// 하나만 확인한다(실제로 새 회차는 일주일에 한 번만 생기므로 그걸로
// 충분하다).
func runLottoWeeklyCheckLoop(ctx context.Context, conn *sql.DB) {
	catchUpMissingLottoRounds(ctx, conn)

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

// checkForNewLottoRound는 runLottoWeeklyCheckLoop의 ticker가 매
// lottoCheckInterval(24시간)마다 호출하는 평상시 정기 점검이다. DB의 최신
// 회차 다음 번호가 이미 발표되었을 시점인지만 확인하고, 그렇다면 그 회차
// 하나만 조회를 시도한다(재시도 없이 단 한 번) — 실제로 새 회차는
// 일주일에 한 번만 생기므로 이걸로 충분하다. 서버 시작 시(또는 토글을 켤
// 때) 밀린 회차가 여러 개 있을 수 있는 경우는 이 함수가 아니라
// catchUpMissingLottoRounds가 처리한다. 아직 발표 전이거나(정상 상황),
// 조회에 실패했다면 아무 것도 하지 않고 다음 정기 점검까지 조용히
// 기다린다 — 이 함수 자체가 실패해도 절대 즉시 재시도하지 않는다.
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

	// GitHub 데이터셋만 사용한다 — dhlottery로의 폴백은 없다. 실패하면
	// 재시도하지 않고 다음 정기 점검까지 조용히 기다린다.
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

	// 새 회차가 저장됐으니, 이 회차가 "실제 결과"로 확정하는 직전 주기의
	// trend/regression/uniform 3개 모드 추천을 확보하고 일치 결과를
	// 계산한다 — insertCtx는 이미 취소된 짧은 컨텍스트라 재사용하지 않고
	// 새 컨텍스트를 쓴다(lottoInsertTimeout 문서 주석과 같은 이유:
	// 저장/후속 계산을 STOP 요청 등으로 인한 상위 ctx 취소와 분리해둔다).
	matchCtx, matchCancel := context.WithTimeout(context.Background(), lottoInsertTimeout)
	defer matchCancel()
	processRetroactivePreviousCycleRecommendations(matchCtx, conn, nextDrwNo, data.DrwNoDate)
}

// catchUpMissingLottoRounds는 runLottoWeeklyCheckLoop가 시작될 때(서버
// 시작 시 자동 수집이 켜져 있거나, 사용자가 토글을 켤 때) 딱 한 번만
// 실행된다. DB 최신 회차와 이론적 최신 회차(theoreticalLatestDrwNo) 사이에
// 밀린 회차가 몇 개인지부터 파악하고, 있다면 오래된 순서대로 전부 순차
// 조회해 채운다. checkForNewLottoRound(평상시 정기 점검, 다음 회차 1개만
// 확인)와 달리 이 함수는 하루 처리 개수 상한을 두지 않는다 — GitHub
// 데이터셋은 dhlottery와 달리 봇 차단 위험이 낮은 일반 정적 파일 서빙이라,
// 여러 회차를 한 번에 몰아서 조회해도 안전하다는 전제가 성립하기
// 때문이다. 그래도 무료 서비스이니 회차 사이에 lottoCatchUpRoundDelay만큼만
// 예의상 간격을 둔다.
//
// 회차 하나가 실패해도(그 회차 파일이 데이터셋에 아직/영영 없는 경우 등)
// 나머지 회차는 계속 진행한다 — 한 회차의 실패가 이후 회차 전부를 막아서는
// 안 되기 때문이다. 진행 상황은 lottoCollectionState의 catchingUp/
// totalPendingCount/processedCount에 기록해 GET
// /api/lotto/collection/status로 노출하고, 화면은 이를 보고 "N회차
// 밀려있어 순차적으로 채우는 중입니다"를 보여준다(CollectionToggle.tsx) —
// 끝나면(catchingUp=false) 안내 문구도 사라진다.
func catchUpMissingLottoRounds(ctx context.Context, conn *sql.DB) {
	now := time.Now()
	lottoCollectionState.mu.Lock()
	lottoCollectionState.lastCheckedAt = now
	lottoCollectionState.nextCheckAt = now.Add(lottoCheckInterval)
	lottoCollectionState.mu.Unlock()

	if conn == nil {
		log.Println("로또: DB에 연결되어 있지 않아 밀린 회차 확인을 건너뜁니다")
		return
	}

	var latestInDB int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(drw_no), 0) FROM lotto_draws`).Scan(&latestInDB); err != nil {
		log.Printf("로또: 최신 저장 회차 조회 실패, 밀린 회차 확인을 건너뜁니다: %v", err)
		return
	}

	theoretical := theoreticalLatestDrwNo(now)
	pending := theoretical - latestInDB
	if pending <= 0 {
		log.Println("로또: 밀린 회차가 없습니다 — 평상시 정기 점검으로 전환")
		return
	}

	log.Printf("로또: %d회차 밀려있음(%d~%d) — 순차적으로 채우기 시작", pending, latestInDB+1, theoretical)

	lottoCollectionState.mu.Lock()
	lottoCollectionState.catchingUp = true
	lottoCollectionState.totalPendingCount = pending
	lottoCollectionState.processedCount = 0
	lottoCollectionState.mu.Unlock()

	defer func() {
		lottoCollectionState.mu.Lock()
		lottoCollectionState.catchingUp = false
		lottoCollectionState.totalPendingCount = 0
		lottoCollectionState.processedCount = 0
		lottoCollectionState.mu.Unlock()
	}()

	for drwNo := latestInDB + 1; drwNo <= theoretical; drwNo++ {
		if ctx.Err() != nil {
			log.Println("로또: 밀린 회차 채우기가 중단 요청으로 종료됨")
			return
		}

		data, err := fetchLottoDrawFromGitHubWithRetry(ctx, drwNo, lottoCatchUpRetryDelays)
		if err != nil {
			log.Printf("로또: 밀린 회차 채우기 중 %d회차 조회 실패 — 건너뛰고 다음 회차로 계속: %v", drwNo, err)
		} else if insertErr := func() error {
			insertCtx, cancel := context.WithTimeout(context.Background(), lottoInsertTimeout)
			defer cancel()
			return insertLottoDraw(insertCtx, conn, data)
		}(); insertErr != nil {
			log.Printf("로또: 밀린 회차 채우기 중 %d회차 저장 실패 — 건너뛰고 다음 회차로 계속: %v", drwNo, insertErr)
		} else {
			lottoCollectionState.mu.Lock()
			lottoCollectionState.lastCollectedAt = time.Now()
			lottoCollectionState.mu.Unlock()
			log.Printf("로또: 밀린 회차 채우기 — %d회차 저장 완료", drwNo)

			matchCtx, matchCancel := context.WithTimeout(context.Background(), lottoInsertTimeout)
			processRetroactivePreviousCycleRecommendations(matchCtx, conn, drwNo, data.DrwNoDate)
			matchCancel()
		}

		lottoCollectionState.mu.Lock()
		lottoCollectionState.processedCount++
		lottoCollectionState.mu.Unlock()

		if drwNo < theoretical {
			select {
			case <-ctx.Done():
				log.Println("로또: 밀린 회차 채우기가 중단 요청으로 종료됨")
				return
			case <-time.After(lottoCatchUpRoundDelay):
			}
		}
	}

	log.Printf("로또: 밀린 회차 채우기 완료(%d개 처리)", pending)

	// 캐치업 자체가 시간이 걸렸으니, 화면에 보여줄 다음 정기 점검 시각을
	// 지금 시점 기준으로 다시 계산해둔다 — runLottoWeeklyCheckLoop가 바로
	// 이어서 time.NewTicker(lottoCheckInterval)를 새로 만들므로 실제 다음
	// tick 시각과 (오차 없이) 일치한다.
	lottoCollectionState.mu.Lock()
	lottoCollectionState.nextCheckAt = time.Now().Add(lottoCheckInterval)
	lottoCollectionState.mu.Unlock()
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

// queryLottoHistory는 항상 DB에 저장된 현재 최신 회차부터 거슬러 올라간
// 최근 `limit`개를 반환한다 — queryLottoHistoryAsOf(limit, 0)의 얇은
// 래퍼일 뿐이다.
func queryLottoHistory(ctx context.Context, conn *sql.DB, limit int) ([]LottoDraw, error) {
	return queryLottoHistoryAsOf(ctx, conn, limit, 0)
}

// queryLottoHistoryAsOf는 queryLottoHistory와 같지만 maxDrwNo가 0보다 크면
// drw_no <= maxDrwNo인 회차만 대상으로 한다 — "지난주 추천 결과" 사후
// 계산(lotto_recommendation_history.go)이 "그 시점에 실제로 존재했던
// 데이터만"으로 과거 추천을 재현하는 데 필요하다. 예를 들어 방금 저장된
// N회차에 대한 지난 사이클의 추천을 사후 계산하려면, N회차 자체는 그
// 사이클 동안 아직 발표되지 않았던 미래 정보이므로 maxDrwNo=N-1로
// 걸러야 한다.
func queryLottoHistoryAsOf(ctx context.Context, conn *sql.DB, limit, maxDrwNo int) ([]LottoDraw, error) {
	query := `SELECT drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no FROM lotto_draws`
	args := []any{}
	if maxDrwNo > 0 {
		query += ` WHERE drw_no <= ?`
		args = append(args, maxDrwNo)
	}
	query += ` ORDER BY drw_no DESC LIMIT ?`
	args = append(args, limit)

	rows, err := conn.QueryContext(ctx, query, args...)
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

// queryLottoDrawNumbers는 특정 회차의 본번호 6개(보너스 제외)를 오름차순이
// 아닌 추첨 순서 그대로 반환한다 — "지난주 추천 결과"가 실제 당첨번호와
// 대조할 때 쓴다.
func queryLottoDrawNumbers(ctx context.Context, conn *sql.DB, drwNo int) ([]int, error) {
	var n1, n2, n3, n4, n5, n6 int
	err := conn.QueryRowContext(ctx,
		`SELECT num1, num2, num3, num4, num5, num6 FROM lotto_draws WHERE drw_no = ?`, drwNo,
	).Scan(&n1, &n2, &n3, &n4, &n5, &n6)
	if err != nil {
		return nil, err
	}
	return []int{n1, n2, n3, n4, n5, n6}, nil
}

// queryFrequency는 항상 DB에 저장된 현재 최신 회차부터 거슬러 올라간 최근
// `window`개 회차 기준으로 집계한다 — queryFrequencyAsOf(window, 0)의 얇은
// 래퍼일 뿐이다.
func queryFrequency(ctx context.Context, conn *sql.DB, window int) (map[int]int, error) {
	return queryFrequencyAsOf(ctx, conn, window, 0)
}

// queryFrequencyAsOf는 queryFrequency와 같지만 maxDrwNo가 0보다 크면
// drw_no <= maxDrwNo인 회차만 집계 대상으로 한다 — queryLottoHistoryAsOf와
// 같은 이유(지난주 추천 결과 사후 계산)로 필요하다. WITH(CTE) + UNION ALL +
// GROUP BY를 써서 카운팅 자체를 Go가 아니라 DB가 하도록 한다 — 이 문법은
// 표준 SQL이라 SQLite/libSQL에서도 MySQL과 완전히 동일하게 동작한다. 한
// 번도 나오지 않은 번호도 count 0으로 맵에 그대로 남겨서, 프론트엔드가
// 45개 슬롯을 전부 그릴 수 있게 한다.
func queryFrequencyAsOf(ctx context.Context, conn *sql.DB, window, maxDrwNo int) (map[int]int, error) {
	freq := make(map[int]int, 45)
	for n := 1; n <= 45; n++ {
		freq[n] = 0
	}

	recentQuery := `SELECT num1, num2, num3, num4, num5, num6 FROM lotto_draws`
	args := []any{}
	if maxDrwNo > 0 {
		recentQuery += ` WHERE drw_no <= ?`
		args = append(args, maxDrwNo)
	}
	recentQuery += ` ORDER BY drw_no DESC LIMIT ?`
	args = append(args, window)

	query := fmt.Sprintf(`
		WITH recent AS (%s),
		nums AS (
			SELECT num1 AS num FROM recent
			UNION ALL SELECT num2 FROM recent
			UNION ALL SELECT num3 FROM recent
			UNION ALL SELECT num4 FROM recent
			UNION ALL SELECT num5 FROM recent
			UNION ALL SELECT num6 FROM recent
		)
		SELECT num, COUNT(*) FROM nums GROUP BY num`, recentQuery)

	rows, err := conn.QueryContext(ctx, query, args...)
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
