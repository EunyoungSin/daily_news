package main

import (
	"testing"
	"time"
)

// TestLatLonToGridKnownCities는 LCC 변환 결과를 KMA가 공식 발표한 격자
// 값과 대조한다. 서울/부산/인천은 cityCoords의 위경도로부터 정확히
// 재현되지만, 대구는 여기서 일부러 제외했다 — cityCoords에 등록된
// 좌표가 KMA 자체 기준점보다 격자 한 칸만큼 벗어나 있기 때문이다
// (latLonToGrid의 주석 참고). 그래서 domesticGrid는 실제 서비스에
// 쓰이는 네 도시에 대해 이 함수에 의존하지 않고 공식 값을 하드코딩해
// 둔다.
func TestLatLonToGridKnownCities(t *testing.T) {
	cases := []struct {
		city   string
		lat    float64
		lon    float64
		wantNx int
		wantNy int
	}{
		{"seoul", 37.5665, 126.9780, 60, 127},
		{"busan", 35.1796, 129.0756, 98, 76},
		{"incheon", 37.4563, 126.7052, 55, 124},
	}

	for _, c := range cases {
		nx, ny := latLonToGrid(c.lat, c.lon)
		if nx != c.wantNx || ny != c.wantNy {
			t.Errorf("%s: latLonToGrid(%v, %v) = (%d, %d), want (%d, %d)",
				c.city, c.lat, c.lon, nx, ny, c.wantNx, c.wantNy)
		}
	}
}

func TestDomesticGridMatchesCityCoords(t *testing.T) {
	for city := range domesticGrid {
		if _, ok := cityCoords[city]; !ok {
			t.Errorf("domesticGrid has %q but cityCoords doesn't define it", city)
		}
	}
	for _, overseas := range []string{"tokyo", "newyork", "london"} {
		if isDomesticCity(overseas) {
			t.Errorf("%s should not be treated as domestic (no KMA coverage)", overseas)
		}
	}
}

// TestUltraSrtNcstBaseDateTime은 기능 명세가 명시적으로 요구하는 바로 그
// 시나리오를 검증한다: 14:35에 조회하면(그 시각의 :40 확정 이전이므로)
// 13:00 데이터를 요청해야 하고, 14:41에 조회하면 14:00 데이터를
// 요청해야 한다.
func TestUltraSrtNcstBaseDateTime(t *testing.T) {
	cases := []struct {
		name         string
		hour, minute int
		wantTime     string
	}{
		{"before :40 falls back to previous hour", 14, 35, "1300"},
		{"at/after :40 uses the current hour", 14, 41, "1400"},
		{"exactly :40 uses the current hour", 14, 40, "1400"},
		{"midnight rollover", 0, 10, "2300"},
	}

	for _, c := range cases {
		now := time.Date(2026, 7, 28, c.hour, c.minute, 0, 0, kst)
		date, tm := ultraSrtNcstBaseDateTime(now)
		wantDate := "20260728"
		if c.name == "midnight rollover" {
			wantDate = "20260727"
		}
		if tm != c.wantTime || date != wantDate {
			t.Errorf("%s: ultraSrtNcstBaseDateTime(%02d:%02d) = (%s, %s), want (%s, %s)",
				c.name, c.hour, c.minute, date, tm, wantDate, c.wantTime)
		}
	}
}

func TestVilageFcstBaseDateTime(t *testing.T) {
	cases := []struct {
		name         string
		hour, minute int
		wantDate     string
		wantTime     string
	}{
		{"just after 14:10 issuance", 14, 15, "20260728", "1400"},
		{"just before 14:10 issuance falls back to 11:00", 14, 5, "20260728", "1100"},
		{"before today's first issuance falls back to yesterday 23:00", 1, 30, "20260727", "2300"},
	}

	for _, c := range cases {
		now := time.Date(2026, 7, 28, c.hour, c.minute, 0, 0, kst)
		date, tm := vilageFcstBaseDateTime(now)
		if date != c.wantDate || tm != c.wantTime {
			t.Errorf("%s: vilageFcstBaseDateTime(%02d:%02d) = (%s, %s), want (%s, %s)",
				c.name, c.hour, c.minute, date, tm, c.wantDate, c.wantTime)
		}
	}
}

