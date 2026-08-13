package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// itTermGlossary는 해외(international) 모드 헤드라인 번역에서만 쓰인다.
// 예전에는 AI 브리핑의 뉴스 섹션(briefing.go)과도 공유했는데, 그 프롬프트는
// 국내/해외 모든 카테고리(정치/경제/사회/스포츠 등)에 공통으로 쓰이다 보니
// IT 용어집이 늘 포함되어 있으면 IT와 무관한 기사까지 기술 관련 내용으로
// 왜곡하는 원인이 됐다 — 그래서 용어집은 번역이 실제로 필요한 여기(해외
// 모드 번역)에만 남기고, briefing.go의 newsSectionSystemPrompt에서는
// 제거했다. 아래 프롬프트도 등장 시에만 참고하도록 조건부로 안내한다.
// 각 줄에 들여쓰기와 화살표 기호를 넣은 불릿 목록 대신 쉼표로 이어진 한 줄
// 인라인 목록으로 압축했다 — 같은 용어 매핑 정보를 담으면서도 서식 문자에
// 드는 토큰을 줄인다.
const itTermGlossary = `IT/AI 전문 용어는 자연스러운 한국어 관용 표현으로 번역하되, 업계에서 이미 굳어진 표현이 있으면 그대로 사용하세요. 자주 쓰는 용어: frontier model=최상위 모델, fine-tune=파인튜닝, RL=강화학습, open model/open source=오픈소스 모델, open-weights=오픈 웨이트, benchmark=벤치마크, inference=추론, training=학습, dataset=데이터셋, parameter=파라미터, prompt=프롬프트, agent=에이전트, context window=컨텍스트 윈도우, token=토큰, latency=지연 시간.`

// newsTranslationSystemPrompt의 페르소나는 특정 분야에 못박혀 있지 않다 —
// 이 번역기는 top/business/technology/sports/entertainment/health/science
// 등 모든 카테고리의 해외 헤드라인을 다루므로("기술/IT 뉴스 전용 번역가"로
// 소개했던 예전 문구는 Hacker News 전용이던 시절의 흔적이었다), 실제
// 기사의 분야를 임의로 바꾸지 말라는 지침을 명시적으로 넣었다.
//
// 0번 규칙의 의학/과학/법률 전문 용어 문장은 briefing.go의
// newsSectionSystemPrompt 규칙 5번과 같은 이유(실제 보고된 "belly size" →
// "배圍" 혼종 표현 사례)로 추가됐다 — 이 프롬프트는 헤드라인 번역이라는
// 별개의 Groq 호출이지만, 영어 원문을 한국어로 옮기는 동안 똑같이
// 전문 용어를 한자로 정확히 옮기려다 실패할 수 있어 같은 문구를
// 동일하게 반영했다. 두 프롬프트가 상수를 공유하지는 않으므로(하나는
// briefingCommonRules 기반, 하나는 독립적인 문자열), 이런 종류의 규칙을
// 바꿀 때는 두 곳 모두 함께 갱신해야 한다.
const newsTranslationSystemPrompt = `당신은 다양한 분야(정치, 경제, 사회, 문화, 스포츠, 기술 등)의 뉴스 헤드라인을 한국어로 번역하는 전문 번역가입니다. 원문의 실제 주제나 분야를 임의로 다른 분야(기술, AI 등)로 바꾸거나 재해석하지 말고, 있는 그대로 번역하세요.
0. 번역문은 순수 한국어만 사용하세요 — 한자·중국어·일본어 문자는 절대 쓰지 마세요(숫자는 예외). 원문에 등장하는 지명·인명이 한자나 가나로 표기되어 있어도, 통용되는 한국어 표기로 옮기세요(고유명사를 원문의 로마자/한글 그대로 유지하는 것은 허용되지만 한자·가나로는 절대 남기지 마세요). 의학·과학·법률 등 전문 용어도 한자를 섞지 말고 한글로만 쓰고(예: "belly size" → "배 둘레", 한자 "腹圍" 금지), 옮기기 애매하면 쉬운 말로 풀어서 번역하세요.
1. 원문의 의미를 정확히 유지하면서 자연스러운 한국어 뉴스 제목체로 번역하세요 (예: "~ 발표", "~ 공개", "~ 논란" 같은 뉴스 제목 어투).
2. 고유명사(회사명, 프로젝트명, 인명)는 번역하지 말고 원문 그대로 유지하세요.
3. 원문에 있던 숫자+단위(K/M/B) 표현은 이미 정확한 한국어 환산값으로 전부 바뀌어서 전달됩니다 (예: 원문의 "9B"는 "90억"으로, "$6.6B"는 "66억 달러"로 이미 바뀌어 있음). 번역문에는 그 한국어 값을 그대로 사용하세요. "M"이나 "B" 같은 원래 단위 알파벳은 데이터에 남아있지 않으니, 있지도 않은 단위를 상상해서 다시 계산하거나 다른 숫자로 바꾸지 마세요. 만약 이렇게 이미 변환되어 있지 않은 숫자+단위가 있다면, K=천, M=백만, B=십억(10억) 기준으로 정확히 계산해서 변환하세요.
4. 기사에 IT/AI 관련 전문 용어가 등장하면 다음을 참고하세요(등장하지 않는 기사라면 무시하세요): ` + itTermGlossary + `
5. 영어 원문의 어순을 그대로 따라가지 말고, 한국어 뉴스 제목으로 자연스럽게 재구성하세요. 예: "City council approves $2M transit budget increase after months of debate" → "시의회, 수개월간의 논의 끝에 대중교통 예산 200만 달러 증액 승인" 같은 자연스러운 기사 제목 형태로 재구성하세요.
6. 번역을 완료한 후, 원문에 등장한 모든 숫자(단위 포함)가 번역문에도 정확히 반영되었는지 다시 한번 확인하세요.
7. 반드시 요청받은 개수만큼, 요청 순서와 id를 그대로 유지해서 JSON 배열로만 응답하세요. 다른 설명은 절대 추가하지 마세요.`

