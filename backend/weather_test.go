package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestClassifyKMAFailureReason은 fetchWeatherKMA/fetchKMACurrent가 실제로
// 적용하는 fmt.Errorf("...: %w", err) 래핑을 거쳐도 reason 태그가 살아남는지
// 검증한다 — err.Error()에 대한 단순 문자열 매칭은 래핑 문구가 바뀌면
// 쉽게 깨지므로, 대신 errors.Is 기반 동작을 고정(pin)해서 검증한다.
func TestClassifyKMAFailureReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing service key", fmt.Errorf("초단기실황 조회 실패: %w", errKMAServiceKeyMissing), "인증키 미설정"},
		{"deadline exceeded, doubly wrapped", fmt.Errorf("단기예보 조회 실패: %w", fmt.Errorf("Get url: %w", context.DeadlineExceeded)), "timeout"},
		{"other error", fmt.Errorf("기상청 API 오류(03): NO_DATA"), "API 오류"},
	}

	for _, c := range cases {
		if got := classifyKMAFailureReason(c.err); got != c.want {
			t.Errorf("%s: classifyKMAFailureReason(%v) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
}

// TestBuildForecastMidnightBoundary는 기능 스펙에서 명시적으로 언급하는
// 검증이다: 08:00/14:00 샘플은 배열상 자정 경계에 인접해 있다는 이유만으로
// 잘못된 날짜에 배정되지 않고, 반드시 올바른 날짜에 매칭되어야 한다.
func TestBuildForecastMidnightBoundary(t *testing.T) {
	// "Now"는 2026-07-28 늦은 시각이므로 today=07-28, tomorrow=07-29다.
	now := time.Date(2026, 7, 28, 23, 30, 0, 0, kst)

	times := []string{
		"2026-07-28T23:00", // 오늘, 늦은 밤 — 08:00도 14:00도 아님
		"2026-07-29T00:00", // 내일, 자정 — 08:00도 14:00도 아님
		"2026-07-29T08:00", // 내일 아침 (08:00)
		"2026-07-29T14:00", // 내일 오후 (14:00)
		"2026-07-28T08:00", // 오늘 아침 (08:00)
		"2026-07-28T14:00", // 오늘 오후 (14:00)
	}
	// index:                0     1     2     3     4     5
	temps := []float64{10.0, 11.0, 14.0, 21.0, 16.0, 26.0}
	codes := []int{0, 0, 3, 61, 0, 0}
	precip := []int{0, 0, 20, 90, 5, 5}

	forecast := buildForecast(times, temps, codes, precip, now)

	if got, want := forecast.Tomorrow.Morning.TemperatureC, 14.0; got != want {
		t.Errorf("tomorrow morning temp = %v, want %v (index 2, the 08:00 sample)", got, want)
	}
	if got, want := forecast.Tomorrow.Afternoon.TemperatureC, 21.0; got != want {
		t.Errorf("tomorrow afternoon temp = %v, want %v (index 3, the 14:00 sample)", got, want)
	}
	if got, want := forecast.Today.Morning.TemperatureC, 16.0; got != want {
		t.Errorf("today morning temp = %v, want %v (index 4, the 08:00 sample)", got, want)
	}
	if got, want := forecast.Today.Afternoon.TemperatureC, 26.0; got != want {
		t.Errorf("today afternoon temp = %v, want %v (index 5, the 14:00 sample)", got, want)
	}

	// 23:00/00:00 샘플(인덱스 0, 1)은 절대 선택되면 안 된다 — 만약 선택됐다면
	// 위 검증 중 하나가 어긋났을 것이다 (예: 00:00의 temp=11.0).
	if forecast.Tomorrow.Afternoon.PrecipProbability != 90 {
		t.Errorf("tomorrow afternoon precip = %d, want 90 (the 14:00 sample, index 3)", forecast.Tomorrow.Afternoon.PrecipProbability)
	}
	if forecast.Tomorrow.Afternoon.Description != "비" {
		t.Errorf("tomorrow afternoon description = %q, want %q (code 61 at 14:00, not the 08:00 clear reading)", forecast.Tomorrow.Afternoon.Description, "비")
	}
}

// TestBuildForecastAcrossYearBoundary는 (day-of-year 산술이 아니라)
// 날짜 문자열 비교 방식이 연도가 바뀌는 경계에서도 올바르게 동작하는지
// 검증한다.
func TestBuildForecastAcrossYearBoundary(t *testing.T) {
	now := time.Date(2026, 12, 31, 9, 0, 0, 0, kst)

	times := []string{"2027-01-01T08:00", "2027-01-01T14:00"}
	temps := []float64{5.0, 7.0}
	codes := []int{0, 0}
	precip := []int{0, 0}

	forecast := buildForecast(times, temps, codes, precip, now)

	if got, want := forecast.Tomorrow.Morning.TemperatureC, 5.0; got != want {
		t.Errorf("tomorrow morning temp across year boundary = %v, want %v", got, want)
	}
	if got, want := forecast.Tomorrow.Afternoon.TemperatureC, 7.0; got != want {
		t.Errorf("tomorrow afternoon temp across year boundary = %v, want %v", got, want)
	}
}

// TestBuildForecastMissingSample은 08:00/14:00 항목이 없을 때(예: 짧거나
// 잘못된 형태의 시간별 배열) panic이 나거나 엉뚱한 샘플을 고르는 대신
// PeriodForecast의 제로값으로 처리되는지 검증한다.
func TestBuildForecastMissingSample(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, kst)

	times := []string{"2026-07-28T08:00"} // 14:00 항목이 아예 없음
	temps := []float64{20.0}
	codes := []int{0}
	precip := []int{0}

	forecast := buildForecast(times, temps, codes, precip, now)

	if forecast.Today.Morning.TemperatureC != 20.0 || !forecast.Today.Morning.Available {
		t.Errorf("today morning = %+v, want temp 20 and Available:true", forecast.Today.Morning)
	}
	if (forecast.Today.Afternoon != PeriodForecast{}) {
		t.Errorf("today afternoon = %+v, want zero value (no 14:00 sample present)", forecast.Today.Afternoon)
	}
	if forecast.Today.Afternoon.Available {
		t.Error("today afternoon should have Available:false — no 14:00 sample means no data, not a real 0")
	}
}

// TestSuspiciousZeroTemp는 실제로 사용자가 겪었던 정확히 그 시나리오다:
// 누락되거나 잘못 파싱된 예보 슬롯이 조용히 0.0°C로 떨어지고, 브리핑이
// 마치 그것이 실제 값인 것처럼 "오전 8시엔 0도"라고 말해버리는 상황이다.
// 한국의 겨울철이 아닌 시기에 정확히 0.0이 나오면, 이는 실제 날씨라기보다
// 잘못된 값일 가능성이 높다고 판단해 처리한다.
func TestSuspiciousZeroTemp(t *testing.T) {
	july := time.Date(2026, 7, 29, 10, 0, 0, 0, kst)
	january := time.Date(2026, 1, 15, 10, 0, 0, 0, kst)

	if !suspiciousZeroTemp(0, july) {
		t.Error("0.0°C in July should be flagged as suspicious")
	}
	if suspiciousZeroTemp(0.1, july) {
		t.Error("0.1°C should never be flagged — only an exact 0.0 sentinel-shaped value")
	}
	if suspiciousZeroTemp(0, january) {
		t.Error("0.0°C in January (real Korean winter weather) should NOT be flagged")
	}
}

// TestSanityCheckDayForecastClearsImplausibleSwing은 보고된 또 다른
// 시나리오다: 같은 날의 오전/오후 값 차이가 너무 커서 실제 날씨보다는
// 잘못된 값(엉뚱한 슬롯, 파싱 오류)일 가능성이 더 높은 경우 — 이럴 때는
// 둘 중 하나를 믿는 대신 둘 다 폐기한다.
func TestSanityCheckDayForecastClearsImplausibleSwing(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, kst)

	day := DayForecast{
		Morning:   PeriodForecast{TemperatureC: 10, Available: true},
		Afternoon: PeriodForecast{TemperatureC: 30, Available: true}, // 20도 차이
	}
	sanityCheckDayForecast(&day, "daegu", "오늘", now)

	if day.Morning.Available || day.Afternoon.Available {
		t.Errorf("expected both periods cleared for a 20-degree same-day swing, got %+v", day)
	}
}

