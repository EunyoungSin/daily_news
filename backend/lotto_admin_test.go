package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateLottoEntry(t *testing.T) {
	valid := lottoEntryInput{DrwNo: 1187, DrwDate: "2025-08-16", Numbers: []int{3, 12, 19, 27, 33, 41}, Bonus: 7}
	if err := validateLottoEntry(valid); err != nil {
		t.Errorf("expected a well-formed entry to pass validation, got %v", err)
	}

	cases := []struct {
		name  string
		entry lottoEntryInput
	}{
		{"drwNo zero", lottoEntryInput{DrwNo: 0, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 6}, Bonus: 7}},
		{"drwNo negative", lottoEntryInput{DrwNo: -1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 6}, Bonus: 7}},
		{"malformed date", lottoEntryInput{DrwNo: 1, DrwDate: "2025/08/16", Numbers: []int{1, 2, 3, 4, 5, 6}, Bonus: 7}},
		{"invalid date", lottoEntryInput{DrwNo: 1, DrwDate: "not-a-date", Numbers: []int{1, 2, 3, 4, 5, 6}, Bonus: 7}},
		{"too few numbers", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5}, Bonus: 7}},
		{"too many numbers", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 6, 7}, Bonus: 8}},
		{"number below range", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{0, 2, 3, 4, 5, 6}, Bonus: 7}},
		{"number above range", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 46}, Bonus: 7}},
		{"duplicate numbers", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 5}, Bonus: 7}},
		{"bonus below range", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 6}, Bonus: 0}},
		{"bonus above range", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 6}, Bonus: 46}},
		{"bonus overlaps numbers", lottoEntryInput{DrwNo: 1, DrwDate: "2025-08-16", Numbers: []int{1, 2, 3, 4, 5, 6}, Bonus: 6}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateLottoEntry(c.entry); err == nil {
				t.Errorf("expected validation to fail for %+v", c.entry)
			}
		})
	}
}

func TestRequireAdminKey(t *testing.T) {
	t.Run("missing ADMIN_SECRET_KEY disables the endpoint entirely", func(t *testing.T) {
		t.Setenv("ADMIN_SECRET_KEY", "")
		req := httptest.NewRequest(http.MethodPost, "/api/admin/lotto/manual-entry", nil)
		req.Header.Set(adminKeyHeader, "anything")
		rec := httptest.NewRecorder()
		if requireAdminKey(rec, req) {
			t.Error("expected requireAdminKey to fail when ADMIN_SECRET_KEY is unset")
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})

	t.Run("wrong key is rejected", func(t *testing.T) {
		t.Setenv("ADMIN_SECRET_KEY", "correct-secret")
		req := httptest.NewRequest(http.MethodPost, "/api/admin/lotto/manual-entry", nil)
		req.Header.Set(adminKeyHeader, "wrong-secret")
		rec := httptest.NewRecorder()
		if requireAdminKey(rec, req) {
			t.Error("expected requireAdminKey to fail for a wrong key")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("missing header is rejected", func(t *testing.T) {
		t.Setenv("ADMIN_SECRET_KEY", "correct-secret")
		req := httptest.NewRequest(http.MethodPost, "/api/admin/lotto/manual-entry", nil)
		rec := httptest.NewRecorder()
		if requireAdminKey(rec, req) {
			t.Error("expected requireAdminKey to fail when the header is absent")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("correct key is accepted", func(t *testing.T) {
		t.Setenv("ADMIN_SECRET_KEY", "correct-secret")
		req := httptest.NewRequest(http.MethodPost, "/api/admin/lotto/manual-entry", nil)
		req.Header.Set(adminKeyHeader, "correct-secret")
		rec := httptest.NewRecorder()
		if !requireAdminKey(rec, req) {
			t.Errorf("expected requireAdminKey to succeed for the correct key, got status %d", rec.Code)
		}
	})
}

// TestLottoManualEntryHandlerRejectsWrongMethod은 DB 없이도 확인 가능한
// 부분(메서드 검사가 인증보다 먼저 실행됨)을 검증한다.
func TestLottoManualEntryHandlerRejectsWrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/lotto/manual-entry", nil)
	rec := httptest.NewRecorder()
	lottoManualEntryHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for a non-POST request, got %d", rec.Code)
	}
}

// TestLottoManualEntryHandlerRejectsWrongAdminKey는 DB 연결 없이도(db가
// nil인 테스트 환경에서도) 인증이 DB 접근보다 먼저 걸러내는지 확인한다.
func TestLottoManualEntryHandlerRejectsWrongAdminKey(t *testing.T) {
	t.Setenv("ADMIN_SECRET_KEY", "correct-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/admin/lotto/manual-entry", nil)
	req.Header.Set(adminKeyHeader, "wrong-secret")
	rec := httptest.NewRecorder()
	lottoManualEntryHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for a wrong admin key, got %d", rec.Code)
	}
}
