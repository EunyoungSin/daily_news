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
