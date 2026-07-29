package main

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// weatherSlotCacheRow는 실제로 DB에 저장되는 값이다 — Description은
// 컬럼으로 존재하지 않으므로(createWeatherSlotCacheTable의 주석 참고),
// PeriodForecast로 되돌릴 때 weathercodeDescription으로 다시 계산해서
// 채운다.
type weatherSlotCacheRow struct {
	Temperature              float64
	WeatherCode              int
	PrecipitationProbability int
}

func (r weatherSlotCacheRow) toPeriodForecast() PeriodForecast {
	return PeriodForecast{
		TemperatureC:      r.Temperature,
		WeatherCode:       r.WeatherCode,
		Description:       weathercodeDescription(r.WeatherCode),
		PrecipProbability: r.PrecipitationProbability,
		Available:         true,
	}
}

func lookupWeatherSlot(ctx context.Context, conn *sql.DB, city, slotDate, slotTime string) (weatherSlotCacheRow, bool) {
	if conn == nil {
		return weatherSlotCacheRow{}, false
	}
	var row weatherSlotCacheRow
	err := conn.QueryRowContext(ctx,
		`SELECT temperature, weather_code, precipitation_probability FROM weather_slot_cache WHERE city = ? AND slot_date = ? AND slot_time = ?`,
		city, slotDate, slotTime,
	).Scan(&row.Temperature, &row.WeatherCode, &row.PrecipitationProbability)
	if err != nil {
		return weatherSlotCacheRow{}, false
	}
	return row, true
}

func upsertWeatherSlot(ctx context.Context, conn *sql.DB, city, slotDate, slotTime string, p PeriodForecast) {
	if conn == nil {
		return
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO weather_slot_cache (city, slot_date, slot_time, temperature, weather_code, precipitation_probability)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE temperature = VALUES(temperature), weather_code = VALUES(weather_code), precipitation_probability = VALUES(precipitation_probability)`,
		city, slotDate, slotTime, p.TemperatureC, p.WeatherCode, p.PrecipProbability,
	)
	if err != nil {
		log.Printf("날씨(%s): %s %s 슬롯 DB 저장 실패: %v", city, slotDate, slotTime, err)
		return
	}
	log.Printf("날씨(%s): %s %s 슬롯 값(기온=%.1f) DB에 저장됨", city, slotDate, slotTime, p.TemperatureC)
}

// resolveForecastSlot은 (도시, 날짜, 시간) 예보 슬롯 하나에 대한 영속화
// 계층이다. apiValue는 방금 가져온 KMA/Open-Meteo 응답 결과인데,
// Available:false일 수도 있다 — 그 슬롯이 아직 응답에 아예 없어서일 수도
// 있고(예: 그날 예보가 처음부터 아직 발표 전인 경우), KMA의 getVilageFcst가
// 이미 그 시점을 지나쳐서일 수도 있다(base_time 이후로만 예보하므로, 당일
// 슬롯 중 이미 지나간 시각은 응답에서 아예 빠진다).
//
//   - apiValue.Available인 경우: 가장 최신 값이므로 무조건 우선 사용하고
//     DB에도 (다시) 저장해서, API가 계속 값을 내려주는 한 DB 값을 최신
//     상태로 유지한다.
//   - 그렇지 않으면 이 슬롯에 대해 이전에 저장된 값이 있으면 그 값을
//     쓴다 — 이 덕분에 해당 시각의 슬롯이 KMA 응답에서 빠져나간 뒤에도
//     오늘 08:00이 "데이터 없음" 대신 실제 값을 계속 보여줄 수 있다.
//   - 그것도 없으면 이 슬롯에는 정말로 아직 데이터가 없는 것이다
//     (Available은 false로 남는다) — 실제로는 그날 예보가 처음 발표되기
//     전, 하루 중 아주 이른 시간에만 발생한다.
func resolveForecastSlot(ctx context.Context, city, dateStr, hourMinute string, apiValue PeriodForecast) PeriodForecast {
	if apiValue.Available {
		upsertWeatherSlot(ctx, db, city, dateStr, hourMinute, apiValue)
		return apiValue
	}

	if cached, ok := lookupWeatherSlot(ctx, db, city, dateStr, hourMinute); ok {
		log.Printf("날씨(%s): %s %s 슬롯이 API 응답에 없어 DB 캐시 값(기온=%.1f) 사용", city, dateStr, hourMinute, cached.Temperature)
		return cached.toPeriodForecast()
	}

	return PeriodForecast{}
}

// applyWeatherSlotCache는 고정된 네 개 슬롯(오늘/내일 x 오전/오후)
// 전체에 대해 resolveForecastSlot을 in-place로 실행한다. sanityCheckForecast
// (weather.go 참고) 이후에 호출되므로, 방금 그 검사에서 값이 비현실적이라
// 판단되어 버려진 슬롯도 동일하게 기존 정상 값으로 대체될 기회를 얻고,
// 동시에 이상한 값이 그 자체로 DB에 저장되는 일도 없다(apiValue로 여기
// 도달하는 건 Available:true인 값뿐이다).
func applyWeatherSlotCache(ctx context.Context, forecast *WeatherForecast, city string, referenceTime time.Time) {
	now := referenceTime.In(kst)
	todayStr := now.Format("2006-01-02")
	tomorrowStr := now.AddDate(0, 0, 1).Format("2006-01-02")

	forecast.Today.Morning = resolveForecastSlot(ctx, city, todayStr, forecastMorningHour, forecast.Today.Morning)
	forecast.Today.Afternoon = resolveForecastSlot(ctx, city, todayStr, forecastAfternoonHour, forecast.Today.Afternoon)
	forecast.Tomorrow.Morning = resolveForecastSlot(ctx, city, tomorrowStr, forecastMorningHour, forecast.Tomorrow.Morning)
	forecast.Tomorrow.Afternoon = resolveForecastSlot(ctx, city, tomorrowStr, forecastAfternoonHour, forecast.Tomorrow.Afternoon)
}
