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

// slotIsPast는 (dateStr, hourMinute)로 표현된 예보 슬롯 시각이 now
// 기준으로 이미 지났는지 판단한다 — resolveForecastSlot이 "아직 발표
// 전"(unavailableNotYetAvailable)과 "이미 지난 시각인데 데이터가
// 없음"(unavailablePastMissing)을 구분하는 데 쓴다.
func slotIsPast(dateStr, hourMinute string, now time.Time) bool {
	slotTime, err := time.ParseInLocation("2006-01-02 15:04", dateStr+" "+hourMinute, kst)
	if err != nil {
		// 있어서는 안 되는 입력 오류이니, 안전한 쪽("곧 발표될 예정입니다")으로
		// 처리한다.
		return false
	}
	return now.In(kst).After(slotTime)
}

// backfillMissingSlot은 이미 지난 시각인데 API 응답에도 DB 캐시에도 값이
// 없는 슬롯 하나를 즉시 한 번 더 살아있는 API로 조회해본다 — 원래는
// 발표됐어야 하는데(base_time 자체는 지났으니) 어쩌다 한 번도 저장되지
// 못한 예외 상황(예: 서버가 그 시각이 지나기 전엔 한 번도 요청을 받지
// 못한 경우)에 대한 자동 복구 시도다. 국내 도시는 기상청을, 해외 도시는
// Open-Meteo를 그대로 다시 부른다 — fetchWeather 전체를 다시 타지 않고
// 이 두 저수준 함수를 직접 호출하는 이유는, fetchWeather는
// finalizeWeatherForecast를 거쳐 다시 이 함수(resolveForecastSlot)를
// 호출하므로 그대로 재사용하면 무한 재귀에 빠지기 때문이다.
//
// 이미 지나간 KMA 슬롯은 구조적으로 getVilageFcst 응답에 다시는 나타나지
// 않으므로(그 API는 항상 base_time 이후만 예보한다), 이 재시도가 실제로
// 값을 복구하는 경우는 주로 원래 시도가 타임아웃 등으로 일시적으로
// 실패했던 경우다 — 그래도 실패하면 그대로 unavailablePastMissing으로
// 남긴다.
func backfillMissingSlot(ctx context.Context, city, dateStr, hourMinute string) (PeriodForecast, bool) {
	cityKey := normalizeCity(city)
	coord := cityCoords[cityKey]

	var forecast WeatherForecast
	if isDomesticCity(cityKey) {
		kmaCtx, cancel := context.WithTimeout(ctx, backfillFetchTimeout)
		data, err := fetchWeatherKMA(kmaCtx, cityKey, coord)
		cancel()
		if err != nil {
			log.Printf("날씨(%s): %s %s 슬롯 백필 재시도 실패(KMA): %v", city, dateStr, hourMinute, err)
			return PeriodForecast{}, false
		}
		forecast = data.Forecast
	} else {
		omCtx, cancel := context.WithTimeout(ctx, backfillFetchTimeout)
		data, err := fetchWeatherOpenMeteo(omCtx, cityKey, coord)
		cancel()
		if err != nil {
			log.Printf("날씨(%s): %s %s 슬롯 백필 재시도 실패(Open-Meteo): %v", city, dateStr, hourMinute, err)
			return PeriodForecast{}, false
		}
		forecast = data.Forecast
	}

	slot := pickForecastSlot(forecast, dateStr, hourMinute, time.Now())

	if !slot.Available {
		log.Printf("날씨(%s): %s %s 슬롯 백필 재조회에도 데이터 없음", city, dateStr, hourMinute)
		return PeriodForecast{}, false
	}

	log.Printf("날씨(%s): %s %s 슬롯 백필 성공(기온=%.1f)", city, dateStr, hourMinute, slot.TemperatureC)
	return slot, true
}

