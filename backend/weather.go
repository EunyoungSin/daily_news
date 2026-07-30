package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

type cityCoord struct {
	Lat, Lon float64
	Label    string
	// EnglishName은 해외 도시에만 설정된다. 해외 도시는 상세 날씨 링크가
	// 한국어 네이버 검색이 아니라 영어 구글 검색으로 연결되기 때문이다.
	// weatherDetailURL은 별도의 조회 없이 이 값의 유무만으로 국내/해외 도시를
	// 구분한다.
	EnglishName string
}

var cityCoords = map[string]cityCoord{
	"seoul":   {Lat: 37.5665, Lon: 126.9780, Label: "서울"},
	"daegu":   {Lat: 35.8714, Lon: 128.6014, Label: "대구"},
	"busan":   {Lat: 35.1796, Lon: 129.0756, Label: "부산"},
	"incheon": {Lat: 37.4563, Lon: 126.7052, Label: "인천"},
	"tokyo":   {Lat: 35.6762, Lon: 139.6503, Label: "도쿄", EnglishName: "Tokyo"},
	"newyork": {Lat: 40.7128, Lon: -74.0060, Label: "뉴욕", EnglishName: "New York"},
	"london":  {Lat: 51.5074, Lon: -0.1278, Label: "런던", EnglishName: "London"},
}

// weatherDetailURL은 WeatherCard의 "자세히 보기" 링크에 쓸 외부 날씨 검색
// URL을 만든다 — 국내 도시는 한글 도시명으로 네이버 검색을, 해외 도시는
// 영문 도시명으로 구글 검색을 가리킨다.
func weatherDetailURL(coord cityCoord) string {
	if coord.EnglishName != "" {
		return "https://www.google.com/search?q=" + url.QueryEscape(coord.EnglishName+" weather")
	}
	return "https://search.naver.com/search.naver?query=" + url.QueryEscape(coord.Label+" 날씨")
}

const defaultCity = "daegu"

func normalizeCity(city string) string {
	if _, ok := cityCoords[city]; ok {
		return city
	}
	return defaultCity
}

// weathercodeDescription은 WMO 날씨 코드를 한글 설명으로 매핑한다.
// https://open-meteo.com/en/docs (WMO Weather interpretation codes)
func weathercodeDescription(code int) string {
	switch {
	case code == 0:
		return "맑음"
	case code == 1:
		return "대체로 맑음"
	case code == 2:
		return "구름 조금"
	case code == 3:
		return "흐림"
	case code == 45 || code == 48:
		return "안개"
	case code == 51 || code == 53 || code == 55:
		return "이슬비"
	case code == 56 || code == 57:
		return "언 이슬비"
	case code == 61 || code == 63 || code == 65:
		return "비"
	case code == 66 || code == 67:
		return "언 비"
	case code == 71 || code == 73 || code == 75:
		return "눈"
	case code == 77:
		return "싸락눈"
	case code == 80 || code == 81 || code == 82:
		return "소나기"
	case code == 85 || code == 86:
		return "눈 소나기"
	case code == 95:
		return "뇌우"
	case code == 96 || code == 99:
		return "뇌우(우박 동반)"
	default:
		return "알 수 없음"
	}
}

type openMeteoResponse struct {
	CurrentWeather struct {
		Temperature float64 `json:"temperature"`
		Windspeed   float64 `json:"windspeed"`
		Weathercode int     `json:"weathercode"`
		Time        string  `json:"time"`
	} `json:"current_weather"`
	Hourly struct {
		Time                     []string  `json:"time"`
		Temperature2m            []float64 `json:"temperature_2m"`
		Weathercode              []int     `json:"weathercode"`
		PrecipitationProbability []int     `json:"precipitation_probability"`
	} `json:"hourly"`
}

