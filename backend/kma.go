package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- LCC 격자 좌표 변환 ----------

// 기상청 단기예보 격자는 위경도를 그대로 쓰지 않고 람베르트 정각원추도법(LCC)
// 투영을 사용한다. 아래 상수들은 기상청이 공식 발표한 이 투영법의 계수값이며
// (문서: 기상청 단기예보 격자체계) — 임의로 조정하면 안 되는 값이다.
const (
	kmaRE    = 6371.00877 // 지도반경 (km)
	kmaGrid  = 5.0        // 격자간격 (km)
	kmaSlat1 = 30.0       // 투영위도1 (degree)
	kmaSlat2 = 60.0       // 투영위도2 (degree)
	kmaOlon  = 126.0      // 기준점 경도 (degree)
	kmaOlat  = 38.0       // 기준점 위도 (degree)
	kmaXo    = 43.0       // 기준점 X좌표 (GRID)
	kmaYo    = 136.0      // 기준점 Y좌표 (GRID)
)

// latLonToGrid는 WGS84 위경도(도 단위)를 기상청 표준 공식을 사용해
// 기상청 격자 좌표(nx, ny)로 변환한다. 서울/부산/인천 기준으로 검증했으며,
// 기상청이 공식 발표한 격자값과 정확히 일치한다. 이 함수는 범용 변환기로
// 남겨둔 것으로(추후 국내 도시를 더 추가할 때를 위함) — 실제 런타임에 쓰이는
// 값은 아래 domesticGrid를 참고할 것. 그쪽 값은 이 함수의 출력이 아니라
// 기상청이 공식 발표한 격자점 값인데, weatherDetailURL 등에 쓰이는 도시의
// "대표" 위경도가 격자 경계 근처에서는 기상청 자체 기준점과 한 칸(5km) 정도
// 어긋날 수 있기 때문이다(대구가 그 사례다: 이 함수는 cityCoords에 있는
// 대구 좌표에 대해 ny=91을 출력하는데, 기상청이 발표한 ny=90보다 한 칸 벗어나 있다).
func latLonToGrid(lat, lon float64) (nx, ny int) {
	const degRad = math.Pi / 180.0
	re := kmaRE / kmaGrid
	slat1 := kmaSlat1 * degRad
	slat2 := kmaSlat2 * degRad
	olon := kmaOlon * degRad
	olat := kmaOlat * degRad

	sn := math.Log(math.Cos(slat1)/math.Cos(slat2)) /
		math.Log(math.Tan(math.Pi*0.25+slat2*0.5)/math.Tan(math.Pi*0.25+slat1*0.5))
	sf := math.Pow(math.Tan(math.Pi*0.25+slat1*0.5), sn) * math.Cos(slat1) / sn
	ro := re * sf / math.Pow(math.Tan(math.Pi*0.25+olat*0.5), sn)

	ra := re * sf / math.Pow(math.Tan(math.Pi*0.25+lat*degRad*0.5), sn)
	theta := lon*degRad - olon
	if theta > math.Pi {
		theta -= 2 * math.Pi
	}
	if theta < -math.Pi {
		theta += 2 * math.Pi
	}
	theta *= sn

	x := math.Floor(ra*math.Sin(theta) + kmaXo + 0.5)
	y := math.Floor(ro - ra*math.Cos(theta) + kmaYo + 0.5)
	return int(x), int(y)
}

// domesticGrid는 cityCoords에 있는 국내 도시들에 대해 기상청이 공식 발표한
// 격자 좌표를 담는다. 해외 도시는 이 맵에 없다 — 기상청 예보 범위 밖이라
// 항상 Open-Meteo를 사용하기 때문이다(weather.go의 fetchWeather 참고).
var domesticGrid = map[string][2]int{
	"seoul":   {60, 127},
	"daegu":   {89, 90},
	"busan":   {98, 76},
	"incheon": {55, 124},
}

func isDomesticCity(cityKey string) bool {
	_, ok := domesticGrid[cityKey]
	return ok
}

// ---------- base_date/base_time 선택 ----------

