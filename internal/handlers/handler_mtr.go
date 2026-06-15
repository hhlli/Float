package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"Float/internal/database"
)

// RequestAgentMTR 向探针下发 MTR 即时任务指令
func RequestAgentMTR(nodeID, target string) bool {
	// 直接读取同包下 handler_ws.go 中的 agentConns 和 agentConnsMu
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

	// 调用同包下 handler_tasks.go 中的 isValidTarget 函数
	if !isValidTarget(req.Target) {
		http.Error(w, "非法的目标格式", http.StatusBadRequest)
		return
	}

	success := RequestAgentMTR(req.NodeID, req.Target)
	if !success {
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
	err := database.DB.QueryRow("SELECT timestamp, result_json FROM mtr_results WHERE node_id = ? AND target = ?", nodeID, target).Scan(&timestamp, &resultJSON)

	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"pending"}`))
		return
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"success","timestamp":%d,"data":%s}`, timestamp, resultJSON)
}