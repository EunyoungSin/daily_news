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

// TestSeedLottoDrawsIfEmptyNilDB는 다른 DB 캐시들과 동일한 nil-DB 안전성
// 보장을 검증한다 — DB가 설정되지 않은 상태로 서버가 실행돼도 패닉하거나
// 네트워크(이 경우 임베드된 파일 파싱뿐이라 네트워크 자체가 없지만)에
// 접근하지 않아야 한다.
func TestSeedLottoDrawsIfEmptyNilDB(t *testing.T) {
	if err := seedLottoDrawsIfEmpty(context.Background(), nil); err != nil {
		t.Errorf("expected seedLottoDrawsIfEmpty against a nil db to no-op without error, got %v", err)
	}
}