// ultraSrtNcstBaseDateTime은 getUltraSrtNcst(초단기실황조회)에 사용할
// base_date/base_time을 결정한다. 기상청은 한 시간의 관측값을 그 시각으로부터
// 약 40분이 지나야 확정하므로, 40분 이전에는 이전 시각의 데이터를 요청해야
// 한다 — 예를 들어 14:35에 요청하면 13:00 데이터를, 14:41에 요청하면
// 14:00 데이터를 요청하게 된다.
func ultraSrtNcstBaseDateTime(now time.Time) (baseDate, baseTime string) {
	now = now.In(kst)
	base := now
	if now.Minute() < 40 {
		base = now.Add(-1 * time.Hour)
	}
	return base.Format("20060102"), base.Format("15") + "00"
}

// vilageFcstIssueHours는 getVilageFcst(단기예보조회)의 하루 8회 발표
// 시각(KST 기준 시)이다. 각 시각은 표시된 시각으로부터 약 10분 후에 확정되어
// 조회 가능해진다(예: 14시 발표분은 14:10부터 조회 가능).
var vilageFcstIssueHours = []int{23, 20, 17, 14, 11, 8, 5, 2}

// vilageFcstBaseDateTime은 기상청의 1일 8회 발표 일정과 약 10분의 발표
// 지연을 감안했을 때, 이미 발표되어 있을 것으로 예상되는 가장 최근의
// getVilageFcst base_date/base_time을 결정한다.
func vilageFcstBaseDateTime(now time.Time) (baseDate, baseTime string) {
	now = now.In(kst)
	for _, h := range vilageFcstIssueHours {
		issuedAt := time.Date(now.Year(), now.Month(), now.Day(), h, 10, 0, 0, kst)
		if !now.Before(issuedAt) {
			return now.Format("20060102"), fmt.Sprintf("%02d00", h)
		}
	}
	// 오늘 02:10 이전인 경우 — 가장 최근 발표분은 어제 23:00 발표분이다.
	yesterday := now.AddDate(0, 0, -1)
	return yesterday.Format("20060102"), "2300"
}

// vilageFcstBaseDateTimeBeforeSlot은 vilageFcstBaseDateTime과 반대되는
// 질문에 답한다: "지금 기준으로 가장 최근 발표는 무엇인가"가 아니라,
// "(dateStr, hourMinute) 슬롯이 아직 미래였을 때의 발표 중 가장 최근 것은
// 무엇인가"이다. getVilageFcst의 발표 하나하나는 "그 발표 시각 기준으로
// 그날 남은 시간에 대한 예보 스냅샷"이므로, 슬롯 시각보다 먼저 발표된
// 회차를 골라두면 그 슬롯은 그 회차의 응답 안에서는 언제나(발표 시각이
// 아무리 지난 뒤에 다시 조회하더라도) 여전히 "미래" 시각으로 남아있다 —
// vilageFcstBaseDateTime처럼 "지금" 기준으로 최신 회차를 고르면, 그
// 회차의 기준 발표 시각이 슬롯을 지나쳐버린 순간부터는 그 슬롯이 응답에서
// 영구히 사라진다. vilageFcstIssueHours는 내림차순이므로, 슬롯 시각보다
// 이르면서(그래야 슬롯이 그 회차의 미래 구간에 들어간다) 이미 발표된
// (issuedAt에 now가 아니라 slotTime을 기준으로 10분을 더한다 — 이 함수는
// 항상 이미 지난 슬롯에 대해서만 호출되므로 그 발표 자체도 당연히 이미
// 끝난 상태다) 것 중 가장 늦은(=슬롯에 가장 가까운, 그래서 가장 최신인)
// 회차를 첫 매치로 찾는다.
func vilageFcstBaseDateTimeBeforeSlot(dateStr, hourMinute string, now time.Time) (baseDate, baseTime string, ok bool) {
	slotTime, err := time.ParseInLocation("2006-01-02 15:04", dateStr+" "+hourMinute, kst)
	if err != nil {
		return "", "", false
	}

	for _, h := range vilageFcstIssueHours {
		issuedAt := time.Date(slotTime.Year(), slotTime.Month(), slotTime.Day(), h, 10, 0, 0, kst)
		if issuedAt.Before(slotTime) && !now.Before(issuedAt) {
			return slotTime.Format("20060102"), fmt.Sprintf("%02d00", h), true
		}
	}
	return "", "", false
}

