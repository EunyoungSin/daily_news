package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// 이번 회차 판매는 토요일 20:00 KST에 마감된다.
	lottoSalesCloseWeekday = time.Saturday
	lottoSalesCloseHour    = 20
	// 다음 회차의 추천번호는 일요일 06:00 KST에 공개된다 — lotto_draws가
	// 새로 추첨된 회차를 반영하는 시점과 정확히 같아서, "이번 주" 추천번호와
	// "이번 주" 빈도 데이터가 함께 바뀐다.
	lottoCycleStartWeekday = time.Sunday
	lottoCycleStartHour    = 6
)

// isLottoRecommendationBlackout은 `now`가 이번 회차 판매 마감 시점과 다음
// 회차 번호 공개 시점 사이에 있는지를 나타낸다. 이 구간 동안에는 더 이상
// 판매되지 않는 회차의 낡은 번호를 보여주는 대신, 추천번호 자체를 아예
// 숨긴다.
func isLottoRecommendationBlackout(now time.Time) bool {
	now = now.In(kst)
	if now.Weekday() == lottoSalesCloseWeekday && now.Hour() >= lottoSalesCloseHour {
		return true
	}
	if now.Weekday() == lottoCycleStartWeekday && now.Hour() < lottoCycleStartHour {
		return true
	}
	return false
}

// lottoCycleStartDate는 `now` 이전(또는 같은 시점)의 가장 최근 일요일
// 06:00 KST를 반환한다 — "이번 주" 추천 사이클을 식별하는 값이다.
// isLottoRecommendationBlackout(now)가 false일 때만 의미가 있으며, blackout
// 구간 중에는 아직 "현재" 사이클이 존재하지 않는다.
func lottoCycleStartDate(now time.Time) time.Time {
	now = now.In(kst)
	daysSinceSunday := int(now.Weekday())
	sunday := now.AddDate(0, 0, -daysSinceSunday)
	return time.Date(sunday.Year(), sunday.Month(), sunday.Day(), lottoCycleStartHour, 0, 0, 0, kst)
}

// nextLottoAvailableAt은 `now`보다 엄밀히 이후인 다음 일요일 06:00 KST를
// 반환한다 — blackout이 언제 끝나는지 프론트엔드에 알려주는 데 쓰인다.
func nextLottoAvailableAt(now time.Time) time.Time {
	now = now.In(kst)
	daysUntilSunday := (7 - int(now.Weekday())) % 7
	candidate := now.AddDate(0, 0, daysUntilSunday)
	candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), lottoCycleStartHour, 0, 0, 0, kst)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

const (
	recommendationGroupHot  = "hot"
	recommendationGroupMid  = "mid"
	recommendationGroupCold = "cold"
	recommendationGroupSize = 15 // 45개 번호 / 3개 그룹
	recommendationPickCount = 2  // 그룹당 뽑는 개수
)

// computeRecommendationNumbers는 최근(lottoHistoryWindow 회차만큼의) 출현
// 빈도를 기준으로 1~45를 15개씩 세 구간으로 나눈다 — 상위 15개("hot"),
// 중위 15개("mid"), 하위 15개("cold") — 그리고 각 구간에서 무작위로 2개씩
// 뽑아 총 6개를 만든다.
//
// 이는 보여주는 번호에 다양성을 주기 위한 "빈도 구간을 섞는" 장치일 뿐,
// 어떤 구간이 통계적으로 더 유리하다는 주장이 아니다: 로또 추첨은 매번
// 독립적인 사건이며, 과거 출현 횟수가 다음 회차의 확률에 영향을 주지
// 않는다. 구간 나누기는 그룹당 2개씩 뽑는 자리에 어떤 번호가 *뽑힐 자격이
// 있는지*만 정할 뿐이고, 실제로 구간 안에서 어떤 번호가 뽑히는지는 균등
// 확률의 무작위다.
func computeRecommendationNumbers(frequency map[int]int) []LottoRecommendationNumber {
	numbers := make([]int, 45)
	for n := 1; n <= 45; n++ {
		numbers[n-1] = n
	}

	// 빈도 내림차순으로 정렬하고, 동점일 때는 번호 오름차순으로 정렬한다.
	// 이는 순전히 구간 경계를 결정적(deterministic)으로 만들기 위한 것이다
	// (동일한 빈도를 가진 번호가 많을 경우, 이렇게 하지 않으면 "누가 상위
	// 구간에 들어가는지"가 map 순회 순서에 따라 달라진다) — 이 동점 처리에는
	// 통계적 의미가 전혀 없다.
	sort.Slice(numbers, func(i, j int) bool {
		if frequency[numbers[i]] != frequency[numbers[j]] {
			return frequency[numbers[i]] > frequency[numbers[j]]
		}
		return numbers[i] < numbers[j]
	})

	bands := []struct {
		group string
		pool  []int
	}{
		{recommendationGroupHot, numbers[0:recommendationGroupSize]},
		{recommendationGroupMid, numbers[recommendationGroupSize : recommendationGroupSize*2]},
		{recommendationGroupCold, numbers[recommendationGroupSize*2 : recommendationGroupSize*3]},
	}

	result := make([]LottoRecommendationNumber, 0, len(bands)*recommendationPickCount)
	for _, band := range bands {
		pool := make([]int, len(band.pool))
		copy(pool, band.pool)
		rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })

		for _, n := range pool[:recommendationPickCount] {
			result = append(result, LottoRecommendationNumber{Number: n, Group: band.group})
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result
}

