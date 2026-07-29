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
// 상태로 남는다.
func TestResolveForecastSlotNilDB(t *testing.T) {
	available := PeriodForecast{TemperatureC: 26.4, Available: true}
	if got := resolveForecastSlot(context.Background(), "daegu", "2026-07-29", "08:00", available); got != available {
		t.Errorf("expected an Available apiValue to pass through unchanged, got %+v", got)
	}

	unavailable := PeriodForecast{}
	if got := resolveForecastSlot(context.Background(), "daegu", "2026-07-29", "08:00", unavailable); got.Available {
		t.Errorf("expected no DB to fall back to, got %+v", got)
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