// newsTranslationUpsertTimeout은 캐시 저장(INSERT) 자체의 타임아웃이다.
// context.Background()에서 독립적으로 파생시킨다 — raw_data_cache.go의
// rawCacheUpsertTimeout과 같은 이유다: 호출자의 요청 스코프 ctx를 그대로
// 쓰면, 느린 Groq 배치 번역 호출이 요청 타임아웃 예산을 거의 다 써버린
// 뒤 성공하는 경우에도 방금 받아온 번역을 저장하려는 순간 컨텍스트가
// 이미 만료돼 있어 저장 자체가 실패할 수 있다.
const newsTranslationUpsertTimeout = 5 * time.Second

// lookupNewsTranslation은 성공적으로 캐시된 번역만 반환한다 — 실패 기록
// (translated_title이 빈 문자열인 행, recordNewsTranslationFailure 참고)은
// 일부러 조건에서 제외해 found=false로 취급한다. "캐시된 성공"과
// "쿨다운 중인 실패"를 서로 다른 함수(이 함수 vs
// recentlyFailedNewsTranslation)로 분리해두면, 예전에 있었던 버그(빈
// 문자열 행을 캐시 성공으로 잘못 판단해 원문 표시가 영구 고정되던 문제)가
// 같은 형태로 재발할 여지가 없다.
func lookupNewsTranslation(ctx context.Context, conn *sql.DB, articleID string) (string, bool) {
	if conn == nil {
		return "", false
	}
	var title string
	err := conn.QueryRowContext(ctx,
		`SELECT translated_title FROM news_translation_cache WHERE article_id = ? AND translated_title != ''`, articleID,
	).Scan(&title)
	if err != nil {
		return "", false
	}
	return title, true
}

// 번역 실패 사유 분류. rate_limit과 그 외(validation_failed/api_error)는
// 서로 다른 쿨다운(newsTranslationCooldownForReason)을 받는다 — rate
// limit은 Groq TPM(분당 토큰) 예산이 그 다음 분(minute) 버킷이면 대개
// 풀려있으니 짧게, 그 외(한자/영어 혼입 같은 콘텐츠 검증 실패나 일반
// API 오류)는 같은 입력을 당장 재시도해도 비슷한 결과가 나올 가능성이
// 있으니 기존처럼 길게 기다린다.
const (
	newsTranslationFailureReasonRateLimit        = "rate_limit"
	newsTranslationFailureReasonValidationFailed = "validation_failed"
	newsTranslationFailureReasonAPIError         = "api_error"
)

