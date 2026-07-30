package main

import (
	"encoding/json"
	"net/http"
)

// lottoCollectionStartHandler는 POST /api/lotto/collection/start를 서빙한다.
// 이미 실행 중이면 아무 것도 하지 않고 started:false를 반환한다(중복
// 클릭에 안전).
func lottoCollectionStartHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST 요청만 허용됩니다"})
		return
	}
	if db == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "데이터베이스에 연결할 수 없습니다"})
		return
	}

	started := lottoStartCollection(db)
	json.NewEncoder(w).Encode(map[string]bool{"started": started, "running": true})
}

// lottoCollectionStopHandler는 POST /api/lotto/collection/stop을 서빙한다.
// 실행 중이 아니었으면 아무 것도 하지 않고 stopped:false를 반환한다.
func lottoCollectionStopHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST 요청만 허용됩니다"})
		return
	}

	stopped := lottoStopCollection()
	json.NewEncoder(w).Encode(map[string]bool{"stopped": stopped, "running": false})
}

// lottoCollectionStatusHandler는 GET /api/lotto/collection/status를
// 서빙한다 — 실행 중 여부, 마지막 수집 시각, 현재까지 저장된 회차 수를
// 반환한다. 프론트엔드가 토글이 켜져 있는 동안 이를 주기적으로 폴링해서
// "42/50 회차 수집됨" 진행 상황을 보여준다.
func lottoCollectionStatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status, err := lottoCollectionStatusSnapshot(r.Context(), db)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(status)
}