// TestVilageFcstBaseDateTimeBeforeSlot은 vilageFcstBaseDateTime과의
// 핵심적인 차이를 검증한다: "지금 기준 최신 발표"가 아니라 "그 슬롯이 아직
// 미래였을 때의 발표 중 가장 최근 것"을 골라야 하고, 이 대답은 now가
// 슬롯보다 한참 뒤여도(즉 슬롯이 이미 지난 뒤 나중에 다시 물어봐도)
// 바뀌지 않아야 한다.
func TestVilageFcstBaseDateTimeBeforeSlot(t *testing.T) {
	cases := []struct {
		name         string
		hourMinute   string
		nowHour      int
		nowMinute    int
		wantBaseTime string
		wantOK       bool
	}{
		{"08:00 slot, asked right after it passes -> latest earlier issue 05:00", "08:00", 8, 30, "0500", true},
		{"08:00 slot, asked much later same day -> still 05:00 (doesn't drift with now)", "08:00", 20, 0, "0500", true},
		{"14:00 slot, asked right after it passes -> latest earlier issue 11:00", "14:00", 14, 30, "1100", true},
	}

	for _, c := range cases {
		now := time.Date(2026, 7, 29, c.nowHour, c.nowMinute, 0, 0, kst)
		baseDate, baseTime, ok := vilageFcstBaseDateTimeBeforeSlot("2026-07-29", c.hourMinute, now)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if baseTime != c.wantBaseTime || baseDate != "20260729" {
			t.Errorf("%s: vilageFcstBaseDateTimeBeforeSlot = (%s, %s), want (20260729, %s)",
				c.name, baseDate, baseTime, c.wantBaseTime)
		}
	}
}

// TestVilageFcstBaseDateTimeBeforeSlotRejectsUnparseableInput은 slotIsPast와
// 동일한 안전한 실패 방식을 검증한다 — 있어서는 안 되는 입력이라도 panic
// 대신 ok=false를 반환해야 한다.
func TestVilageFcstBaseDateTimeBeforeSlotRejectsUnparseableInput(t *testing.T) {
	if _, _, ok := vilageFcstBaseDateTimeBeforeSlot("not-a-date", "08:00", time.Now()); ok {
		t.Error("expected an unparseable slot to fail safely with ok=false")
	}
}

func TestKmaWeatherCode(t *testing.T) {
	cases := []struct {
		pty, sky string
		want     int
	}{
		{"0", "1", 0},  // 맑음
		{"0", "3", 2},  // 구름많음 -> 구름 조금
		{"0", "4", 3},  // 흐림
		{"1", "4", 61}, // 비 (PTY가 SKY보다 우선)
		{"3", "1", 71}, // 눈
		{"4", "1", 80}, // 소나기
		{"0", "", 0},   // sky 정보 없음(현재 날씨 케이스)이면 맑음으로 기본 처리
	}

	for _, c := range cases {
		if got := kmaWeatherCode(c.pty, c.sky); got != c.want {
			t.Errorf("kmaWeatherCode(%q, %q) = %d, want %d", c.pty, c.sky, got, c.want)
		}
	}
}

// TestBuildKMAForecastMissingSample은 Open-Meteo 경로용
// TestBuildForecastMissingSample과 대응된다: getVilageFcst가 아직
// 예보하지 않은 슬롯(예: 오늘 중 이미 지나간 08:00)은 panic이나 잘못된
// 값 할당 없이 zero-value PeriodForecast로 처리되어야 한다.
func TestBuildKMAForecastMissingSample(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, kst)

	items := []kmaItem{
		{Category: "TMP", FcstDate: "20260728", FcstTime: "1400", FcstValue: "26.0"},
		{Category: "POP", FcstDate: "20260728", FcstTime: "1400", FcstValue: "30"},
		{Category: "SKY", FcstDate: "20260728", FcstTime: "1400", FcstValue: "3"},
		{Category: "PTY", FcstDate: "20260728", FcstTime: "1400", FcstValue: "0"},
	}

	forecast := buildKMAForecast(items, now)

	if forecast.Today.Afternoon.TemperatureC != 26.0 {
		t.Errorf("today afternoon temp = %v, want 26.0", forecast.Today.Afternoon.TemperatureC)
	}
	if forecast.Today.Afternoon.WeatherCode != 2 {
		t.Errorf("today afternoon code = %d, want 2 (SKY=3 -> 구름 조금)", forecast.Today.Afternoon.WeatherCode)
	}
	if (forecast.Today.Morning != PeriodForecast{}) {
		t.Errorf("today morning = %+v, want zero value (no 08:00 slot in the fixture)", forecast.Today.Morning)
	}
	if (forecast.Tomorrow.Morning != PeriodForecast{}) {
		t.Errorf("tomorrow morning = %+v, want zero value (no data in the fixture)", forecast.Tomorrow.Morning)
	}
}
