package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTheoreticalLatestDrwNo(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want int
	}{
		{"first draw day", time.Date(2002, 12, 7, 21, 0, 0, 0, kst), 1},
		{"one week later", time.Date(2002, 12, 14, 21, 0, 0, 0, kst), 2},
		{"saturday before draw hour still counts previous round", time.Date(2025, 8, 9, 10, 0, 0, 0, kst), 1183},
		{"saturday after draw hour counts that round", time.Date(2025, 8, 9, 22, 0, 0, 0, kst), 1184},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := theoreticalLatestDrwNo(c.now); got != c.want {
				t.Errorf("theoreticalLatestDrwNo(%s) = %d, want %d", c.now.Format(time.RFC3339), got, c.want)
			}
		})
	}
}

// TestLottoSeedJSONIsWellFormed는 backend/data/lotto_seed.json 자체가
// 유효한 JSON 배열이라는 것과, 안에 든 항목이 있다면 전부
// validateLottoEntry를 통과한다는 것을 회귀 테스트로 고정한다 — 직접
// 손으로 편집하는 정적 파일이라 오타로 깨지기 쉽다.
func TestLottoSeedJSONIsWellFormed(t *testing.T) {
	var seed []lottoEntryInput
	if err := json.Unmarshal(lottoSeedJSON, &seed); err != nil {
		t.Fatalf("lotto_seed.json is not valid JSON: %v", err)
	}
	for _, e := range seed {
		if err := validateLottoEntry(e); err != nil {
			t.Errorf("seed entry drwNo=%d failed validation: %v", e.DrwNo, err)
		}
	}
}

// TestParseGitHubLottoDrawConvertsValidResponse는 smok95/lotto가 실제로
// 응답하는 형태(divisions/total_sales_amount 등 우리가 안 쓰는 필드
// 포함)를 넣었을 때, dhlotteryResponse와 정확히 같은 모양으로 변환되는지
// 확인한다 — 실제 results/1236.json 응답을 그대로 옮겨왔다.
func TestParseGitHubLottoDrawConvertsValidResponse(t *testing.T) {
	body := []byte(`{
		"draw_no": 1236,
		"numbers": [12, 18, 21, 29, 34, 38],
		"bonus_no": 10,
		"date": "2026-08-08T00:00:00Z",
		"divisions": [{"prize": 2441919375, "winners": 11}],
		"total_sales_amount": 114070835000,
		"winners_combination": {"auto": 5, "semi_auto": 1, "manual": 5}
	}`)

	got, err := parseGitHubLottoDraw(body, 1236)
	if err != nil {
		t.Fatalf("parseGitHubLottoDraw() error = %v", err)
	}

	want := &dhlotteryResponse{
		ReturnValue: "success",
		DrwNo:       1236,
		DrwNoDate:   "2026-08-08",
		DrwtNo1:     12, DrwtNo2: 18, DrwtNo3: 21, DrwtNo4: 29, DrwtNo5: 34, DrwtNo6: 38,
		BnusNo: 10,
	}
	if *got != *want {
		t.Errorf("parseGitHubLottoDraw() = %+v, want %+v", *got, *want)
	}
}

func TestParseGitHubLottoDrawRejectsMismatchedDrawNo(t *testing.T) {
	body := []byte(`{"draw_no": 1235, "numbers": [1,2,3,4,5,6], "bonus_no": 7, "date": "2026-08-01T00:00:00Z"}`)
	if _, err := parseGitHubLottoDraw(body, 1236); err == nil {
		t.Error("expected an error when draw_no in the response doesn't match the requested round, got nil")
	}
}

func TestParseGitHubLottoDrawRejectsWrongNumberCount(t *testing.T) {
	body := []byte(`{"draw_no": 1236, "numbers": [1,2,3,4,5], "bonus_no": 7, "date": "2026-08-08T00:00:00Z"}`)
	if _, err := parseGitHubLottoDraw(body, 1236); err == nil {
		t.Error("expected an error when numbers has fewer than 6 entries, got nil")
	}
}