// pickForecastSlot은 방금 새로 가져온 WeatherForecast(오늘/내일 x 오전/오후
// 네 슬롯) 중에서, (dateStr, hourMinute)가 가리키는 슬롯 하나를 골라낸다.
// dateStr은 now 기준 오늘 날짜 문자열과 같으면 Today를, 아니면(내일이면)
// Tomorrow를 가리키는 것으로 본다 — backfillMissingSlot이 방금 가져온
// forecast는 항상 지금(now) 기준으로 새로 계산된 것이므로 이 비교만으로
// 충분하다.
func pickForecastSlot(forecast WeatherForecast, dateStr, hourMinute string, now time.Time) PeriodForecast {
	day := forecast.Today
	if dateStr != now.In(kst).Format("2006-01-02") {
		day = forecast.Tomorrow
	}
	if hourMinute == forecastMorningHour {
		return day.Morning
	}
	return day.Afternoon
}

// backfillFetchTimeout은 backfillMissingSlot 전용 타임아웃이다.
// context.Background()에서 독립적으로 파생시킨다 — 호출자의 요청 스코프
// ctx를 그대로 쓰면, 이미 메인 조회(KMA 최대 kmaSubTimeout=9초 + 실패 시
// Open-Meteo 폴백)로 예산을 거의 다 써버린 뒤에 이 재시도까지 더해져
// weatherSectionTimeout(12초)을 넘겨 섹션 전체가 실패할 수 있다 —
// raw_data_cache.go의 rawCacheUpsertTimeout과 같은 이유다. past_missing은
// 정의상 드문 예외 상황이므로, 이 정도의 짧은 추가 지연은 감수할 만하다.
const backfillFetchTimeout = 4 * time.Second

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
//   - 그것도 없고 슬롯 시각이 아직 지나지 않았으면, 정말로 아직 발표 전인
//     정상 상황이다(unavailableNotYetAvailable).
//   - 그것도 없는데 슬롯 시각이 이미 지났다면, 원래는 발표됐어야 하는데
//     하루 중 그 시각이 API 응답에 있는 동안 단 한 번도 저장되지 못한
//     예외 상황이다. DB가 있을 때만(단위 테스트처럼 DB가 없으면 복구해도
//     저장할 곳이 없다) backfillMissingSlot으로 즉시 한 번 더 조회를
//     시도하고, 그래도 실패하면 unavailablePastMissing으로 남긴다.
func resolveForecastSlot(ctx context.Context, city, dateStr, hourMinute string, apiValue PeriodForecast, now time.Time) PeriodForecast {
	if apiValue.Available {
		upsertWeatherSlot(ctx, db, city, dateStr, hourMinute, apiValue)
		return apiValue
	}

	if cached, ok := lookupWeatherSlot(ctx, db, city, dateStr, hourMinute); ok {
		log.Printf("날씨(%s): %s %s 슬롯이 API 응답에 없어 DB 캐시 값(기온=%.1f) 사용", city, dateStr, hourMinute, cached.Temperature)
		return cached.toPeriodForecast()
	}

	if !slotIsPast(dateStr, hourMinute, now) {
		return PeriodForecast{UnavailableReason: unavailableNotYetAvailable}
	}

	if db != nil {
		if recovered, ok := backfillMissingSlot(ctx, city, dateStr, hourMinute); ok {
			upsertWeatherSlot(ctx, db, city, dateStr, hourMinute, recovered)
			return recovered
		}
	}

	log.Printf("날씨(%s): %s %s 슬롯이 이미 지난 시각인데도 API 응답과 DB 캐시 어디에도 값이 없음 — 백필도 실패", city, dateStr, hourMinute)
	return PeriodForecast{UnavailableReason: unavailablePastMissing}
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

	forecast.Today.Morning = resolveForecastSlot(ctx, city, todayStr, forecastMorningHour, forecast.Today.Morning, now)
	forecast.Today.Afternoon = resolveForecastSlot(ctx, city, todayStr, forecastAfternoonHour, forecast.Today.Afternoon, now)
	forecast.Tomorrow.Morning = resolveForecastSlot(ctx, city, tomorrowStr, forecastMorningHour, forecast.Tomorrow.Morning, now)
	forecast.Tomorrow.Afternoon = resolveForecastSlot(ctx, city, tomorrowStr, forecastAfternoonHour, forecast.Tomorrow.Afternoon, now)
}