// weatherRawCacheTTL은 raw_data_cache에 저장된 fetchWeather 결과를 몇 분간
// 재사용 가능하게 유지하여, 그 시간 안에 "조회"/자동 새로고침을
// 반복하거나(또는 같은 도시를 여러 브라우저 탭에서 열어놓는 경우) 매번
// 기상청/Open-Meteo를 다시 호출하지 않도록 한다. 이는
// weatherSlotCache(weather_slot_cache.go)와는 별개다. weatherSlotCache는
// 개별 예보 슬롯을 하루 종일 MySQL에 영속 저장하여 재시작 후에도 남아
// 있고 특정 시각의 누락 데이터를 채워 넣는 반면, 이 캐시는 fetchWeather의
// 반환값 전체(현재 날씨 + 오늘/내일 예보)를 그대로 JSON으로 캐싱한다 —
// 예전에는 프로세스 메모리에만 있어서 서버가 재시작되면(Render 무료
// 티어가 슬립 후 깨어날 때 등) 사라졌지만, 이제는 raw_data_cache
// 테이블(raw_data_cache.go)에 저장되어 재시작 후에도 그대로 남아있다.
const weatherRawCacheTTL = 10 * time.Minute

func weatherCacheKey(city string) string {
	return "weather:" + normalizeCity(city)
}

// getCachedOrFetchWeather는 dashboardHandler의 진입점이다 — 기상청/Open-Meteo를
// 다시 호출하는 대신 최근 fetchWeather 결과를 재사용한다.
func getCachedOrFetchWeather(ctx context.Context, city string) (*WeatherData, error) {
	return fetchWithRawCache(ctx, db, weatherCacheKey(city), weatherRawCacheTTL, func(ctx context.Context) (*WeatherData, error) {
		return fetchWeather(ctx, city)
	})
}

// fetchWeather는 국내 도시에 대해 기상청(KMA)으로 요청을 보낸다 —
// 네이버 날씨도 같은 소스를 사용하므로 사용자가 다른 곳에서 보는 기온과
// 값이 일치한다 — 기상청 호출이 어떤 이유로든 실패하면(서비스 장애, 격자
// 조회 문제, 인증키 누락 등) Open-Meteo로 대체한다. 해외 도시는 기상청
// 예보 범위 밖이므로 항상 Open-Meteo를 직접 사용한다.
func fetchWeather(ctx context.Context, city string) (*WeatherData, error) {
	cityKey := normalizeCity(city)
	coord := cityCoords[cityKey]

	if isDomesticCity(cityKey) {
		kmaCtx, cancel := context.WithTimeout(ctx, kmaSubTimeout)
		data, err := fetchWeatherKMA(kmaCtx, cityKey, coord)
		cancel()
		if err == nil {
			data.DataSource = "kma"
			finalizeWeatherForecast(ctx, data, cityKey)
			return data, nil
		}

		log.Printf("⚠️ 기상청 API 응답 지연/실패로 Open-Meteo 데이터로 대체합니다 (도시: %s, 사유: %s)",
			coord.Label, classifyKMAFailureReason(err))

		data, err = fetchWeatherOpenMeteo(ctx, cityKey, coord)
		if err != nil {
			return nil, err
		}
		data.DataSource = "open-meteo-fallback"
		finalizeWeatherForecast(ctx, data, cityKey)
		return data, nil
	}

	data, err := fetchWeatherOpenMeteo(ctx, cityKey, coord)
	if err != nil {
		return nil, err
	}
	data.DataSource = "open-meteo"
	finalizeWeatherForecast(ctx, data, cityKey)
	return data, nil
}

// finalizeWeatherForecast는 fetchWeather의 모든 반환 경로에 필요한 두 가지
// 후처리를 순서대로 실행한다: 먼저 sanityCheckForecast로(비정상적인 값이
// 어딘가에 저장되기 전에 Available:false로 지워지도록) 처리하고, 그다음
// applyWeatherSlotCache를 실행한다(방금 sanityCheckForecast가 비운 슬롯이나,
// 이미 지나간 당일 시각에 대해 기상청 getVilageFcst가 더 이상 값을 반환하지
// 않는 슬롯이, 데이터 없음으로 표시되는 대신 이전에 저장해둔 값으로 대체될
// 기회를 갖도록 한다).
func finalizeWeatherForecast(ctx context.Context, data *WeatherData, cityKey string) {
	sanityCheckForecast(data, cityKey)
	applyWeatherSlotCache(ctx, &data.Forecast, cityKey, time.Now())
}

