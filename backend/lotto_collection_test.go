package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newGitHubLottoTestServer는 lottoGitHubDatasetBaseURL/{drwNo}.json 형태의
// 요청에 응답하는 가짜 서버를 띄우고, 테스트가 끝나면 원래 값으로 되돌린다
// — catchUpMissingLottoRounds/checkForNewLottoRound가 실제
// raw.githubusercontent.com을 두드리지 않고도 검증되도록 한다. available에
// 없는 회차는 404를 반환해 "그 회차 파일이 데이터셋에 없음"을 재현한다.
func newGitHubLottoTestServer(t *testing.T, available map[int]bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var drwNo int
		if _, err := fmt.Sscanf(r.URL.Path, "/%d.json", &drwNo); err != nil || !available[drwNo] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"draw_no":  drwNo,
			"numbers":  []int{1, 2, 3, 4, 5, 6},
			"bonus_no": 7,
			"date":     "2026-01-01T00:00:00Z",
		})
	}))
	t.Cleanup(srv.Close)

	original := lottoGitHubDatasetBaseURL
	lottoGitHubDatasetBaseURL = srv.URL
	t.Cleanup(func() { lottoGitHubDatasetBaseURL = original })
}

// shrinkLottoCatchUpDelaysForTest는 lottoCatchUpRoundDelay/
// lottoCatchUpRetryDelays를 테스트 동안만 1ms 수준으로 줄여서, 실제로는
// 초 단위인 재시도/간격 대기 때문에 테스트가 느려지지 않게 한다.
func shrinkLottoCatchUpDelaysForTest(t *testing.T) {
	t.Helper()
	origDelay := lottoCatchUpRoundDelay
	origRetries := lottoCatchUpRetryDelays
	lottoCatchUpRoundDelay = time.Millisecond
	lottoCatchUpRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() {
		lottoCatchUpRoundDelay = origDelay
		lottoCatchUpRetryDelays = origRetries
	})
}

// TestCatchUpMissingLottoRoundsFillsSequentiallyAndSkipsFailures는 DB
// 최신 회차가 몇 주치 밀려있는 상태(20~30회차 밀린 상태를 흉내내는 5회차
// 규모로 축소)에서 catchUpMissingLottoRounds를 실행하면, 그 사이 회차
// 전부가 오래된 순서대로 채워지는지 확인한다. 그중 한 회차는 데이터셋에
// 없는 것으로 만들어(404), 그 회차 하나가 실패해도 나머지 회차는 계속
// 처리되는지도 함께 확인한다.
func TestCatchUpMissingLottoRoundsFillsSequentiallyAndSkipsFailures(t *testing.T) {
	conn := openTempLottoTestDB(t)
	shrinkLottoCatchUpDelaysForTest(t)

	theoretical := theoreticalLatestDrwNo(time.Now())
	const pendingCount = 5
	latestInDB := theoretical - pendingCount
	seedLottoDrawForTest(t, conn, latestInDB, "2020-01-01", []int{10, 11, 12, 13, 14, 15}, 16)

	missingRound := latestInDB + 3
	available := make(map[int]bool)
	for n := latestInDB + 1; n <= theoretical; n++ {
		if n != missingRound {
			available[n] = true
		}
	}
	newGitHubLottoTestServer(t, available)

	catchUpMissingLottoRounds(context.Background(), conn)

	for n := latestInDB + 1; n <= theoretical; n++ {
		var count int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM lotto_draws WHERE drw_no = ?`, n).Scan(&count); err != nil {
			t.Fatalf("query drw_no=%d: %v", n, err)
		}
		want := 1
		if n == missingRound {
			want = 0
		}
		if count != want {
			t.Errorf("drw_no=%d: got saved count %d, want %d", n, count, want)
		}
	}

	lottoCollectionState.mu.Lock()
	catchingUp := lottoCollectionState.catchingUp
	processed := lottoCollectionState.processedCount
	total := lottoCollectionState.totalPendingCount
	nextCheckAt := lottoCollectionState.nextCheckAt
	lottoCollectionState.mu.Unlock()

	if catchingUp {
		t.Error("expected catchingUp to be reset to false once the catch-up finishes")
	}
	if processed != 0 || total != 0 {
		t.Errorf("expected processedCount/totalPendingCount reset to 0 after completion, got %d/%d", processed, total)
	}
	if nextCheckAt.IsZero() {
		t.Error("expected nextCheckAt to be set after the catch-up finishes")
	}
}

// TestCatchUpMissingLottoRoundsNoopWhenUpToDate는 밀린 회차가 없는(평소)
// 상태에서 catchUpMissingLottoRounds를 실행해도 catchingUp이 켜지지 않고,
// checkForNewLottoRound와 마찬가지로 lastCheckedAt/nextCheckAt만 갱신되는지
// 확인한다 — 평상시 정기 점검 동작이 그대로 유지되는지에 대한 회귀 테스트다.
func TestCatchUpMissingLottoRoundsNoopWhenUpToDate(t *testing.T) {
	conn := openTempLottoTestDB(t)
	shrinkLottoCatchUpDelaysForTest(t)

	theoretical := theoreticalLatestDrwNo(time.Now())
	seedLottoDrawForTest(t, conn, theoretical, "2020-01-01", []int{10, 11, 12, 13, 14, 15}, 16)

	catchUpMissingLottoRounds(context.Background(), conn)

	lottoCollectionState.mu.Lock()
	catchingUp := lottoCollectionState.catchingUp
	nextCheckAt := lottoCollectionState.nextCheckAt
	lottoCollectionState.mu.Unlock()

	if catchingUp {
		t.Error("expected catchingUp to stay false when nothing is pending")
	}
	if nextCheckAt.IsZero() {
		t.Error("expected nextCheckAt to be set even when nothing is pending")
	}

	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM lotto_draws`).Scan(&count); err != nil {
		t.Fatalf("count lotto_draws: %v", err)
	}
	if count != 1 {
		t.Errorf("expected no new rounds to be inserted, got %d rows", count)
	}
}

func TestLottoAutoCollectionDefaultOn(t *testing.T) {
	cases := []struct {
		envVal string
		want   bool
	}{
		{"", true},
		{"on", true},
		{"ON", true},
		{"off", false},
		{"OFF", false},
		{" off ", false},
		{"garbage", true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("env=%q", c.envVal), func(t *testing.T) {
			t.Setenv(lottoAutoCollectionDefaultEnvVar, c.envVal)
			if got := lottoAutoCollectionDefaultOn(); got != c.want {
				t.Errorf("lottoAutoCollectionDefaultOn() with env=%q = %v, want %v", c.envVal, got, c.want)
			}
		})
	}
}
