package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"
)

// raw_data_cache는 날씨/환율/뉴스 원본 API 응답을 JSON 그대로 DB(Turso/
// libSQL, 로컬 개발 시에는 파일 기반 libSQL)에 저장한다 — 예전에는
// 프로세스 메모리(sync.RWMutex + map) TTL 캐시였는데,
// Render 같은 플랫폼에서 무료 티어 인스턴스가 잠들었다 깨어나면(또는 그냥
// 재배포되면) 메모리가 통째로 초기화되어 캐시도 함께 사라지고, 재시작
// 직후 사용자 요청들이 한꺼번에 외부 API를 다시 두들기는 문제가 있었다.
// DB에 저장해두면 프로세스가 재시작돼도 캐시가 그대로 남아있다.
type rawDataCacheRow struct {
	dataJSON  string
	fetchedAt time.Time
	expiresAt time.Time
}

func lookupRawDataCache(ctx context.Context, conn *sql.DB, key string) (rawDataCacheRow, bool) {
	if conn == nil {
		return rawDataCacheRow{}, false
	}
	var row rawDataCacheRow
	err := conn.QueryRowContext(ctx,
		`SELECT data_json, fetched_at, expires_at FROM raw_data_cache WHERE cache_key = ?`, key,
	).Scan(&row.dataJSON, &row.fetchedAt, &row.expiresAt)
	if err != nil {
		return rawDataCacheRow{}, false
	}
	return row, true
}

func upsertRawDataCache(ctx context.Context, conn *sql.DB, key, dataJSON string, fetchedAt, expiresAt time.Time) error {
	if conn == nil {
		return nil
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO raw_data_cache (cache_key, data_json, fetched_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET data_json = excluded.data_json, fetched_at = excluded.fetched_at, expires_at = excluded.expires_at`,
		key, dataJSON, fetchedAt, expiresAt,
	)
	return err
}

// isRawCacheFresh는 캐시 행이 아직(expires_at을 지나지 않아) 유효한지
// 판단하는 순수 함수다 — DB 접근이 없어 단위 테스트가 쉽다. 실제 DB 연동
// 동작(캐시 적중 시 외부 API 미호출, 만료/실패 시 재조회 또는 잠정치 폴백)
// 자체는 이 프로젝트의 다른 DB 캐시들(lotto_draws, briefing_section_cache
// 등)과 마찬가지로 실제 서버 재시작 + curl로 라이브 검증한다 — 단위
// 테스트만으로는 재현하기 어려운 "프로세스 재시작 후에도 캐시가 살아있다"는
// 속성이 이 기능의 핵심이기 때문이다.
func isRawCacheFresh(row rawDataCacheRow, now time.Time) bool {
	return now.Before(row.expiresAt)
}

// rawCacheUpsertTimeout은 캐시 저장(INSERT) 자체의 타임아웃이다.
// context.Background()에서 독립적으로 파생시킨다 — 호출자의 요청 스코프
// ctx를 그대로 쓰면, 느린 외부 API 호출(예: 환율의 Frankfurter "현재값" +
// "주간 추이" 2건 병렬 호출)이 요청 타임아웃 예산을 거의 다 써버린 뒤
// 성공하는 경우, 방금 받아온 데이터를 저장하려는 순간 컨텍스트가 이미
// 만료돼 있어 저장 자체가 실패한다(실제로 "DB 저장 실패: context deadline
// exceeded"로 라이브 환경에서 재현됨) — lotto.go의 lottoInsertTimeout과
// 정확히 같은 문제이자 같은 해법이다.
const rawCacheUpsertTimeout = 5 * time.Second

// fetchWithRawCache는 날씨가 쓰는 "DB 기반 원본 응답 캐시" 패턴을 구현한다.
// 뉴스와 환율은 이 함수를 재사용하지 않고 각자 별도로 구현되어 있다:
// 뉴스(news.go의 getCachedOrFetchNews)는 일일 크레딧 할당량에 근접했을 때
// 만료된 캐시라도 미리 서빙하는 세 번째 분기가 필요하고, 환율(exchange.go의
// getCachedOrFetchExchange)은 현재값과 7일 추이(weekly)를 서로 다른 TTL로
// 독립적으로 캐싱해야 해서(하나가 실패해도 다른 하나의 캐시를 30분씩
// 덮어쓰지 않도록) 캐시 항목이 두 개다.
//
//  1. 유효한(expires_at 이전) 캐시가 있으면 그대로 반환한다(외부 API 미호출).
//  2. 없거나 만료됐으면 fetchFn으로 실제 API를 호출한다.
//  3. 성공하면 결과를 JSON으로 직렬화해 raw_data_cache에 upsert하고
//     ttl만큼 뒤를 새 expires_at으로 저장한다.
//  4. 실패했는데 만료된 캐시라도 있으면, 완전히 실패 처리하는 대신 그
//     옛 데이터를 "잠정치"로 반환한다(로그에 남긴다) — 외부 API가 일시
//     장애를 겪어도 화면이 완전히 비지 않게 하기 위함이다.
func fetchWithRawCache[T any](ctx context.Context, conn *sql.DB, key string, ttl time.Duration, fetchFn func(context.Context) (*T, error)) (*T, error) {
	row, found := lookupRawDataCache(ctx, conn, key)
	now := time.Now()

	if found && isRawCacheFresh(row, now) {
		var data T
		if err := json.Unmarshal([]byte(row.dataJSON), &data); err == nil {
			log.Printf("[원본 캐시 재사용] %s: %s까지 유효 (외부 API 미호출)", key, row.expiresAt.Format("15:04:05"))
			return &data, nil
		}
		log.Printf("[원본 캐시] %s: 저장된 JSON 파싱 실패, 무시하고 새로 가져옵니다", key)
		found = false
	}

	fetched, err := fetchFn(ctx)
	if err != nil {
		if found {
			var stale T
			if jsonErr := json.Unmarshal([]byte(row.dataJSON), &stale); jsonErr == nil {
				log.Printf("[원본 캐시] %s: 외부 API 호출 실패(%v) — %s에 가져온 만료 캐시를 잠정치로 사용", key, err, row.fetchedAt.Format("2006-01-02 15:04:05"))
				return &stale, nil
			}
		}
		return nil, err
	}

	encoded, marshalErr := json.Marshal(fetched)
	if marshalErr != nil {
		log.Printf("[원본 캐시] %s: 직렬화 실패, 캐시 저장은 생략하고 방금 가져온 값을 그대로 반환합니다: %v", key, marshalErr)
		return fetched, nil
	}
	insertCtx, cancel := context.WithTimeout(context.Background(), rawCacheUpsertTimeout)
	defer cancel()
	if upsertErr := upsertRawDataCache(insertCtx, conn, key, string(encoded), now, now.Add(ttl)); upsertErr != nil {
		log.Printf("[원본 캐시] %s: DB 저장 실패: %v", key, upsertErr)
	}
	return fetched, nil
}