func TestParseGitHubLottoDrawRejectsMalformedDate(t *testing.T) {
	body := []byte(`{"draw_no": 1236, "numbers": [1,2,3,4,5,6], "bonus_no": 7, "date": "2026-08-08"}`)
	if _, err := parseGitHubLottoDraw(body, 1236); err == nil {
		t.Error("expected an error for a non-RFC3339 date, got nil")
	}
}

func TestParseGitHubLottoDrawRejectsInvalidJSON(t *testing.T) {
	if _, err := parseGitHubLottoDraw([]byte("not json"), 1236); err == nil {
		t.Error("expected an error for malformed JSON, got nil")
	}
}

// TestSeedLottoDrawsIfEmptyNilDB는 다른 DB 캐시들과 동일한 nil-DB 안전성
// 보장을 검증한다 — DB가 설정되지 않은 상태로 서버가 실행돼도 패닉하거나
// 네트워크(이 경우 임베드된 파일 파싱뿐이라 네트워크 자체가 없지만)에
// 접근하지 않아야 한다.
func TestSeedLottoDrawsIfEmptyNilDB(t *testing.T) {
	if err := seedLottoDrawsIfEmpty(context.Background(), nil); err != nil {
		t.Errorf("expected seedLottoDrawsIfEmpty against a nil db to no-op without error, got %v", err)
	}
}

// TestInsertLottoDrawSetsCollectedAt은 자동 수집이 실제로 회차를 저장할
// 때마다 collected_at이 "지금"으로 채워지는지 확인한다 — checkForNewLottoRound
// 와 catchUpMissingLottoRounds 둘 다 신규 회차 저장에 insertLottoDraw
// 하나만 쓰므로, 이 테스트 하나가 두 경로 모두를 커버한다.
func TestInsertLottoDrawSetsCollectedAt(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	before := time.Now()
	data := &dhlotteryResponse{
		DrwNo: 1, DrwNoDate: "2002-12-07",
		DrwtNo1: 1, DrwtNo2: 2, DrwtNo3: 3, DrwtNo4: 4, DrwtNo5: 5, DrwtNo6: 6, BnusNo: 7,
	}
	if err := insertLottoDraw(ctx, conn, data); err != nil {
		t.Fatalf("insertLottoDraw: %v", err)
	}
	after := time.Now()

	got, err := queryLottoLastCollectedAt(ctx, conn)
	if err != nil {
		t.Fatalf("queryLottoLastCollectedAt: %v", err)
	}
	if got.IsZero() {
		t.Fatal("expected a non-zero collected_at right after insertLottoDraw succeeded")
	}
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("collected_at = %v, want a time between %v and %v", got, before, after)
	}
}

