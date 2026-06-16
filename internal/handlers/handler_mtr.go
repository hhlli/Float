package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"Float/internal/database"
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
	_, _ = database.DB.Exec("INSERT OR REPLACE INTO mtr_results (node_id, target, timestamp, result_json) VALUES (?, ?, ?, ?)", req.NodeID, req.Target, time.Now().Unix(), pendingJSON)

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
	err := database.DB.QueryRow("SELECT timestamp, result_json FROM mtr_results WHERE node_id = ? AND target = ?", nodeID, target).Scan(&timestamp, &resultJSON)

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