// newsTranslationRateLimitCooldown/newsTranslationDefaultFailureCooldown은
// newsTranslationCooldownForReason이 사유별로 고르는 쿨다운 길이다.
// rate_limit만 30초~1분 사이의 짧은 값으로 두고, validation_failed/
// api_error는 예전에 모든 실패에 일괄 적용하던 5분을 그대로 유지한다.
const (
	newsTranslationRateLimitCooldown      = 45 * time.Second
	newsTranslationDefaultFailureCooldown = 5 * time.Minute
)

// newsTranslationCooldownForReason은 실패 사유에 맞는 쿨다운 길이를
// 고른다 — rate_limit만 특별 취급하고 나머지(validation_failed,
// api_error, 혹은 알 수 없는 값)는 모두 기존 기본값을 쓴다.
func newsTranslationCooldownForReason(reason string) time.Duration {
	if reason == newsTranslationFailureReasonRateLimit {
		return newsTranslationRateLimitCooldown
	}
	return newsTranslationDefaultFailureCooldown
}

// classifyNewsTranslationFailureReason은 fetchNewsTranslation이 배치
// 전체에 대해 반환한 에러를 분류한다 — briefing.go의
// classifyBriefingFailureReason과 같은 방식(에러 메시지에 "rate
// limit"/"tokens per minute"/"(tpm)"가 있으면 rate_limit)이다.
// validation_failed는 여기서 나오지 않는다 — 그건 에러가 아니라 성공
// 응답인데 검증(findForeignCJK/findLeakedEnglish)에 실패해 특정 항목의
// translatedTitle만 빈 문자열이 된 경우라서, 그 판단은 이 함수가 아니라
// translateNewsItems가 fetchNewsTranslation의 반환값을 보고 직접 한다.
func classifyNewsTranslationFailureReason(err error) string {
	if err == nil {
		return newsTranslationFailureReasonAPIError
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "tokens per minute") || strings.Contains(msg, "(tpm)") {
		return newsTranslationFailureReasonRateLimit
	}
	return newsTranslationFailureReasonAPIError
}

// recentlyFailedNewsTranslation은 article_id에 대해 아직 유효한(retry_after가
// 지나지 않은) 실패 기록이 news_translation_cache에 있는지 확인한다.
// 실패 사유별로 다른 쿨다운이 이미 recordNewsTranslationFailure가 저장한
// retry_after에 반영되어 있으므로, 여기서는 그 시각이 지났는지만 보면
// 된다. translated_title != ”인 행(성공 캐시)은 애초에 이 조건에
// 걸리지 않는다 — lookupNewsTranslation이 그 경우를 먼저 처리한다.
func recentlyFailedNewsTranslation(ctx context.Context, conn *sql.DB, articleID string) bool {
	if conn == nil {
		return false
	}
	var retryAfterStr sql.NullString
	err := conn.QueryRowContext(ctx,
		`SELECT retry_after FROM news_translation_cache WHERE article_id = ? AND translated_title = ''`, articleID,
	).Scan(&retryAfterStr)
	if err != nil || !retryAfterStr.Valid || retryAfterStr.String == "" {
		return false
	}
	retryAfter, parseErr := time.Parse(time.RFC3339, retryAfterStr.String)
	if parseErr != nil {
		return false
	}
	return time.Now().Before(retryAfter)
}

