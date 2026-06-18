package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"Float/internal/database"
	"Float/internal/logger"
	"go.uber.org/zap"
)

// RequestAgentMTR 向探针下发 MTR 即时任务指令
func RequestAgentMTR(nodeID, target string) bool {
	agentConnsMu.RLock()
	ac, ok := agentConns[nodeID]
	agentConnsMu.RUnlock()

	if !ok {
		return false
	}

	msg := RPCRequest{
		JSONRPC: "2.0",
		Method:  "mtr.request",
		Params:  mustMarshal(map[string]string{"target": target}),
	}

	ac.mu.Lock()
	defer ac.mu.Unlock()
	if err := ac.conn.WriteJSON(msg); err != nil {
		return false
	}
	return true
}

// ApiRunMTRHandler [API] 前端手动触发 MTR 诊断 (异步下发)
func ApiRunMTRHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		NodeID string `json:"node_id"`
		Target string `json:"target"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" || req.Target == "" {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}

	if !isValidTarget(req.Target) {
		http.Error(w, "非法的目标格式", http.StatusBadRequest)
		return
	}

	// 写入 pending 状态与当前时间戳，作为服务端判定超时的基准
	pendingJSON := `{"status":"pending"}`
	_, errInsert := database.DB.Exec("INSERT OR REPLACE INTO mtr_results (node_id, target, timestamp, result_json) VALUES (?, ?, ?, ?)", req.NodeID, req.Target, time.Now().Unix(), pendingJSON)
	if errInsert != nil {
		logger.Log.Error("写入 MTR pending 行失败", zap.String("module", "DB"), zap.Error(errInsert))
		http.Error(w, "Database write error", http.StatusInternalServerError)
		return
	}

	success := RequestAgentMTR(req.NodeID, req.Target)
	if !success {
		// 下发失败，清理占位数据
		_, _ = database.DB.Exec("DELETE FROM mtr_results WHERE node_id = ? AND target = ?", req.NodeID, req.Target)
		http.Error(w, "目标探针离线或未建立 WebSocket 连接", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"dispatched"}`))
}

// ApiGetMTRResultHandler [API] 前端轮询获取 MTR 结果
func ApiGetMTRResultHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	target := r.URL.Query().Get("target")

	if nodeID == "" || target == "" {
		http.Error(w, "Missing params", http.StatusBadRequest)
		return
	}

	var timestamp int64
	var resultJSON string
	err := database.DB.QueryRow("SELECT timestamp, result_json FROM mtr_results WHERE node_id = ? AND target = ? ORDER BY timestamp DESC LIMIT 1", nodeID, target).Scan(&timestamp, &resultJSON)

	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"pending"}`))
		return
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// 服务端任务生命周期断言机制 (60 秒防死锁)
	if resultJSON == `{"status":"pending"}` {
		if time.Now().Unix()-timestamp > 60 {
			errJSON := fmt.Sprintf(`{"status":"success","timestamp":%d,"data":{"target":"%s","error":"服务端判定任务超时 (超过 60 秒未收到探针回传数据，探针网络可能已中断)"}}`, timestamp, target)
			
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(errJSON))
			
			database.DB.Exec("DELETE FROM mtr_results WHERE node_id = ? AND target = ?", nodeID, target)
			return
		}
		
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"pending"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"success","timestamp":%d,"data":%s}`, timestamp, resultJSON)
}

// ApiReportMTRHandler [API] 接收探针端通过 HTTP POST 回传的 MTR 结果
func ApiReportMTRHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		NodeID    string          `json:"node_id"`
		Target    string          `json:"target"`
		Timestamp int64           `json:"timestamp"`
		Result    json.RawMessage `json:"result"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 探针鉴权
	authHeader := r.Header.Get("Authorization")
	token := ""
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		token = authHeader[7:]
	}

	var isValid bool
	errDb := database.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM servers WHERE node_id = ? AND auth_token = ?)", req.NodeID, token).Scan(&isValid)
	if errDb != nil || !isValid {
		http.Error(w, "Unauthorized: Invalid token or node", http.StatusUnauthorized)
		return
	}

	// 更新 MTR 结果到数据库，覆盖 pending 状态
	resultStr := string(req.Result)
	res, err := database.DB.Exec(
		"UPDATE mtr_results SET result_json = ?, timestamp = ? WHERE node_id = ? AND target = ?",
		resultStr, req.Timestamp, req.NodeID, req.Target,
	)

	if err != nil {
		logger.Log.Error("MTR Report 落库更新失败", zap.Error(err))
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		logger.Log.Warn("MTR Report 未匹配到 pending 记录", zap.String("node_id", req.NodeID), zap.String("target", req.Target))
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"success"}`))
}