func encodeRecommendationNumbers(numbers []LottoRecommendationNumber) (numbersCSV, groupsCSV string) {
	nParts := make([]string, len(numbers))
	gParts := make([]string, len(numbers))
	for i, n := range numbers {
		nParts[i] = strconv.Itoa(n.Number)
		gParts[i] = n.Group
	}
	return strings.Join(nParts, ","), strings.Join(gParts, ",")
}

func decodeRecommendationNumbers(numbersCSV, groupsCSV string) ([]LottoRecommendationNumber, error) {
	nParts := strings.Split(numbersCSV, ",")
	gParts := strings.Split(groupsCSV, ",")
	if len(nParts) != len(gParts) {
		return nil, fmt.Errorf("numbers/groups length mismatch: %d vs %d", len(nParts), len(gParts))
	}

	result := make([]LottoRecommendationNumber, len(nParts))
	for i, s := range nParts {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("parse number %q: %w", s, err)
		}
		result[i] = LottoRecommendationNumber{Number: n, Group: strings.TrimSpace(gParts[i])}
	}
	return result, nil
}

func lookupLottoRecommendation(ctx context.Context, conn *sql.DB, cycleStartDate string) (numbers []LottoRecommendationNumber, basedOnDrwNo int, basedOnDataHash string, generatedAt time.Time, found bool) {
	var numbersCSV, groupsCSV string
	err := conn.QueryRowContext(ctx,
		`SELECT numbers, number_groups, based_on_drw_no, based_on_data_hash, generated_at FROM lotto_recommendation WHERE cycle_start_date = ?`, cycleStartDate,
	).Scan(&numbersCSV, &groupsCSV, &basedOnDrwNo, &basedOnDataHash, &generatedAt)
	if err != nil {
		return nil, 0, "", time.Time{}, false
	}

	numbers, err = decodeRecommendationNumbers(numbersCSV, groupsCSV)
	if err != nil {
		return nil, 0, "", time.Time{}, false
	}
	return numbers, basedOnDrwNo, basedOnDataHash, generatedAt, true
}

// insertLottoRecommendationIfAbsent는 `INSERT OR IGNORE`를 사용한다
// (cycle_start_date가 primary key다) — MySQL의 `INSERT IGNORE`에 해당하는
// SQLite/libSQL 문법이다. 같은 새 사이클에 대해 동시에 생성 요청이 들어와도
// 둘 다 쓰기에 성공하는 일이 없도록 하기 위해서다 — 경쟁에서 진 쪽의
// insert는 조용히 무시되고, getLottoRecommendation이 이후에 다시 읽어서
// 실제로 이긴 쪽의 row를 가져온다. 한 사이클 안의 모든 요청이 동일한 번호를
// 보게 되는 것은 이 insert가 아니라 그 재조회(re-read) 덕분이다.
func insertLottoRecommendationIfAbsent(ctx context.Context, conn *sql.DB, cycleStartDate string, basedOnDrwNo int, basedOnDataHash string, numbers []LottoRecommendationNumber, generatedAt time.Time) error {
	numbersCSV, groupsCSV := encodeRecommendationNumbers(numbers)
	_, err := conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO lotto_recommendation (cycle_start_date, based_on_drw_no, based_on_data_hash, numbers, number_groups, generated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		cycleStartDate, basedOnDrwNo, basedOnDataHash, numbersCSV, groupsCSV, generatedAt,
	)
	return err
}

// updateLottoRecommendationForNewData는 이미 존재하는 cycle_start_date 행을
// 새 통계 입력 기준으로 덮어쓴다 — 같은 사이클 안에서 통계(frequency)가
// 바뀐 경우에만 호출된다. 이는 두 가지 상황을 모두 포함한다: (1) 자동
// 수집이 막혔다가 관리자가 나중에 새 회차를 수동으로 넣는 경우, (2) 새
// 회차 없이 이미 저장된 회차의 오타를 나중에 정정하는 경우(latest_drw_no는
// 그대로지만 frequency 자체가 바뀐다). WHERE 절에 staleDataHash를 함께
// 거는 것은, 동시에 여러 요청이 "낡았다"고 판단해 각자 새로 계산한(서로
// 다른 무작위) 번호로 서로를 계속 덮어쓰는 경쟁을 막기 위해서다 — 이미
// 누군가 newDataHash로 갱신해뒀다면 이 UPDATE는 0행에 적용되고 조용히
// 넘어가며, getLottoRecommendation이 재조회로 그 결과를 가져온다.
func updateLottoRecommendationForNewData(ctx context.Context, conn *sql.DB, cycleStartDate, staleDataHash string, newDrwNo int, newDataHash string, numbers []LottoRecommendationNumber, generatedAt time.Time) error {
	numbersCSV, groupsCSV := encodeRecommendationNumbers(numbers)
	_, err := conn.ExecContext(ctx, `
		UPDATE lotto_recommendation
		SET based_on_drw_no = ?, based_on_data_hash = ?, numbers = ?, number_groups = ?, generated_at = ?
		WHERE cycle_start_date = ? AND based_on_data_hash = ?`,
		newDrwNo, newDataHash, numbersCSV, groupsCSV, generatedAt, cycleStartDate, staleDataHash,
	)
	return err
}

