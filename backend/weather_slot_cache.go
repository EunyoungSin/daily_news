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
	Source                   string
}

func (r weatherSlotCacheRow) toPeriodForecast() PeriodForecast {
	return PeriodForecast{
		TemperatureC:      r.Temperature,
		WeatherCode:       r.WeatherCode,
		Description:       weathercodeDescription(r.WeatherCode),
		PrecipProbability: r.PrecipitationProbability,
		Available:         true,
		Source:            r.Source,
	}
}

func lookupWeatherSlot(ctx context.Context, conn *sql.DB, city, slotDate, slotTime string) (weatherSlotCacheRow, bool) {
	if conn == nil {
		return weatherSlotCacheRow{}, false
	}
	var row weatherSlotCacheRow
	err := conn.QueryRowContext(ctx,
		`SELECT temperature, weather_code, precipitation_probability, source FROM weather_slot_cache WHERE city = ? AND slot_date = ? AND slot_time = ?`,
		city, slotDate, slotTime,
	).Scan(&row.Temperature, &row.WeatherCode, &row.PrecipitationProbability, &row.Source)
	if err != nil {
		return weatherSlotCacheRow{}, false
	}
	return row, true
}

// upsertWeatherSlot은 p.Source를 그대로 저장하되, 호출하는 쪽이 실수로
// 비워뒀다면(예: 새 테스트나 향후 새 호출부) weatherSlotSourceObserved로
// 기본 처리한다 — DB 컬럼이 NOT NULL이라 빈 문자열을 그대로 넣을 수 없고,
// 이 함수에 도달하는 값은 apiValue.Available 경로든 백필 경로든 항상
// "정상적으로 확보된" 쪽이 기본값이어야 맞기 때문이다.
func upsertWeatherSlot(ctx context.Context, conn *sql.DB, city, slotDate, slotTime string, p PeriodForecast) {
	if conn == nil {
		return
	}
	source := p.Source
	if source == "" {
		source = weatherSlotSourceObserved
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO weather_slot_cache (city, slot_date, slot_time, temperature, weather_code, precipitation_probability, source)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(city, slot_date, slot_time) DO UPDATE SET
			temperature = excluded.temperature,
			weather_code = excluded.weather_code,
			precipitation_probability = excluded.precipitation_probability,
			source = excluded.source,
			updated_at = CURRENT_TIMESTAMP`,
		city, slotDate, slotTime, p.TemperatureC, p.WeatherCode, p.PrecipProbability, source,
	)
	if err != nil {
		log.Printf("날씨(%s): %s %s 슬롯 DB 저장 실패: %v", city, slotDate, slotTime, err)
		return
	}
	log.Printf("날씨(%s): %s %s 슬롯 값(기온=%.1f, source=%s) DB에 저장됨", city, slotDate, slotTime, p.TemperatureC, source)
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
// 없는 슬롯 하나를 "지금 기준 최신" 조회로 즉시 한 번 더 시도해본다 —
// 원래는 발표됐어야 하는데(base_time 자체는 지났으니) 어쩌다 한 번도
// 저장되지 못한 예외 상황(예: 서버가 그 시각이 지나기 전엔 한 번도 요청을
// 받지 못한 경우)에 대한 자동 복구 시도다. 국내 도시는 기상청을, 해외
// 도시는 Open-Meteo를 그대로 다시 부른다 — fetchWeather 전체를 다시 타지
// 않고 이 두 저수준 함수를 직접 호출하는 이유는, fetchWeather는
// finalizeWeatherForecast를 거쳐 다시 이 함수(resolveForecastSlot)를
// 호출하므로 그대로 재사용하면 무한 재귀에 빠지기 때문이다.
//
// 해외(Open-Meteo) 도시에서는 이게 여전히 유일하고도 충분한 복구 경로다 —
// Open-Meteo는 당일 00:00부터의 hourly 데이터를 통째로 돌려주므로, 재시도가
// 실패하는 경우는 진짜 일시적 장애뿐이다. 반면 국내(KMA) 도시에서는
// resolveForecastSlot이 이 함수를 부르기 전에 이미
// backfillPastSlotFromEarlierVilageFcstRun을 먼저 시도한다 — 그쪽이
// "슬롯보다 이전 발표"를 명시적으로 골라 구조적으로 이 문제를 해결하는
// 반면, 이 함수는 (fetchWeatherKMA를 통해) 결국 vilageFcstBaseDateTime(now)로
// "지금 기준 최신 발표"를 다시 가져올 뿐이라 이미 그 발표에서도 슬롯이
// 지나가버린 상태(애초에 이 함수가 호출된 이유)에서는 구조적으로 절대
// 복구되지 않기 때문이다(그 API는 항상 base_time 이후만 예보한다). 그래서
// 국내 도시에 한해서는 이 함수가 실질적으로 순수한 재시도(원래 시도가
// 타임아웃 등으로 일시적으로 실패했던 경우) 역할만 하며, 그래도 실패하면
// 그대로 unavailablePastMissing으로 남긴다.
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

// backfillPastSlotFromEarlierVilageFcstRun은 이미 지난 시각인 국내(KMA)
// 슬롯을, "지금 기준 최신 발표"가 아니라 그 슬롯 시각보다 먼저 발표된
// getVilageFcst 회차로 소급 조회해서 복구한다. backfillMissingSlot(바로
// 아래)과 달리 시도 자체가 구조적으로 의미 있는 경로다 — backfillMissingSlot은
// fetchWeatherKMA를 통해 결국 vilageFcstBaseDateTime(now)를 다시 부르므로
// "지금 기준 최신 발표"를 또 가져올 뿐이라, 이미 그 발표에서도 슬롯이
// 지나가버린 상태(바로 이 함수가 호출되는 이유)라면 똑같이 실패할 수밖에
// 없다. 반면 이 함수가 쓰는 vilageFcstBaseDateTimeBeforeSlot은 슬롯보다
// 이전 회차를 고르므로, 그 회차의 응답에서는 슬롯이 언제나 "미래" 구간에
// 있어 몇 시간이 지난 뒤에 다시 조회해도 그대로 나온다.
//
// 해외 도시는 애초에 이 문제가 없다 — Open-Meteo의 hourly 응답은 요청
// 시각과 무관하게 당일 00:00부터를 통째로 돌려주므로, 오늘 이미 지난
// 시각도 처음부터 buildForecast에서 정상적으로 채워진다. 그래서 이 함수는
// domesticGrid에 없는 도시에 대해서는 바로 실패를 반환하고,
// backfillMissingSlot(Open-Meteo 경로 포함)만 계속 그 역할을 한다.
func backfillPastSlotFromEarlierVilageFcstRun(ctx context.Context, city, dateStr, hourMinute string, now time.Time) (PeriodForecast, bool) {
	cityKey := normalizeCity(city)
	grid, ok := domesticGrid[cityKey]
	if !ok {
		return PeriodForecast{}, false
	}

	baseDate, baseTime, ok := vilageFcstBaseDateTimeBeforeSlot(dateStr, hourMinute, now)
	if !ok {
		log.Printf("날씨(%s): %s %s 슬롯보다 이전에 발표된 단기예보 회차를 찾지 못함 — 소급 조회 생략", city, dateStr, hourMinute)
		return PeriodForecast{}, false
	}

	fcstCtx, cancel := context.WithTimeout(ctx, backfillFetchTimeout)
	defer cancel()
	forecast, err := fetchKMAForecastAt(fcstCtx, grid[0], grid[1], baseDate, baseTime)
	if err != nil {
		log.Printf("날씨(%s): %s %s 슬롯을 %s %s 발표 단기예보로 소급 조회 실패: %v", city, dateStr, hourMinute, baseDate, baseTime, err)
		return PeriodForecast{}, false
	}

	slot := pickForecastSlot(*forecast, dateStr, hourMinute, now)
	if !slot.Available {
		log.Printf("날씨(%s): %s %s 슬롯이 %s %s 발표 단기예보 응답에도 없음", city, dateStr, hourMinute, baseDate, baseTime)
		return PeriodForecast{}, false
	}

	slot.Source = weatherSlotSourceForecast
	log.Printf("날씨(%s): %s %s 슬롯을 %s %s 발표 단기예보로 소급 복구함(기온=%.1f)", city, dateStr, hourMinute, baseDate, baseTime, slot.TemperatureC)
	return slot, true
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
//     (Source를 weatherSlotSourceObserved로 표시해서) DB에도 (다시)
//     저장해서, API가 계속 값을 내려주는 한 DB 값을 최신 상태로 유지한다.
//   - 그렇지 않으면 이 슬롯에 대해 이전에 저장된 값이 있으면 그 값을
//     쓴다 — 이 덕분에 해당 시각의 슬롯이 KMA 응답에서 빠져나간 뒤에도
//     오늘 08:00이 "데이터 없음" 대신 실제 값을 계속 보여줄 수 있다.
//     Source도 이때 저장된 그대로(observed 또는 forecast) 함께 돌아온다.
//   - 그것도 없고 슬롯 시각이 아직 지나지 않았으면, 정말로 아직 발표 전인
//     정상 상황이다(unavailableNotYetAvailable).
//   - 그것도 없는데 슬롯 시각이 이미 지났다면, DB가 있을 때만(단위
//     테스트처럼 DB가 없으면 복구해도 저장할 곳이 없다) 두 단계로
//     복구를 시도한다: 먼저
//     backfillPastSlotFromEarlierVilageFcstRun으로 그 슬롯 시각 이전에
//     발표된 단기예보 회차를 소급 조회하고(국내 도시에서 실제로 값을
//     복구하는 경로는 사실상 이쪽이다 — Source는
//     weatherSlotSourceForecast), 그게 안 되면 기존처럼
//     backfillMissingSlot으로 "지금 기준 최신" 조회를 한 번 더 시도한다
//     (이번 값도 정상적으로 확보된 것이니 Source는
//     weatherSlotSourceObserved). 둘 다 실패하면 unavailablePastMissing으로
//     남긴다.
func resolveForecastSlot(ctx context.Context, city, dateStr, hourMinute string, apiValue PeriodForecast, now time.Time) PeriodForecast {
	if apiValue.Available {
		apiValue.Source = weatherSlotSourceObserved
		upsertWeatherSlot(ctx, db, city, dateStr, hourMinute, apiValue)
		return apiValue
	}

	if cached, ok := lookupWeatherSlot(ctx, db, city, dateStr, hourMinute); ok {
		log.Printf("날씨(%s): %s %s 슬롯이 API 응답에 없어 DB 캐시 값(기온=%.1f, source=%s) 사용", city, dateStr, hourMinute, cached.Temperature, cached.Source)
		return cached.toPeriodForecast()
	}

	if !slotIsPast(dateStr, hourMinute, now) {
		return PeriodForecast{UnavailableReason: unavailableNotYetAvailable}
	}

	if db != nil {
		if recovered, ok := backfillPastSlotFromEarlierVilageFcstRun(ctx, city, dateStr, hourMinute, now); ok {
			upsertWeatherSlot(ctx, db, city, dateStr, hourMinute, recovered)
			return recovered
		}
		if recovered, ok := backfillMissingSlot(ctx, city, dateStr, hourMinute); ok {
			recovered.Source = weatherSlotSourceObserved
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