// recordNewsTranslationFailure는 번역 실패를 사유(reason)와 함께
// news_translation_cache에 기록한다. translated_title은 일부러 빈
// 문자열로 남긴다 — lookupNewsTranslation이 빈 문자열 행을 "캐시된
// 성공 없음"으로 취급해 원문 표시로 폴백하게 하기 위해서다.
// retry_after는 reason별 쿨다운만큼 뒤로 설정되어, 다음 조회 시점에
// recentlyFailedNewsTranslation이 그 시각이 지났는지만 보고 사유별로
// 다른 속도로 재시도를 허용한다.
func recordNewsTranslationFailure(conn *sql.DB, articleID, reason string) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), newsTranslationUpsertTimeout)
	defer cancel()
	now := time.Now()
	retryAfter := now.Add(newsTranslationCooldownForReason(reason)).Format(time.RFC3339)
	_, err := conn.ExecContext(ctx, `
		INSERT INTO news_translation_cache (article_id, translated_title, cached_at, failure_reason, retry_after)
		VALUES (?, '', ?, ?, ?)
		ON CONFLICT(article_id) DO UPDATE SET
			translated_title = '', cached_at = excluded.cached_at,
			failure_reason = excluded.failure_reason, retry_after = excluded.retry_after`,
		articleID, now, reason, retryAfter,
	)
	if err != nil {
		log.Printf("뉴스 번역(%s): 실패 기록 저장 실패: %v", articleID, err)
	}
}

// upsertNewsTranslation은 성공한 번역을 캐시하면서, 혹시 이전에 남아있던
// 실패 기록(failure_reason/retry_after)도 함께 지운다 — 같은 기사가
// 재시도 끝에 성공했는데 예전 실패 사유가 그대로 남아있으면, 이미
// 성공했음에도 다음 조회 때 recentlyFailedNewsTranslation이 혼란을 줄
// 여지가 있기 때문이다(실제로는 translated_title이 채워지는 순간
// lookupNewsTranslation이 먼저 성공으로 처리하므로 안전하지만, 두 컬럼을
// 항상 일관된 상태로 유지해두는 편이 이해하기 쉽다).
func upsertNewsTranslation(conn *sql.DB, articleID, translatedTitle string) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), newsTranslationUpsertTimeout)
	defer cancel()
	_, err := conn.ExecContext(ctx, `
		INSERT INTO news_translation_cache (article_id, translated_title, cached_at, failure_reason, retry_after)
		VALUES (?, ?, ?, '', NULL)
		ON CONFLICT(article_id) DO UPDATE SET
			translated_title = excluded.translated_title, cached_at = excluded.cached_at,
			failure_reason = '', retry_after = NULL`,
		articleID, translatedTitle, time.Now(),
	)
	if err != nil {
		log.Printf("뉴스 번역(%s): 캐시 저장 실패: %v", articleID, err)
	}
}

type newsTranslationRequestItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type newsTranslationResponseItem struct {
	ID              string `json:"id"`
	TranslatedTitle string `json:"translatedTitle"`
}