// ---------- 기상청 날씨 코드 -> WMO 유사 코드 변환(Open-Meteo와 공용) ----------

// kmaWeatherCode는 기상청의 PTY(강수형태) / SKY(하늘상태) 카테고리 값을
// weathercodeDescription이 이미 이해하는 WMO 스타일 코드 체계로 매핑한다.
// 이렇게 하면 데이터 출처가 무엇이든 프런트엔드의 아이콘/설명 로직을 그대로
// 사용할 수 있다. 강수가 있을 때는 SKY보다 PTY를 우선한다. sky는 ""일 수
// 있는데(getUltraSrtNcst는 하늘상태를 아예 보고하지 않고 강수형태만 보고함),
// 이 경우 PTY가 "0"이면 코드 0(맑음)으로 기본 처리한다; 이는 매핑상의 버그가
// 아니라 해당 엔드포인트의 알려진 한계다.
func kmaWeatherCode(pty, sky string) int {
	switch pty {
	case "1": // 비
		return 61
	case "2": // 비/눈
		return 61
	case "3": // 눈
		return 71
	case "4": // 소나기
		return 80
	case "5": // 빗방울
		return 51
	case "6": // 빗방울눈날림
		return 61
	case "7": // 눈날림
		return 71
	}

	switch sky {
	case "3": // 구름많음
		return 2
	case "4": // 흐림
		return 3
	default: // "1"(맑음) 또는 값이 없거나 알 수 없는 경우
		return 0
	}
}

// ---------- HTTP 요청 및 응답 파싱 ----------

const kmaBaseURL = "https://apis.data.go.kr/1360000/VilageFcstInfoService_2.0"

type kmaResponse struct {
	Response struct {
		Header struct {
			ResultCode string `json:"resultCode"`
			ResultMsg  string `json:"resultMsg"`
		} `json:"header"`
		Body struct {
			Items struct {
				Item []kmaItem `json:"item"`
			} `json:"items"`
		} `json:"body"`
	} `json:"response"`
}

type kmaItem struct {
	Category  string `json:"category"`
	ObsrValue string `json:"obsrValue"`
	FcstDate  string `json:"fcstDate"`
	FcstTime  string `json:"fcstTime"`
	FcstValue string `json:"fcstValue"`
}

var kmaXMLErrMsg = regexp.MustCompile(`<errMsg>(.*?)</errMsg>`)

// kmaRequest는 getUltraSrtNcst/getVilageFcst 형태의 엔드포인트를 호출하고
// item 목록을 반환한다. data.go.kr 엔드포인트는 dataType=JSON을 요청해도
// 오류 시에는 XML 에러 응답으로 돌아오는 경우가 있어(이 API 계열의 잘 알려진
// 특성이다), JSON이 아닌 응답을 json.Unmarshal에서 알아보기 힘든 에러로
// 넘기지 않고 명시적으로 먼저 감지한다.
func kmaRequest(ctx context.Context, endpoint string, params url.Values) ([]kmaItem, error) {
	serviceKey := os.Getenv("KMA_SERVICE_KEY")
	if serviceKey == "" {
		return nil, errKMAServiceKeyMissing
	}

	// data.go.kr이 발급하는 serviceKey는 이미 URL 인코딩되어 있다("Encoding"
	// 키) — 이를 url.Values.Encode()에 한 번 더 통과시키면 이중 인코딩되어
	// 요청이 거부되므로, 쿼리 문자열에 그대로(as-is) 이어붙인다.
	reqURL := kmaBaseURL + "/" + endpoint + "?serviceKey=" + serviceKey + "&" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("기상청 API 상태 코드 %d: %s", resp.StatusCode, string(body))
	}

	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "<") {
		if m := kmaXMLErrMsg.FindStringSubmatch(trimmed); m != nil {
			return nil, fmt.Errorf("기상청 API 오류: %s", m[1])
		}
		return nil, fmt.Errorf("기상청 API가 JSON이 아닌 응답을 반환함: %.200s", trimmed)
	}

	var parsed kmaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if parsed.Response.Header.ResultCode != "00" {
		return nil, fmt.Errorf("기상청 API 오류(%s): %s", parsed.Response.Header.ResultCode, parsed.Response.Header.ResultMsg)
	}

	return parsed.Response.Body.Items.Item, nil
}