func toLottoRecommendation(cycleStartDate string, numbers []LottoRecommendationNumber, generatedAt time.Time) LottoRecommendation {
	return LottoRecommendation{
		Numbers:        numbers,
		CycleStartDate: cycleStartDate,
		GeneratedAt:    generatedAt.Format(time.RFC3339),
	}
}

// getLottoRecommendation은 이번 요청의 추천 상태를 결정한다: blackout 안내,
// 캐시된 사이클의 번호, 또는 새로 계산한 번호 중 하나다. 캐시 유효성은
// cycle_start_date만으로 판단하지 않고, 그 통계 계산에 실제로 쓰인
// frequency 입력의 해시(based_on_data_hash)가 지금의 frequency와 같은지도
// 함께 본다 — latest_drw_no(=identity)만 비교하면, 새 회차 없이 이미 저장된
// 회차의 오타만 정정된 경우(latest_drw_no는 그대로지만 frequency는 바뀜)를
// 놓친다. 해시 비교는 이 경우와 "같은 사이클 도중 새 회차가 추가되는"
// 경우를 모두 잡아낸다. based_on_drw_no는 더 이상 유효성 판단에 쓰이지
// 않고, 로그에서 "어느 회차 기준으로 계산됐는지"를 보여주는 용도로만
// 남아있다.
func getLottoRecommendation(ctx context.Context, conn *sql.DB, frequency map[int]int, latestDrwNo int, now time.Time) LottoRecommendation {
	if isLottoRecommendationBlackout(now) {
		return LottoRecommendation{
			IsBlackout:      true,
			NextAvailableAt: nextLottoAvailableAt(now).Format(time.RFC3339),
		}
	}

	cycleStart := lottoCycleStartDate(now).Format("2006-01-02")
	dataHash := hashJSON(frequency)

	numbers, basedOnDrwNo, basedOnDataHash, generatedAt, found := lookupLottoRecommendation(ctx, conn, cycleStart)
	if found && basedOnDataHash == dataHash {
		return toLottoRecommendation(cycleStart, numbers, generatedAt)
	}

	if found {
		log.Printf("로또: 추천번호 캐시가 낡음(cycle=%s, 캐시 기준 회차=%d, 최신 회차=%d) — 통계 변경 감지, 재계산", cycleStart, basedOnDrwNo, latestDrwNo)
		numbers = computeRecommendationNumbers(frequency)
		generatedAt = time.Now()
		if err := updateLottoRecommendationForNewData(ctx, conn, cycleStart, basedOnDataHash, latestDrwNo, dataHash, numbers, generatedAt); err != nil {
			log.Printf("로또: 추천번호 갱신 실패(cycle=%s): %v", cycleStart, err)
		}
	} else {
		numbers = computeRecommendationNumbers(frequency)
		generatedAt = time.Now()
		if err := insertLottoRecommendationIfAbsent(ctx, conn, cycleStart, latestDrwNo, dataHash, numbers, generatedAt); err != nil {
			// 치명적이지 않다 — 최악의 경우 이번 요청은 저장되지 않을 방금 뽑은
			// 번호를 보여주게 되고, 다음 요청이 insert를 다시 시도한다.
			log.Printf("로또: 추천번호 저장 실패(cycle=%s): %v", cycleStart, err)
		}
	}

	// 우리의 write가 경쟁에서 이겼는지 여부와 무관하게 다시 읽는다. 그래야
	// 경쟁에서 진 동시 요청도 자신이 로컬에서 우연히 뽑은 번호가 아니라
	// 실제로 저장된 번호를 반환하게 된다.
	if cachedNumbers, _, _, cachedGeneratedAt, found := lookupLottoRecommendation(ctx, conn, cycleStart); found {
		return toLottoRecommendation(cycleStart, cachedNumbers, cachedGeneratedAt)
	}

	// (단순한 경쟁 문제가 아니라) DB 왕복 자체가 완전히 실패한 경우 — 이번
	// 요청에서라도 섹션이 계속 동작하도록 이미 생성해둔 번호로 대체한다.
	return toLottoRecommendation(cycleStart, numbers, generatedAt)
}