// translateNewsItems는 각 항목의 TranslatedTitle을 제자리(in place)에서
// 채운다. 캐시된 번역은 그대로 재사용하고, 남은 항목들은 한 번의 배치 Groq
// 호출로 번역한 뒤 다음을 위해 캐시에 저장한다. 번역이 불가능한 항목(키
// 누락, Groq 오류, 또는 모델 응답에 단순히 빠져 있는 경우)은 TranslatedTitle을
// 빈 값으로 남겨둔다 — 뉴스 섹션 전체가 실패하는 대신 프론트엔드가 원문
// 제목으로 대체해서 보여준다.
func translateNewsItems(ctx context.Context, items []NewsItem) {
	if len(items) == 0 {
		return
	}

	toTranslate := make([]NewsItem, 0, len(items))
	cacheHits := 0
	cooldownSkips := 0

	for i := range items {
		if cached, ok := lookupNewsTranslation(ctx, db, items[i].ID); ok {
			items[i].TranslatedTitle = cached
			cacheHits++
		} else if recentlyFailedNewsTranslation(ctx, db, items[i].ID) {
			cooldownSkips++
		} else {
			toTranslate = append(toTranslate, items[i])
		}
	}

	if cacheHits > 0 {
		for i := 0; i < cacheHits; i++ {
			recordGroqCacheHit()
		}
		log.Printf("[캐시 재사용] 뉴스 번역: %d개 항목 캐시 재사용, %d개 신규 번역 필요", cacheHits, len(toTranslate))
	}
	if cooldownSkips > 0 {
		log.Printf("뉴스 번역: 최근 실패한 %d개 항목은 사유별 재시도 쿨다운 중이라 원문 표시로 폴백", cooldownSkips)
	}

	if len(toTranslate) == 0 {
		return
	}

	translated, err := fetchNewsTranslation(ctx, toTranslate)
	if err != nil {
		reason := classifyNewsTranslationFailureReason(err)
		log.Printf("뉴스: 번역 실패(사유=%s, 쿨다운=%s): %v", reason, newsTranslationCooldownForReason(reason), err)
		for _, it := range toTranslate {
			recordNewsTranslationFailure(db, it.ID, reason)
		}
		return
	}

	byID := make(map[string]string, len(translated))
	for _, t := range translated {
		byID[t.ID] = t.TranslatedTitle
	}

	// 성공(비어 있지 않은 번역)만 진짜 번역으로 캐시하고, 검증 실패로 빈
	// 문자열이 된 항목이나 모델 응답에서 통째로 빠진 항목은
	// validation_failed 사유로 쿨다운을 기록한다 — 쿨다운이 지나면 다음
	// 요청에서 다시 번역을 시도할 수 있다.
	for _, it := range toTranslate {
		title, ok := byID[it.ID]
		if !ok || title == "" {
			recordNewsTranslationFailure(db, it.ID, newsTranslationFailureReasonValidationFailed)
			continue
		}
		upsertNewsTranslation(db, it.ID, title)
	}

	for i := range items {
		if title, ok := byID[items[i].ID]; ok {
			items[i].TranslatedTitle = title
		}
	}
}

type newsTranslationEnvelope struct {
	Translations []newsTranslationResponseItem `json:"translations"`
}

// maxNewsTranslationRetries는 번역된 제목 중 하나라도 엄격한 검증(외국어
// CJK 문자 혼입이나 영어 원문 잔존 — briefing.go의 findForeignCJK/
// findLeakedEnglish를 그대로 재사용해서, 두 번역 경로가 무엇을 실패로
// 볼지 서로 일치시킨다)에 실패했을 때 배치 전체에 대해 1회 재시도(총 2회
// 시도)한다는 뜻이다. 재시도할 때는 frequentGroqModel()에서
// escalationGroqModel()로 모델을 승격한다(groq.go 참고) — 원래는 숫자 단위
// 오역 문제 때문에 매 호출마다 더 큰 모델을 쓰는 것이 동기였지만, 실제
// 검증 실패가 발생했을 때만 승격하면 헤드라인 하나하나마다 70B 모델의
// 훨씬 작은 daily quota를 소모하지 않고도 동일한 정확도를 얻을 수 있다.
// 그래도 여전히 문제가 있는 항목은 배치 전체를 버리는 대신 개별적으로만
// 비워낸다. 고집스럽게 실패하는 헤드라인 하나 때문에 잘 번역된 나머지
// 네 개까지 희생시키지 않기 위해서다 — translateNewsItems는 이미 빈
// TranslatedTitle을 "원문을 대신 보여주라"는 뜻으로 처리한다.
const maxNewsTranslationRetries = 1