// maxPlausibleSameDaySwingC는 같은 날 오전/오후 기온이 합리적으로 벌어질
// 수 있는 최대 폭을 제한한다 — 같은 날 08:00와 14:00의 차이가 이만큼
// 크다면 실제 날씨보다는 잘못된 값(파싱 오류, 슬롯 오류 등)일 가능성이
// 훨씬 높으므로, 어느 한쪽을 믿는 대신 그날 두 값 모두 폐기한다.
const maxPlausibleSameDaySwingC = 15.0

// suspiciousZeroTemp는 한국의 겨울철이 아닌 시기에 정확히 0.0°C인 값을
// 실제 관측값이 아니라 제로값 센티널이 걸러지지 않고 새어 나온 것으로
// 의심한다 — 예를 들어 7월에 실제로 0°C가 관측될 확률보다 버그일 확률이
// 훨씬 높기 때문이다.
func suspiciousZeroTemp(tempC float64, now time.Time) bool {
	if tempC != 0 {
		return false
	}
	month := now.In(kst).Month()
	return month != time.December && month != time.January && month != time.February
}

// sanityCheckDayForecast는 하루의 오전/오후 값이 실제 날씨가 아니라 잘못된
// 데이터로 보일 때 그중 하나 또는 둘 다를 Available:false로 지운다 —
// maxPlausibleSameDaySwingC와 suspiciousZeroTemp 참고. 실제로 값을 폐기할
// 때마다 로그를 남기는데, 이런 일은 드물게 일어나야 정상이기 때문이다.
func sanityCheckDayForecast(day *DayForecast, cityKey, dayLabel string, now time.Time) {
	if day.Morning.Available && suspiciousZeroTemp(day.Morning.TemperatureC, now) {
		log.Printf("날씨(%s): %s 오전 기온이 의심스러운 0.0도 — 데이터 없음으로 대체", cityKey, dayLabel)
		day.Morning = PeriodForecast{}
	}
	if day.Afternoon.Available && suspiciousZeroTemp(day.Afternoon.TemperatureC, now) {
		log.Printf("날씨(%s): %s 오후 기온이 의심스러운 0.0도 — 데이터 없음으로 대체", cityKey, dayLabel)
		day.Afternoon = PeriodForecast{}
	}
	if day.Morning.Available && day.Afternoon.Available {
		swing := day.Afternoon.TemperatureC - day.Morning.TemperatureC
		if swing < 0 {
			swing = -swing
		}
		if swing >= maxPlausibleSameDaySwingC {
			log.Printf("날씨(%s): %s 오전(%.1f)/오후(%.1f) 기온 차이가 %.0f도로 비정상적 — 둘 다 데이터 없음으로 대체",
				cityKey, dayLabel, day.Morning.TemperatureC, day.Afternoon.TemperatureC, swing)
			day.Morning = PeriodForecast{}
			day.Afternoon = PeriodForecast{}
		}
	}
}

func sanityCheckForecast(data *WeatherData, cityKey string) {
	now := time.Now()
	sanityCheckDayForecast(&data.Forecast.Today, cityKey, "오늘", now)
	sanityCheckDayForecast(&data.Forecast.Tomorrow, cityKey, "내일", now)
}

// classifyKMAFailureReason은 (여러 겹으로 감싸져 있을 수 있는) 기상청 에러를
// 폴백 로그 한 줄에 남길 짧은 사유 태그로 바꾼다. 매번 로그에 전체 에러
// 체인을 그대로 쏟아내지 않기 위함이다.
func classifyKMAFailureReason(err error) string {
	switch {
	case errors.Is(err, errKMAServiceKeyMissing):
		return "인증키 미설정"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "API 오류"
	}
}

