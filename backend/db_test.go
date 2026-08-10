package main

import (
	"errors"
	"testing"
)

func TestClassifyDBErrorTypeNilIsEmpty(t *testing.T) {
	if got := classifyDBErrorType(nil); got != "" {
		t.Errorf("classifyDBErrorType(nil) = %q, want empty string", got)
	}
}

func TestClassifyDBErrorTypeDetectsTursoOutagePatterns(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"502 Bad Gateway literal", errors.New("received 502 Bad Gateway from upstream")},
		{"upstream forward failed literal", errors.New("libsql: upstream forward failed")},
		{"other 5xx status code", errors.New("libsql: request failed with status 503 Service Unavailable")},
		{"5xx wrapped in Go error chain", errors.New(`failed to connect: Post "https://x.turso.io": 500 Internal Server Error`)},
		{"mixed case", errors.New("Upstream Forward Failed: connection reset")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDBErrorType(tc.err); got != dbErrorTypeTursoOutage {
				t.Errorf("classifyDBErrorType(%q) = %q, want %q", tc.err, got, dbErrorTypeTursoOutage)
			}
		})
	}
}

func TestClassifyDBErrorTypeDetectsOrdinaryConnectionFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"auth token rejected", errors.New("401 Unauthorized: invalid auth token")},
		{"dns/dial failure", errors.New("dial tcp: lookup db.turso.io: no such host")},
		{"context timeout", errors.New("context deadline exceeded")},
		{"malformed DSN", errors.New("invalid TURSO_DATABASE_URL: missing scheme")},
		{"local file permission error", errors.New("failed to create local db directory data: permission denied")},
		{"port number should not be mistaken for a 5xx status", errors.New("dial tcp 127.0.0.1:5000: connect: connection refused")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDBErrorType(tc.err); got != dbErrorTypeConnectionFailed {
				t.Errorf("classifyDBErrorType(%q) = %q, want %q", tc.err, got, dbErrorTypeConnectionFailed)
			}
		})
	}
}

// TestCurrentDBErrorTypeFallsBackWhenUnset은 setDBErrorType이 아직 호출되지
// 않았거나 빈 문자열로 초기화된 상태에서도, db==nil인 동안 이 값이 조용히
// "정상"으로 오인되지 않도록 dbErrorTypeConnectionFailed로 안전하게
// 폴백하는지 확인한다.
func TestCurrentDBErrorTypeFallsBackWhenUnset(t *testing.T) {
	setDBErrorType("")
	if got := currentDBErrorType(); got != dbErrorTypeConnectionFailed {
		t.Errorf("currentDBErrorType() with unset state = %q, want fallback %q", got, dbErrorTypeConnectionFailed)
	}
}

func TestSetAndCurrentDBErrorTypeRoundTrip(t *testing.T) {
	setDBErrorType(dbErrorTypeTursoOutage)
	if got := currentDBErrorType(); got != dbErrorTypeTursoOutage {
		t.Errorf("currentDBErrorType() = %q, want %q", got, dbErrorTypeTursoOutage)
	}

	setDBErrorType(dbErrorTypeConnectionFailed)
	if got := currentDBErrorType(); got != dbErrorTypeConnectionFailed {
		t.Errorf("currentDBErrorType() = %q, want %q", got, dbErrorTypeConnectionFailed)
	}
}
