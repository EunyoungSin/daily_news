package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComputeUmbrellaAdvice(t *testing.T) {
	cases := []struct {
		name       string
		morning    int
		afternoon  int
		wantNeeded bool
		wantPeriod string
	}{
		{"both low", 0, 22, false, ""},
		{"right at threshold counts as high", 40, 0, true, "오전 8시"},
		{"just under threshold is not high", 39, 39, false, ""},
		{"afternoon only", 10, 85, true, "오후 2시"},
		{"morning only", 60, 5, true, "오전 8시"},
		{"both high", 70, 80, true, "오전 8시와 오후 2시 모두"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			today := DayForecast{
				Morning:   PeriodForecast{PrecipProbability: tc.morning, Available: true},
				Afternoon: PeriodForecast{PrecipProbability: tc.afternoon, Available: true},
			}
			got := computeUmbrellaAdvice(today)
			if got.Needed != tc.wantNeeded || got.Period != tc.wantPeriod {
				t.Errorf("computeUmbrellaAdvice(%d, %d) = %+v, want {Needed:%v Period:%q}",
					tc.morning, tc.afternoon, got, tc.wantNeeded, tc.wantPeriod)
			}
		})
	}
}

// toBriefingWeatherInput은 Forecast.Tomorrow를 절대 노출해서는 안 된다 —
// 이는 단순히 프롬프트 문구 수준이 아니라, 내일 데이터가 브리핑 내용이나
// 캐시 키에 절대 섞여 들어갈 수 없다는 구조적 보장이다.
func TestToBriefingWeatherInputExcludesTomorrow(t *testing.T) {
	weather := &WeatherData{
		Current: CurrentWeather{CityLabel: "대구"},
		Forecast: WeatherForecast{
			Today:    DayForecast{Morning: PeriodForecast{TemperatureC: 20, Available: true}},
			Tomorrow: DayForecast{Morning: PeriodForecast{TemperatureC: 999, Available: true}},
		},
	}

	input := toBriefingWeatherInput(weather)
	if input.Today.Morning == nil || input.Today.Morning.TemperatureC != 20 {
		t.Errorf("expected today's data to pass through unchanged, got %+v", input.Today.Morning)
	}

	// briefingWeatherInput에는 애초에 Tomorrow 필드 자체가 없으므로 이는
	// 사실상 컴파일 타임에 보장되는 것이다 — 이 테스트는 그 의도를
	// 문서화하기 위한 것이다.
}

// TestToBriefingDayForecastOmitsUnavailablePeriods는 실제 사용자 제보로
// 발견된 버그를 고친 결과를 검증한다: 어떤 시간대가 Available:false이면
// Groq에 보내는 JSON에 그 필드가 아예 없어야 한다(리터럴 "temperatureC":0
// 로 남으면 안 됨) — 그렇지 않으면 모델이 이 zero-value를 그대로 읽고
// "오전 8시엔 0도"라고 써버리는 현상이 실제로 관찰됐다.
func TestToBriefingDayForecastOmitsUnavailablePeriods(t *testing.T) {
	day := DayForecast{
		Morning:   PeriodForecast{}, // Available: false (제로 값)
		Afternoon: PeriodForecast{TemperatureC: 32, Available: true},
	}

	out := toBriefingDayForecast(day)
	if out.Morning != nil {
		t.Errorf("expected Morning to be nil (omitted), got %+v", out.Morning)
	}
	if out.Afternoon == nil || out.Afternoon.TemperatureC != 32 {
		t.Errorf("expected Afternoon to carry through, got %+v", out.Afternoon)
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if strings.Contains(string(encoded), "morning") {
		t.Errorf("expected no \"morning\" key in JSON when unavailable, got %s", encoded)
	}
}

// TestToBriefingWeatherInputExcludesDetailURL은 나중에 briefingCurrentWeather가
// CurrentWeather 전체를 통째로 다시 포함하는 방식으로 리팩터링되더라도,
// DetailURL(나중에 추가된 프론트엔드의 "자세히 보기" 링크)이 Groq
// 프롬프트/캐시 키에 모르는 새 다시 섞여 들어가지 않도록 지키는
// 테스트다.
func TestToBriefingWeatherInputExcludesDetailURL(t *testing.T) {
	weather := &WeatherData{
		Current: CurrentWeather{CityLabel: "대구", DetailURL: "https://example.com/should-not-leak"},
	}

	encoded, err := json.Marshal(toBriefingWeatherInput(weather))
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if strings.Contains(string(encoded), "should-not-leak") {
		t.Errorf("DetailURL leaked into briefing input JSON: %s", encoded)
	}
}
