package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
)

// itTermGlossary는 뉴스 헤드라인 번역(이 파일)과 AI 브리핑의 뉴스 섹션
// (briefing.go)이 공유한다. "frontier model" 같은 용어가 등장할 때마다
// 동일하게 번역되도록 하기 위해서다 — 두 곳에서 각자 관리하는 별도의
// 용어 목록이 서로 어긋나는 일을 막는다.
// 각 줄에 들여쓰기와 화살표 기호를 넣은 불릿 목록 대신 쉼표로 이어진 한 줄
// 인라인 목록으로 압축했다 — 같은 용어 매핑 정보를 담으면서도 서식 문자에
// 드는 토큰을 줄인다(뉴스 브리핑/번역 프롬프트가 공유하는 부분이라, 여기서
// 아낀 토큰은 두 호출 경로 모두에 그대로 반영된다).
const itTermGlossary = `IT/AI 전문 용어는 자연스러운 한국어 관용 표현으로 번역하되, 업계에서 이미 굳어진 표현이 있으면 그대로 사용하세요. 자주 쓰는 용어: frontier model=최상위 모델, fine-tune=파인튜닝, RL=강화학습, open model/open source=오픈소스 모델, open-weights=오픈 웨이트, benchmark=벤치마크, inference=추론, training=학습, dataset=데이터셋, parameter=파라미터, prompt=프롬프트, agent=에이전트, context window=컨텍스트 윈도우, token=토큰, latency=지연 시간.`

const newsTranslationSystemPrompt = `당신은 기술/IT 뉴스 헤드라인을 한국어로 번역하는 전문 번역가입니다.
1. 원문의 의미를 정확히 유지하면서 자연스러운 한국어 뉴스 제목체로 번역하세요 (예: "~ 발표", "~ 공개", "~ 논란" 같은 뉴스 제목 어투).
2. 고유명사(회사명, 프로젝트명, 인명)는 번역하지 말고 원문 그대로 유지하세요.
3. 원문에 있던 숫자+단위(K/M/B) 표현은 이미 정확한 한국어 환산값으로 전부 바뀌어서 전달됩니다 (예: 원문의 "9B"는 "90억"으로, "$6.6B"는 "66억 달러"로 이미 바뀌어 있음). 번역문에는 그 한국어 값을 그대로 사용하세요. "M"이나 "B" 같은 원래 단위 알파벳은 데이터에 남아있지 않으니, 있지도 않은 단위를 상상해서 다시 계산하거나 다른 숫자로 바꾸지 마세요. 만약 이렇게 이미 변환되어 있지 않은 숫자+단위가 있다면, K=천, M=백만, B=십억(10억) 기준으로 정확히 계산해서 변환하세요.
4. ` + itTermGlossary + `
5. 영어 원문의 어순을 그대로 따라가지 말고, 한국어 뉴스 제목으로 자연스럽게 재구성하세요. 예: "A $500 RL fine-tune of a 9B open model beat frontier models on catalog review" → "90억 파라미터 오픈소스 모델, 500달러 강화학습 튜닝만으로 카탈로그 리뷰 평가에서 최상위 모델 제쳤다" 같은 자연스러운 기사 제목 형태로 재구성하세요.
6. 번역을 완료한 후, 원문에 등장한 모든 숫자(단위 포함)가 번역문에도 정확히 반영되었는지 다시 한번 확인하세요.
7. 반드시 요청받은 개수만큼, 요청 순서와 id를 그대로 유지해서 JSON 배열로만 응답하세요. 다른 설명은 절대 추가하지 마세요.`

// newsTranslationCache는 NewsData.io의 article_id를 그 한국어 제목에
// 매핑한다. 로또 데이터와 달리 뉴스 헤드라인은 재시작 후에도 유지될 필요가
// 없다 — 몇 시간 안에 상위 기사들이 바뀌기 때문에, 인메모리 map만으로
// 충분하다 (MySQL 테이블 불필요).
var newsTranslationCache = struct {
	mu    sync.Mutex
	items map[string]string
}{items: make(map[string]string)}

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

	newsTranslationCache.mu.Lock()
	for i := range items {
		if cached, ok := newsTranslationCache.items[items[i].ID]; ok {
			items[i].TranslatedTitle = cached
			cacheHits++
		} else {
			toTranslate = append(toTranslate, items[i])
		}
	}
	newsTranslationCache.mu.Unlock()

	if cacheHits > 0 {
		for i := 0; i < cacheHits; i++ {
			recordGroqCacheHit()
		}
		log.Printf("[캐시 재사용] 뉴스 번역: %d개 항목 캐시 재사용, %d개 신규 번역 필요", cacheHits, len(toTranslate))
	}

	if len(toTranslate) == 0 {
		return
	}

	translated, err := fetchNewsTranslation(ctx, toTranslate)
	if err != nil {
		log.Printf("뉴스: 번역 실패: %v", err)
		return
	}

	byID := make(map[string]string, len(translated))
	for _, t := range translated {
		byID[t.ID] = t.TranslatedTitle
	}

	newsTranslationCache.mu.Lock()
	for id, title := range byID {
		newsTranslationCache.items[id] = title
	}
	newsTranslationCache.mu.Unlock()

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
		// maxTokens=700은 헤드라인 최대 5개를 배치 번역할 때의 JSON 배열
		// 출력(각 항목의 번역된 제목 하나씩)에 넉넉한 여유이면서도, 모델이
		// 반복 생성 루프에 빠졌을 때 무한정 토큰을 소비하지 않도록 막는다.
		content, callErr := callGroqChat(ctx, apiKey, model, []groqChatMessage{
			{Role: "system", Content: newsTranslationSystemPrompt},
			{Role: "user", Content: userContent},
		}, 0.2, 700, true)
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
