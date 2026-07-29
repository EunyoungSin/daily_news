package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	lottoHistoryWindow    = 50 // 화면에 표시/집계하는 회차 수
	lottoRecentWindow     = 10 // "최근 출현"에 사용하는 회차 수
	lottoFetchConcurrency = 6  // dhlottery API 동시 호출 상한
	lottoDrawHourKST      = 21 // 추첨은 KST 20:45경 진행되며, 그 다음 시각부터 "완료"로 간주
)

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("KST", 9*60*60)
	}
	return loc
}

// theoreticalLatestDrwNo는 매주 추첨된다는 주기성만으로 지금 시점에 존재해야
// 할 최신 회차를 추정한다. 이 값은 조회 범위의 상한일 뿐이다: syncLottoDraws는
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
	url := fmt.Sprintf("https://www.dhlottery.co.kr/common.do?method=getLottoNumber&drwNo=%d", drwNo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
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

// syncLottoDraws는 DB에 저장된 최신 회차와 이론상 최신 회차 사이에 빠진
// 회차를 모두 채워 넣는다. 테이블이 비어 있으면 최근 lottoHistoryWindow개
// 회차를 한 번에 채우고, 그 이후로는 보통 매주 새 회차 하나만 추가하면 된다.
func syncLottoDraws(ctx context.Context, conn *sql.DB) error {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM lotto_draws`).Scan(&count); err != nil {
		return fmt.Errorf("count lotto_draws: %w", err)
	}

	var dbLatest sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT MAX(drw_no) FROM lotto_draws`).Scan(&dbLatest); err != nil {
		return fmt.Errorf("query latest drw_no: %w", err)
	}

	theoretical := theoreticalLatestDrwNo(time.Now())

	start := theoretical - lottoHistoryWindow + 1
	if dbLatest.Valid {
		start = int(dbLatest.Int64) + 1
	}
	if start < 1 {
		start = 1
	}

	if start > theoretical {
		log.Printf("로또: 이미 저장된 회차 %d개, 신규 회차 없음", count)
		return nil
	}

	toFetch := make([]int, 0, theoretical-start+1)
	for n := start; n <= theoretical; n++ {
		toFetch = append(toFetch, n)
	}
	log.Printf("로또: 이미 저장된 회차 %d개, 신규 %d개만 수집합니다", count, len(toFetch))

	type fetchResult struct {
		drwNo int
		data  *dhlotteryResponse
		err   error
	}

	results := make([]fetchResult, len(toFetch))
	sem := make(chan struct{}, lottoFetchConcurrency)
	var wg sync.WaitGroup

	for i, drwNo := range toFetch {
		wg.Add(1)
		go func(idx, n int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := fetchLottoDraw(ctx, n)
			results[idx] = fetchResult{drwNo: n, data: data, err: err}
		}(i, drwNo)
	}
	wg.Wait()

	inserted := 0
	for _, r := range results {
		if r.err != nil {
			log.Printf("로또: 회차 %d 조회 실패: %v", r.drwNo, r.err)
			continue
		}
		if r.data.ReturnValue != "success" {
			// 아직 추첨되지 않은 회차 — 이론상 추정치가 실제보다 앞서
			// 나간 경우다(예: 이번 주 추첨 직전).
			continue
		}
		if err := insertLottoDraw(ctx, conn, r.data); err != nil {
			log.Printf("로또: 회차 %d 저장 실패: %v", r.drwNo, err)
			continue
		}
		inserted++
	}

	log.Printf("로또: 신규 %d개 회차 저장 완료", inserted)
	return nil
}

func insertLottoDraw(ctx context.Context, conn *sql.DB, d *dhlotteryResponse) error {
	drawDate, err := time.Parse("2006-01-02", d.DrwNoDate)
	if err != nil {
		return fmt.Errorf("parse drwNoDate %q: %w", d.DrwNoDate, err)
	}

	_, err = conn.ExecContext(ctx, `
		INSERT INTO lotto_draws (drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE drw_date = drw_date`,
		d.DrwNo, drawDate, d.DrwtNo1, d.DrwtNo2, d.DrwtNo3, d.DrwtNo4, d.DrwtNo5, d.DrwtNo6, d.BnusNo,
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
// 나왔는지 센다. UNION ALL + GROUP BY를 써서 카운팅 자체를 Go가 아니라
// MySQL이 하도록 한다. 한 번도 나오지 않은 번호도 count 0으로 맵에 그대로
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