func fetchNewsTranslation(ctx context.Context, items []NewsItem) ([]newsTranslationResponseItem, error) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		return nil, errGroqKeyMissing
	}

	reqItems := make([]newsTranslationRequestItem, len(items))
	for i, it := range items {
		reqItems[i] = newsTranslationRequestItem{ID: it.ID, Title: annotateNumericUnits(it.Title)}
	}

	reqJSON, err := json.Marshal(reqItems)
	if err != nil {
		return nil, err
	}

	// response_format json_object는 최상위가 JSON *객체*여야 하므로,
	// system prompt가 설명하는 배열은 {"translations": [...]} 형태로
	// 감싸서 전달한다.
	userContent := fmt.Sprintf(
		"다음 %d개의 뉴스 헤드라인을 번역하세요: %s\n\n반드시 {\"translations\": [{\"id\": ..., \"translatedTitle\": \"...\"}, ...]} 형태의 JSON 객체로만 응답하세요.",
		len(items), string(reqJSON),
	)

	var translations []newsTranslationResponseItem
	model := frequentGroqModel()

	for attempt := 1; attempt <= maxNewsTranslationRetries+1; attempt++ {
		// maxTokens=500: estimateTokenCount(groq.go)로 다소 긴 편에 속하는
		// 실제 뉴스 제목 스타일 번역문 5개(각 35~40자) + NewsData.io 스타일
		// article_id(32자)로 구성한 배치 출력 JSON을 측정해보면 약 330
		// 토큰이었다 — 원래 700은 이 실제 필요량 대비 여유가 지나치게
		// 커서, TPM(분당 토큰) 예산을 두고 다른 Groq 호출들과 경쟁하는
		// 상황에서 불필요하게 큰 몫을 예약해두는 셈이었다. 500은 이
		// 측정값 대비 약 50% 여유를 남기면서도(제목이 더 길어지거나
		// 토크나이저가 이 추정치보다 다소 비효율적이어도 흡수할 수 있는
		// 수준), 700 대비 상한을 낮춰 모델이 혹시라도 반복 생성 루프에
		// 빠졌을 때의 최악의 토큰 소비도 함께 줄인다. 다만 max_tokens는
		// 상한일 뿐 실제 소비량이 아니므로 — 정상적으로 성공하는
		// 번역이라면 애초에 700이든 500이든 실제 사용 토큰은 거의
		// 동일하다 — 이 조정의 효과는 주로 그 반복 루프/이례적으로 긴
		// 응답 같은 예외 상황의 상한을 낮추는 데 있다.
		content, callErr := callGroqChat(ctx, apiKey, model, []groqChatMessage{
			{Role: "system", Content: newsTranslationSystemPrompt},
			{Role: "user", Content: userContent},
		}, 0.2, 500, 0, true)
		if callErr != nil {
			return nil, callErr
		}

		var envelope newsTranslationEnvelope
		if jsonErr := json.Unmarshal([]byte(content), &envelope); jsonErr != nil {
			return nil, fmt.Errorf("failed to parse news translation JSON: %w", jsonErr)
		}
		translations = envelope.Translations

		if allNewsTranslationsValid(translations) {
			return translations, nil
		}

		if groqEscalationCountToday() >= maxDailyGroqEscalations {
			log.Printf("뉴스 번역: 한자/영어 혼입 감지되었으나 오늘 70B 승격 횟수가 안전 한도(%d회)에 도달해 승격 없이 마지막 결과를 사용합니다", maxDailyGroqEscalations)
			break
		}
		escalated := escalationGroqModel()
		log.Printf("뉴스 번역: 한자/영어 혼입 감지, 모델 승격 후 배치 재시도 %d/%d (%s -> %s, 오늘 승격 %d/%d회째)",
			attempt, maxNewsTranslationRetries+1, model, escalated, groqEscalationCountToday()+1, maxDailyGroqEscalations)
		model = escalated
	}

	// 재시도 횟수를 모두 소진함 — 여전히 검증에 실패하는 항목만 골라서
	// 비워내면, translateNewsItems에 이미 있는 빈 TranslatedTitle 폴백
	// ("번역 실패, 원문 표시")이 해당 항목에만 적용된다.
	for i, t := range translations {
		if _, found := findForeignCJK(t.TranslatedTitle); found {
			log.Printf("뉴스 번역(%s): 한자/CJK 문자 반복 감지, 원문으로 폴백", t.ID)
			translations[i].TranslatedTitle = ""
			continue
		}
		if _, found := findLeakedEnglish(t.TranslatedTitle); found {
			log.Printf("뉴스 번역(%s): 영어 원문 반복 감지, 원문으로 폴백", t.ID)
			translations[i].TranslatedTitle = ""
		}
	}

	return translations, nil
}

func allNewsTranslationsValid(translations []newsTranslationResponseItem) bool {
	for _, t := range translations {
		if _, found := findForeignCJK(t.TranslatedTitle); found {
			return false
		}
		if _, found := findLeakedEnglish(t.TranslatedTitle); found {
			return false
		}
	}
	return true
}