// TestInsertLottoDrawKeepsFirstCollectedAtOnConflict는 이미 저장된 회차를
// (예: 정기 점검이 같은 회차를 또 조회해) 다시 insertLottoDraw로 넣어도
// ON CONFLICT DO NOTHING이라 collected_at을 포함해 아무 컬럼도 덮어쓰지
// 않는지 확인한다 — 최초로 실제 수집에 성공한 시각이 계속 보존되어야
// 한다.
func TestInsertLottoDrawKeepsFirstCollectedAtOnConflict(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	data := &dhlotteryResponse{
		DrwNo: 1, DrwNoDate: "2002-12-07",
		DrwtNo1: 1, DrwtNo2: 2, DrwtNo3: 3, DrwtNo4: 4, DrwtNo5: 5, DrwtNo6: 6, BnusNo: 7,
	}
	if err := insertLottoDraw(ctx, conn, data); err != nil {
		t.Fatalf("first insertLottoDraw: %v", err)
	}
	first, err := queryLottoLastCollectedAt(ctx, conn)
	if err != nil {
		t.Fatalf("queryLottoLastCollectedAt: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := insertLottoDraw(ctx, conn, data); err != nil {
		t.Fatalf("second insertLottoDraw: %v", err)
	}
	second, err := queryLottoLastCollectedAt(ctx, conn)
	if err != nil {
		t.Fatalf("queryLottoLastCollectedAt: %v", err)
	}

	if !first.Equal(second) {
		t.Errorf("collected_at changed on conflict: first=%v, second=%v (should be preserved)", first, second)
	}
}

// TestLottoCollectionStatusSnapshotReflectsCollectedAtAfterSimulatedRestart는
// 실제 버그였던 시나리오를 재현한다: 서버가 재시작되면 lottoCollectionState
// (메모리)는 항상 제로 값으로 새로 시작하지만, DB에는 이미 회차가
// 저장되어 있다. 이 테스트는 lottoCollectionState를 전혀 건드리지 않은
// (즉 방금 부팅한 프로세스와 동일한) 상태에서 lottoCollectionStatusSnapshot
// 을 호출해도, LastCollectedAt이 DB의 collected_at으로부터 정확히
// 채워지는지 확인한다 — "마지막 성공: 아직 없음"이 영구히 표시되던
// 버그의 회귀 테스트다.
func TestLottoCollectionStatusSnapshotReflectsCollectedAtAfterSimulatedRestart(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	data := &dhlotteryResponse{
		DrwNo: 1237, DrwNoDate: "2026-08-15",
		DrwtNo1: 10, DrwtNo2: 20, DrwtNo3: 23, DrwtNo4: 34, DrwtNo5: 37, DrwtNo6: 40, BnusNo: 36,
	}
	if err := insertLottoDraw(ctx, conn, data); err != nil {
		t.Fatalf("insertLottoDraw: %v", err)
	}

	// lottoCollectionState는 패키지 전역 상태라 다른 테스트가 이미 건드렸을
	// 수 있으므로, "방금 부팅한 프로세스"를 흉내내기 위해 명시적으로
	// 제로 값으로 되돌려둔다.
	lottoCollectionState.mu.Lock()
	lottoCollectionState.running = false
	lottoCollectionState.lastCheckedAt = time.Time{}
	lottoCollectionState.nextCheckAt = time.Time{}
	lottoCollectionState.mu.Unlock()

	status, err := lottoCollectionStatusSnapshot(ctx, conn)
	if err != nil {
		t.Fatalf("lottoCollectionStatusSnapshot: %v", err)
	}
	if status.LastCollectedAt == "" {
		t.Fatal(`LastCollectedAt is empty — regressed to the "마지막 성공: 아직 없음" bug after a simulated restart`)
	}
	parsed, err := time.Parse(time.RFC3339, status.LastCollectedAt)
	if err != nil {
		t.Fatalf("LastCollectedAt %q is not valid RFC3339: %v", status.LastCollectedAt, err)
	}
	if time.Since(parsed) > time.Minute {
		t.Errorf("LastCollectedAt = %v, expected close to now (just inserted)", parsed)
	}
}

// TestBackfillLottoDrawsCollectedAtFillsPreExistingRows는 이 컬럼이
// 추가되기 전부터 있던(collected_at이 NULL인) 행에 대해, 백필이 그
// 회차의 drw_date에 lottoDrawHourKST를 적용한 값을 채우는지 확인한다.
func TestBackfillLottoDrawsCollectedAtFillsPreExistingRows(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	// insertLottoDraw를 거치지 않고 레거시 행을 직접 심는다 — collected_at
	// 컬럼 자체가 없던 시절 저장된 행을 흉내낸다.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO lotto_draws (drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no)
		VALUES (1, '2002-12-07', 1, 2, 3, 4, 5, 6, 7)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := backfillLottoDrawsCollectedAt(conn); err != nil {
		t.Fatalf("backfillLottoDrawsCollectedAt: %v", err)
	}

	got, err := queryLottoLastCollectedAt(ctx, conn)
	if err != nil {
		t.Fatalf("queryLottoLastCollectedAt: %v", err)
	}
	want := time.Date(2002, 12, 7, lottoDrawHourKST, 0, 0, 0, kst)
	if !got.Equal(want) {
		t.Errorf("backfilled collected_at = %v, want %v (drw_date at lottoDrawHourKST KST)", got, want)
	}
}

// TestBackfillLottoDrawsCollectedAtDoesNotOverwriteExistingValues는 백필이
// (여러 번 호출되거나, migrate()가 서버 재시작마다 다시 실행되어도) 이미
// 실제로 기록된 collected_at을 추정치로 덮어쓰지 않는지 확인한다.
func TestBackfillLottoDrawsCollectedAtDoesNotOverwriteExistingValues(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	data := &dhlotteryResponse{
		DrwNo: 1, DrwNoDate: "2002-12-07",
		DrwtNo1: 1, DrwtNo2: 2, DrwtNo3: 3, DrwtNo4: 4, DrwtNo5: 5, DrwtNo6: 6, BnusNo: 7,
	}
	if err := insertLottoDraw(ctx, conn, data); err != nil {
		t.Fatalf("insertLottoDraw: %v", err)
	}
	realCollectedAt, err := queryLottoLastCollectedAt(ctx, conn)
	if err != nil {
		t.Fatalf("queryLottoLastCollectedAt: %v", err)
	}

	if err := backfillLottoDrawsCollectedAt(conn); err != nil {
		t.Fatalf("backfillLottoDrawsCollectedAt: %v", err)
	}

	afterBackfill, err := queryLottoLastCollectedAt(ctx, conn)
	if err != nil {
		t.Fatalf("queryLottoLastCollectedAt: %v", err)
	}
	if !realCollectedAt.Equal(afterBackfill) {
		t.Errorf("backfill overwrote a real collected_at: was %v, now %v", realCollectedAt, afterBackfill)
	}
}

// TestQueryLottoLastCollectedAtOrdersByDrwNoNotStringComparison는
// collected_at 문자열의 표기 형식이 회차마다 다를 때(실수집=UTC "Z"
// 접미사, 백필=KST "+09:00" 오프셋)도 drw_no 기준으로 정확히 최신 회차를
// 고르는지 확인한다 — collected_at 문자열을 그대로 비교(MAX/ORDER BY)
// 하면 표기 형식 차이 때문에 시간 순서가 뒤집힐 수 있다는 것이 이
// 함수를 만든 이유였다.
func TestQueryLottoLastCollectedAtOrdersByDrwNoNotStringComparison(t *testing.T) {
	conn := openTempLottoTestDB(t)
	ctx := context.Background()

	// 회차 1: UTC "Z" 접미사, 시각상으로는 회차 2보다 미래(문자열 비교로는
	// 회차 2보다 사전순으로 더 크게 나온다 — "2026-08-20T..." > "2026-08-15T...").
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO lotto_draws (drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no, collected_at)
		VALUES (1, '2026-08-01', 1, 2, 3, 4, 5, 6, 7, '2026-08-20T00:00:00Z')`); err != nil {
		t.Fatalf("seed round 1: %v", err)
	}
	// 회차 2(더 최신 회차): KST "+09:00" 오프셋이지만, 실제로는 회차 1보다
	// *이전* 문자열이다("2026-08-15..." < "2026-08-20...") — 문자열 비교로
	// 고르면 틀리게 회차 1을 "더 최근"으로 오판한다.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO lotto_draws (drw_no, drw_date, num1, num2, num3, num4, num5, num6, bonus_no, collected_at)
		VALUES (2, '2026-08-08', 8, 9, 10, 11, 12, 13, 14, '2026-08-15T21:00:00+09:00')`); err != nil {
		t.Fatalf("seed round 2: %v", err)
	}

	got, err := queryLottoLastCollectedAt(ctx, conn)
	if err != nil {
		t.Fatalf("queryLottoLastCollectedAt: %v", err)
	}
	want, _ := time.Parse(time.RFC3339, "2026-08-15T21:00:00+09:00")
	if !got.Equal(want) {
		t.Errorf("queryLottoLastCollectedAt = %v, want %v (round 2's own collected_at, since round 2 has the higher drw_no)", got, want)
	}
}