func fetchWeatherOpenMeteo(ctx context.Context, cityKey string, coord cityCoord) (*WeatherData, error) {
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%g&longitude=%g"+
			"&current_weather=true&hourly=temperature_2m,weathercode,precipitation_probability"+
			"&timezone=Asia/Seoul&forecast_days=2",
		coord.Lat, coord.Lon,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo returned status %d", resp.StatusCode)
	}

	var parsed openMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	current := CurrentWeather{
		City:         cityKey,
		CityLabel:    coord.Label,
		TemperatureC: parsed.CurrentWeather.Temperature,
		WindSpeedKph: parsed.CurrentWeather.Windspeed,
		WeatherCode:  parsed.CurrentWeather.Weathercode,
		Description:  weathercodeDescription(parsed.CurrentWeather.Weathercode),
		ObservedAt:   parsed.CurrentWeather.Time,
		DetailURL:    weatherDetailURL(coord),
	}

	return &WeatherData{
		Current:  current,
		Forecast: buildForecast(parsed.Hourly.Time, parsed.Hourly.Temperature2m, parsed.Hourly.Weathercode, parsed.Hourly.PrecipitationProbability, time.Now()),
	}, nil
}

const (
	forecastMorningHour   = "08:00"
	forecastAfternoonHour = "14:00"
)

// buildForecast는 시간대를 집계하는 대신, 오늘/내일의 08:00와 14:00
// 시각별 값을 하나씩 골라낸다(timezone=Asia/Seoul로 요청했으므로 hourly.time은
// 이미 "2026-07-28T08:00"처럼 KST 현지 시각 문자열이다) — 이제 "오전"/"오후"는
// 6시간 평균이 아니라 "정확히 그 시각의 값"을 의미한다.
//
// today/tomorrow는 `referenceTime`을 KST로 변환해 계산하고, hourly.time과는
// 배열 위치가 아니라 정확한 문자열 일치로 매칭한다. 따라서 Open-Meteo가
// 두 날짜를 어떤 순서로 반환하든 자정 경계가 잘못 매칭될 일이 없다.
func buildForecast(times []string, temps []float64, codes []int, precipProbs []int, referenceTime time.Time) WeatherForecast {
	now := referenceTime.In(kst)
	todayStr := now.Format("2006-01-02")
	tomorrowStr := now.AddDate(0, 0, 1).Format("2006-01-02")

	indexByTime := make(map[string]int, len(times))
	for i, t := range times {
		indexByTime[t] = i
	}

	at := func(dateStr, hourMinute string) PeriodForecast {
		idx, ok := indexByTime[dateStr+"T"+hourMinute]
		if !ok || idx >= len(temps) || idx >= len(codes) || idx >= len(precipProbs) {
			log.Printf("날씨(Open-Meteo): %s %s 슬롯을 hourly.time에서 찾지 못함(idx=%d, ok=%v, len=%d/%d/%d) — 데이터 없음으로 처리",
				dateStr, hourMinute, idx, ok, len(temps), len(codes), len(precipProbs))
			return PeriodForecast{}
		}

		code := codes[idx]
		log.Printf("날씨(Open-Meteo): %s %s -> idx=%d 기온=%.1f", dateStr, hourMinute, idx, temps[idx])
		return PeriodForecast{
			TemperatureC:      temps[idx],
			WeatherCode:       code,
			Description:       weathercodeDescription(code),
			PrecipProbability: precipProbs[idx],
			Available:         true,
		}
	}

	return WeatherForecast{
		Today: DayForecast{
			Morning:   at(todayStr, forecastMorningHour),
			Afternoon: at(todayStr, forecastAfternoonHour),
		},
		Tomorrow: DayForecast{
			Morning:   at(tomorrowStr, forecastMorningHour),
			Afternoon: at(tomorrowStr, forecastAfternoonHour),
		},
	}
}