func TestSanityCheckDayForecastKeepsPlausibleSwing(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, kst)

	day := DayForecast{
		Morning:   PeriodForecast{TemperatureC: 20, Available: true},
		Afternoon: PeriodForecast{TemperatureC: 30, Available: true}, // 10도 차이, 있을 법한 값
	}
	sanityCheckDayForecast(&day, "daegu", "오늘", now)

	if !day.Morning.Available || !day.Afternoon.Available {
		t.Errorf("expected a plausible 10-degree swing to survive, got %+v", day)
	}
}

func TestSanityCheckDayForecastClearsSuspiciousZero(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, kst)

	day := DayForecast{
		Morning:   PeriodForecast{TemperatureC: 0, WeatherCode: 0, Description: "맑음", Available: true},
		Afternoon: PeriodForecast{TemperatureC: 32, Available: true},
	}
	sanityCheckDayForecast(&day, "daegu", "오늘", now)

	if day.Morning.Available {
		t.Errorf("expected a suspicious 0.0°C morning reading in July to be cleared, got %+v", day.Morning)
	}
	if !day.Afternoon.Available {
		t.Error("afternoon reading should be untouched")
	}
}

// TestWeatherCacheKeyIncludesPrefixAndNormalizesCity는 raw_data_cache가
// 날씨/환율/뉴스를 테이블 하나에서 공유하므로, 캐시 키에 "weather:" 접두사가
// 항상 붙어서 다른 데이터 종류와 절대 섞이지 않는지, 그리고 알 수 없는
// city 값이 normalizeCity를 거쳐 defaultCity로 정규화된 키가 되는지
// 확인한다 — 캐시 히트/미스 자체(DB 연동)는 이 프로젝트의 다른 DB
// 캐시들과 마찬가지로 실제 서버로 라이브 검증한다(raw_data_cache.go의
// isRawCacheFresh 문서 주석 참고).
func TestWeatherCacheKeyIncludesPrefixAndNormalizesCity(t *testing.T) {
	if got := weatherCacheKey("seoul"); got != "weather:seoul" {
		t.Errorf("weatherCacheKey(seoul) = %q, want %q", got, "weather:seoul")
	}
	if got, want := weatherCacheKey("존재하지-않는-도시"), "weather:"+defaultCity; got != want {
		t.Errorf("weatherCacheKey(unknown) = %q, want %q (normalizeCity falls back to defaultCity)", got, want)
	}
}
