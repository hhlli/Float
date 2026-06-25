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

// RequestAgentPlugin 向探针下发通用执行指令
func RequestAgentPlugin(taskID int64, nodeID, extID string, args []string) bool {
	agentConnsMu.RLock()
	ac, ok := agentConns[nodeID]
	agentConnsMu.RUnlock()

	if !ok {
		return false
	}

	msg := RPCRequest{
		JSONRPC: "2.0",
		Method:  "plugin.execute",
		Params: mustMarshal(map[string]interface{}{
			"ext_id": extID,
			"args":   args,
		}),
		ID: taskID,
	}

	ac.mu.Lock()
	defer ac.mu.Unlock()
	if err := ac.conn.WriteJSON(msg); err != nil {
		return false
	}
	return true
}

// ApiRunPluginHandler [API] 前端手动触发插件 (异步下发)
func ApiRunPluginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		NodeID string   `json:"node_id"`
		ExtID  string   `json:"ext_id"`
		Args   []string `json:"args"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" || req.ExtID == "" {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}

	timestamp := time.Now().Unix()
	
	// 记录初始参数以便前端展示
	pendingMap := map[string]interface{}{
		"status": "pending",
		"args":   req.Args,
	}
	pendingBytes, _ := json.Marshal(pendingMap)

	_, errInsert := database.DB.Exec(
		"INSERT OR REPLACE INTO plugin_results (node_id, ext_id, task_id, timestamp, status, result_json) VALUES (?, ?, ?, ?, 'pending', ?)",
		req.NodeID, req.ExtID, timestamp, timestamp, string(pendingBytes),
	)
	if errInsert != nil {
		logger.Log.Error("写入 Plugin pending 行失败", zap.Error(errInsert))
		http.Error(w, "Database write error", http.StatusInternalServerError)
		return
	}

	success := RequestAgentPlugin(timestamp, req.NodeID, req.ExtID, req.Args)
	if !success {
		database.DB.Exec("DELETE FROM plugin_results WHERE task_id = ?", timestamp)
		http.Error(w, "目标探针离线或未建立 WebSocket 连接", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"status":"dispatched","task_id":%d}`, timestamp)))
}

// ApiGetPluginResultHandler [API] 前端轮询获取插件执行结果
func ApiGetPluginResultHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	extID := r.URL.Query().Get("ext_id")
	taskID := r.URL.Query().Get("task_id")

	if nodeID == "" || extID == "" || taskID == "" {
		http.Error(w, "Missing params", http.StatusBadRequest)
		return
	}

	var timestamp int64
	var status, resultJSON string
	err := database.DB.QueryRow(
		"SELECT timestamp, status, result_json FROM plugin_results WHERE node_id = ? AND ext_id = ? AND task_id = ?",
		nodeID, extID, taskID,
	).Scan(&timestamp, &status, &resultJSON)

	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"pending"}`))
		return
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// 超时阻断机制
	if status == "pending" {
		if time.Now().Unix()-timestamp > 60 {
			errJSON := fmt.Sprintf(`{"status":"success","timestamp":%d,"data":{"error":"服务端判定任务超时，探针未回传结果"}}`, timestamp)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(errJSON))
			database.DB.Exec("DELETE FROM plugin_results WHERE task_id = ?", taskID)
			return
		}
		
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"pending"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"success","timestamp":%d,"data":%s}`, timestamp, resultJSON)
}

// ApiGetPluginHistoryHandler [API] 获取特定插件历史记录
func ApiGetPluginHistoryHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	extID := r.URL.Query().Get("ext_id")
	if extID == "" {
		http.Error(w, "Missing ext_id", http.StatusBadRequest)
		return
	}

	limit := 50
	var rows *sql.Rows
	var err error

	if nodeID != "" {
		rows, err = database.DB.Query(
			"SELECT id, node_id, task_id, timestamp, result_json FROM plugin_history WHERE node_id = ? AND ext_id = ? ORDER BY timestamp DESC LIMIT ?", 
			nodeID, extID, limit,
		)
	} else {
		rows, err = database.DB.Query(
			"SELECT id, node_id, task_id, timestamp, result_json FROM plugin_history WHERE ext_id = ? ORDER BY timestamp DESC LIMIT ?", 
			extID, limit,
		)
	}

	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var records []map[string]interface{}
	for rows.Next() {
		var id int
		var nID, resultJSON string
		var taskID, timestamp int64

		if err := rows.Scan(&id, &nID, &taskID, &timestamp, &resultJSON); err == nil {
			var rawData json.RawMessage
			json.Unmarshal([]byte(resultJSON), &rawData)

			records = append(records, map[string]interface{}{
				"id":          id,
				"node_id":     nID,
				"task_id":     taskID,
				"timestamp":   timestamp,
				"result_data": rawData,
			})
		}
	}

	if records == nil {
		records = make([]map[string]interface{}, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}