// kmaHTTPTimeout은 kmaRequest 한 번의 시도 시간을 제한한다. data.go.kr의
// 게이트웨이는 일부 요청에서 눈에 띄게 응답이 지연되는데(keep-alive를 꺼도
// 약 6번에 1번꼴로 관측됨 — 클라이언트 문제가 아니라 그쪽 인프라의 실제
// 특성이다), 성공하는 호출은 대부분 1~2초 내에 끝난다. 예전에는 이 값을
// 2.5초로 짧게 잡고 재시도로 대부분의 지연을 회복시키려 했지만, 실제로는
// 이 여유가 너무 빠듯해서(kmaSubTimeout 참고) 결국 응답이 오고 있었을
// 요청까지 Open-Meteo 폴백으로 넘기는 경우가 잦았다. 기상청 실측치를
// 최대한 살리는 쪽을 우선하기로 하고 4초로 늘렸다 — 대신 아래
// kmaSubTimeout/handler.go의 weatherSectionTimeout도 함께 늘려서, 늘어난
// 재시도 예산이 그대로 잘려나가지 않도록 했다.
const kmaHTTPTimeout = 4 * time.Second

func kmaRequestWithRetry(ctx context.Context, endpoint string, params url.Values) ([]kmaItem, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, kmaHTTPTimeout)
		items, err := kmaRequest(attemptCtx, endpoint, params)
		cancel()
		if err == nil {
			return items, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// ---------- getUltraSrtNcst (초단기실황조회) ----------

func fetchKMACurrent(ctx context.Context, nx, ny int) (*CurrentWeather, error) {
	baseDate, baseTime := ultraSrtNcstBaseDateTime(time.Now())

	params := url.Values{}
	params.Set("pageNo", "1")
	params.Set("numOfRows", "20")
	params.Set("dataType", "JSON")
	params.Set("base_date", baseDate)
	params.Set("base_time", baseTime)
	params.Set("nx", strconv.Itoa(nx))
	params.Set("ny", strconv.Itoa(ny))

	items, err := kmaRequestWithRetry(ctx, "getUltraSrtNcst", params)
	if err != nil {
		return nil, err
	}

	values := make(map[string]string, len(items))
	for _, it := range items {
		values[it.Category] = it.ObsrValue
	}

	tempStr, ok := values["T1H"]
	if !ok {
		return nil, fmt.Errorf("기상청 초단기실황 응답에 T1H(기온)가 없음")
	}
	temp, err := strconv.ParseFloat(tempStr, 64)
	if err != nil {
		return nil, fmt.Errorf("기상청 T1H 파싱 실패: %w", err)
	}

	var windKph float64
	if wsdStr, ok := values["WSD"]; ok {
		if wsd, err := strconv.ParseFloat(wsdStr, 64); err == nil {
			windKph = wsd * 3.6 // 기상청 WSD는 m/s 단위이고, 여기 필드는 kph 단위
		}
	}

	code := kmaWeatherCode(values["PTY"], "")
	observedAt := baseDate[:4] + "-" + baseDate[4:6] + "-" + baseDate[6:8] + "T" + baseTime[:2] + ":" + baseTime[2:]

	return &CurrentWeather{
		TemperatureC: temp,
		WindSpeedKph: windKph,
		WeatherCode:  code,
		Description:  weathercodeDescription(code),
		ObservedAt:   observedAt,
	}, nil
}

// ---------- getVilageFcst (단기예보조회) ----------

func fetchKMAForecast(ctx context.Context, nx, ny int) (*WeatherForecast, error) {
	baseDate, baseTime := vilageFcstBaseDateTime(time.Now())
	return fetchKMAForecastAt(ctx, nx, ny, baseDate, baseTime)
}

// fetchKMAForecastAt은 "가장 최근 발표"를 스스로 계산하지 않고, 명시적인
// base_date/base_time으로 getVilageFcst를 호출한다. fetchKMAForecast(위)는
// 이 함수를 감싸서 항상 최신 발표를 쓰는 정상 실시간 경로이고,
// weather_slot_cache.go의 backfillPastSlotFromEarlierVilageFcstRun이 이
// 함수의 또 다른 호출부다 — 그쪽은 일부러 "최신"보다 이전에 발표된
// 회차를 요청하는데, 최신 발표는 이미 지나가버린 슬롯을 더 이상 포함하지
// 않기 때문이다.
func fetchKMAForecastAt(ctx context.Context, nx, ny int, baseDate, baseTime string) (*WeatherForecast, error) {
	params := url.Values{}
	params.Set("pageNo", "1")
	params.Set("numOfRows", "1000")
	params.Set("dataType", "JSON")
	params.Set("base_date", baseDate)
	params.Set("base_time", baseTime)
	params.Set("nx", strconv.Itoa(nx))
	params.Set("ny", strconv.Itoa(ny))

	items, err := kmaRequestWithRetry(ctx, "getVilageFcst", params)
	if err != nil {
		return nil, err
	}

	// 호출부(특히 backfillPastSlotFromEarlierVilageFcstRun의 05시/02시 등
	// 소급 조회)가 실제로 이 base_date/base_time으로 요청을 보냈는지, 그리고
	// 그 응답이 진짜 빈 배열(items=0 — 기상청이 그 회차 자체를 아직 채우지
	// 않았거나 격자 좌표가 어긋난 것으로 의심되는 상황)이었는지, 아니면
	// 항목은 있었지만 그중 우리가 찾는 슬롯만 없었는지를 구분할 수 있도록
	// 매 응답의 원본 항목 수를 남긴다 — kmaRequestWithRetry가 에러 자체는
	// 이미 상세히 반환/로깅하므로(HTTP 상태, resultCode 등), 여기서는 "에러
	// 없이 성공했지만 내용이 비어 있었다"는, 그와는 별개의 경우를 채운다.
	log.Printf("날씨(KMA 단기예보): base_date=%s base_time=%s 조회 응답 %d건 수신", baseDate, baseTime, len(items))

	return buildKMAForecast(items, time.Now()), nil
}

// buildKMAForecast는 Open-Meteo용 buildForecast와 동일한 방식을 따른다:
// 오늘/내일의 08:00, 14:00 값을 배열 위치가 아니라 정확한 날짜+시각 문자열
// 일치로 골라내고, getVilageFcst가 아직 예보하지 않은 슬롯은(예: 이미 지나간
// 오늘 08:00) 값이 비어 있는 PeriodForecast로 처리한다.
func buildKMAForecast(items []kmaItem, referenceTime time.Time) *WeatherForecast {
	type slot struct{ tmp, pop, pty, sky string }
	slots := make(map[string]*slot)

	for _, it := range items {
		if len(it.FcstDate) != 8 || len(it.FcstTime) != 4 {
			continue
		}
		key := it.FcstDate[:4] + "-" + it.FcstDate[4:6] + "-" + it.FcstDate[6:8] + "T" + it.FcstTime[:2] + ":" + it.FcstTime[2:]
		s, ok := slots[key]
		if !ok {
			s = &slot{}
			slots[key] = s
		}
		switch it.Category {
		case "TMP":
			s.tmp = it.FcstValue
		case "POP":
			s.pop = it.FcstValue
		case "PTY":
			s.pty = it.FcstValue
		case "SKY":
			s.sky = it.FcstValue
		}
	}

	now := referenceTime.In(kst)
	todayStr := now.Format("2006-01-02")
	tomorrowStr := now.AddDate(0, 0, 1).Format("2006-01-02")

	at := func(dateStr, hourMinute string) PeriodForecast {
		key := dateStr + "T" + hourMinute
		s, ok := slots[key]
		if !ok {
			// 이미 지나간 당일 슬롯이라면 정상적인 상황이다 — 기상청의 getVilageFcst는
			// base_time 이후 시점만 예보할 뿐, 당일 이전 시각을 소급해서 채워주지 않는다.
			log.Printf("날씨(KMA 단기예보): %s 슬롯이 응답에 없음(아직 발표 전이거나 이미 지난 시각) — 데이터 없음으로 처리", key)
			return PeriodForecast{}
		}
		temp, err := strconv.ParseFloat(s.tmp, 64)
		if err != nil {
			log.Printf("날씨(KMA 단기예보): %s TMP 파싱 실패(raw=%q): %v — 데이터 없음으로 처리", key, s.tmp, err)
			return PeriodForecast{}
		}
		pop, _ := strconv.Atoi(s.pop)
		code := kmaWeatherCode(s.pty, s.sky)
		log.Printf("날씨(KMA 단기예보): %s -> 기온=%.1f (PTY=%s SKY=%s)", key, temp, s.pty, s.sky)
		return PeriodForecast{
			TemperatureC:      temp,
			WeatherCode:       code,
			Description:       weathercodeDescription(code),
			PrecipProbability: pop,
			Available:         true,
		}
	}

	return &WeatherForecast{
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

// ---------- 최상위 디스패치 ----------

var errKMAServiceKeyMissing = fmt.Errorf("KMA_SERVICE_KEY 환경변수가 설정되지 않았습니다")

// kmaSubTimeout은 기상청 API 호출에 걸리는 시간을 제한하여, 응답이
// 느리거나 없는 경우에도 아래쪽 Open-Meteo 폴백에 쓸 시간이 섹션 전체
// 타임아웃(handler.go의 weatherSectionTimeout 참고) 안에 충분히 남도록
// 한다. 현재 날씨와 예보 호출이 각각 최대 재시도 예산(kmaHTTPTimeout*2 =
// 8초)을 모두 써야 하는 최악의 경우까지 커버해야 하는데, fetchWeatherKMA가
// 이 둘을 순차가 아니라 동시에 실행함으로써 이를 흡수한다 — 9초로 잡아
// 8초 worst-case에 스케줄링 오버헤드 여유를 조금 더 얹었다.
const kmaSubTimeout = 9 * time.Second

// fetchWeatherKMA는 domesticGrid에 있는 국내 도시 하나에 대해 현재 날씨와
// 오늘/내일 08:00, 14:00 예보를 기상청으로부터 동시에 가져온다(서로 독립적인
// 호출이라 병렬로 실행하면, 각각 재시도를 포함한 최악의 경우 소요 시간이
// 두 배가 되지 않고 kmaSubTimeout 이내로 유지된다). 둘 중 하나라도 실패하면
// 전체를 실패로 처리한다 — 호출자는 한쪽만 대체하는 대신 둘 다 Open-Meteo로
// 폴백한다.
func fetchWeatherKMA(ctx context.Context, cityKey string, coord cityCoord) (*WeatherData, error) {
	grid, ok := domesticGrid[cityKey]
	if !ok {
		return nil, fmt.Errorf("%s는 기상청 격자 좌표가 없는 도시입니다", cityKey)
	}
	nx, ny := grid[0], grid[1]

	var current *CurrentWeather
	var forecast *WeatherForecast
	var currentErr, forecastErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		current, currentErr = fetchKMACurrent(ctx, nx, ny)
	}()
	go func() {
		defer wg.Done()
		forecast, forecastErr = fetchKMAForecast(ctx, nx, ny)
	}()
	wg.Wait()

	if currentErr != nil {
		return nil, fmt.Errorf("초단기실황 조회 실패: %w", currentErr)
	}
	if forecastErr != nil {
		return nil, fmt.Errorf("단기예보 조회 실패: %w", forecastErr)
	}

	current.City = cityKey
	current.CityLabel = coord.Label
	current.DetailURL = weatherDetailURL(coord)

	return &WeatherData{Current: *current, Forecast: *forecast}, nil
}
