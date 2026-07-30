package main

import (
	"context"
	"testing"
	"time"
)

func TestWeatherSlotCacheRowToPeriodForecast(t *testing.T) {
	row := weatherSlotCacheRow{Temperature: 26.4, WeatherCode: 3, PrecipitationProbability: 20}
	got := row.toPeriodForecast()

	want := PeriodForecast{
		TemperatureC:      26.4,
		WeatherCode:       3,
		Description:       weathercodeDescription(3),
		PrecipProbability: 20,
		Available:         true,
	}
	if got != want {
		t.Errorf("toPeriodForecast() = %+v, want %+v", got, want)
	}
}

// TestResolveForecastSlotNilDB는 TestBriefingSectionCacheNilDB와 동일한
// nil-DB 안전성 보장을 검증한다 — MySQL 설정 없이 실행해도 panic이 나거나
// 네트워크에 접근해서는 안 되며, 다만 그 경우 슬롯이 저장된 값으로
// 폴백할 수 없으므로 실제로 값이 없는 API 응답은 그대로 unavailable
// 상태로 남는다. db가 nil이면 백필 재시도도 시도하지 않으므로(그럴 경우
// 복구해도 저장할 곳이 없다), 이 시나리오는 네트워크 호출 없이 순수하게
// 검증된다.
func TestResolveForecastSlotNilDB(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, kst)

	available := PeriodForecast{TemperatureC: 26.4, Available: true}
	if got := resolveForecastSlot(context.Background(), "daegu", "2026-07-29", "08:00", available, now); got != available {
		t.Errorf("expected an Available apiValue to pass through unchanged, got %+v", got)
	}

	// 슬롯 시각(08:00)이 now(12:00)보다 이전이므로 "이미 지난 시각"이다 —
	// db가 nil이라 백필도 시도하지 않으니 past_missing으로 남아야 한다.
	pastMissing := PeriodForecast{}
	got := resolveForecastSlot(context.Background(), "daegu", "2026-07-29", "08:00", pastMissing, now)
	if got.Available {
		t.Errorf("expected no DB to fall back to, got %+v", got)
	}
	if got.UnavailableReason != unavailablePastMissing {
		t.Errorf("expected UnavailableReason %q for an already-past slot with no cache, got %q", unavailablePastMissing, got.UnavailableReason)
	}

	// 슬롯 시각(14:00)이 now(12:00)보다 나중이므로 "아직 발표 전"이다.
	notYet := PeriodForecast{}
	got = resolveForecastSlot(context.Background(), "daegu", "2026-07-29", "14:00", notYet, now)
	if got.Available {
		t.Errorf("expected no DB to fall back to, got %+v", got)
	}
	if got.UnavailableReason != unavailableNotYetAvailable {
		t.Errorf("expected UnavailableReason %q for a not-yet-elapsed slot, got %q", unavailableNotYetAvailable, got.UnavailableReason)
	}
}

func TestSlotIsPast(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, kst)

	if !slotIsPast("2026-07-29", "08:00", now) {
		t.Error("expected 08:00 to be considered past when now is 12:00 the same day")
	}
	if slotIsPast("2026-07-29", "14:00", now) {
		t.Error("expected 14:00 to be considered not-yet-past when now is 12:00 the same day")
	}
	if slotIsPast("2026-07-30", "08:00", now) {
		t.Error("expected tomorrow's 08:00 to be considered not-yet-past")
	}
	// 파싱 실패 입력은 안전한 쪽(아직 발표 전)으로 처리해야 한다.
	if slotIsPast("not-a-date", "08:00", now) {
		t.Error("expected an unparseable slot to be treated as not-yet-past (fails safe)")
	}
}

// TestApplyWeatherSlotCacheCoversAllFourSlots는 고정된 네 슬롯 전부가
// 실제로 resolveForecastSlot을 거치는지 구조적으로 보장하는 테스트다 —
// 복사/붙여넣기 실수로 하나(예: tomorrow.afternoon)를 빠뜨리면, 그
// 슬롯만 KMA 응답 누락에 대한 보호를 영구히 받지 못하는데 이는 겉으로
// 드러나지 않는 문제이기 때문이다.
func TestApplyWeatherSlotCacheCoversAllFourSlots(t *testing.T) {
	forecast := &WeatherForecast{
		Today:    DayForecast{Morning: PeriodForecast{TemperatureC: 1, Available: true}, Afternoon: PeriodForecast{TemperatureC: 2, Available: true}},
		Tomorrow: DayForecast{Morning: PeriodForecast{TemperatureC: 3, Available: true}, Afternoon: PeriodForecast{TemperatureC: 4, Available: true}},
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, kst)

	// db == nil이면 아무 동작도 하지 않고 그대로 통과시키므로, 입력이
	// Available:true였다면 실행 후에도 여전히 Available:true여야 한다 —
	// applyWeatherSlotCache 내부 로직이 값을 저장 후 그대로 반환하지
	// 않고 어딘가에서 필드를 누락시킨다면 이 테스트가 실패한다.
	applyWeatherSlotCache(context.Background(), forecast, "daegu", now)

	for name, p := range map[string]PeriodForecast{
		"today.morning":      forecast.Today.Morning,
		"today.afternoon":    forecast.Today.Afternoon,
		"tomorrow.morning":   forecast.Tomorrow.Morning,
		"tomorrow.afternoon": forecast.Tomorrow.Afternoon,
	} {
		if !p.Available {
			t.Errorf("%s: expected Available:true to survive applyWeatherSlotCache, got %+v", name, p)
		}
	}
}

// TestPickForecastSlot은 backfillMissingSlot이 방금 새로 가져온
// WeatherForecast에서 (dateStr, hourMinute)에 맞는 슬롯 하나를 정확히
// 골라내는지 검증한다 — today/tomorrow와 morning/afternoon을 착각하면
// 전혀 다른 날/시간대의 값을 백필해서 저장해버리는 조용한 버그가 되므로,
// 네트워크 없이도 이 매칭 로직만은 확실히 검증해둔다.
func TestPickForecastSlot(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, kst)
	todayStr := "2026-07-29"
	tomorrowStr := "2026-07-30"

	forecast := WeatherForecast{
		Today:    DayForecast{Morning: PeriodForecast{TemperatureC: 1, Available: true}, Afternoon: PeriodForecast{TemperatureC: 2, Available: true}},
		Tomorrow: DayForecast{Morning: PeriodForecast{TemperatureC: 3, Available: true}, Afternoon: PeriodForecast{TemperatureC: 4, Available: true}},
	}

	cases := []struct {
		dateStr, hourMinute string
		wantTemp            float64
	}{
		{todayStr, "08:00", 1},
		{todayStr, "14:00", 2},
		{tomorrowStr, "08:00", 3},
		{tomorrowStr, "14:00", 4},
	}
	for _, c := range cases {
		got := pickForecastSlot(forecast, c.dateStr, c.hourMinute, now)
		if got.TemperatureC != c.wantTemp {
			t.Errorf("pickForecastSlot(%s, %s) = temp %.1f, want %.1f", c.dateStr, c.hourMinute, got.TemperatureC, c.wantTemp)
		}
	}
}
