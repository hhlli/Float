package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
    "regexp"
	"strings"

	"Float/internal/database"
	"Float/internal/logger"
	"go.uber.org/zap"
)

// 🌟 新增：校验目标是否为合法的 IP 或纯净的域名
func isValidTarget(target string) bool {
    // 允许字母、数字、中划线、点号和冒号（支持端口和IPv6简写形式）
    // 此正则仍能阻断空格及可能引起 Shell 命令逃逸的特殊符号
    matched, _ := regexp.MatchString(`^[a-zA-Z0-9.:-]+$`, target)
    return matched
}

// 🌟 新增：根据目标地址自动分类网络类型
func classifyNetwork(target string) string {
	targetLower := strings.ToLower(target)
	if strings.Contains(targetLower, "-ct-") || strings.Contains(targetLower, "telecom") || strings.Contains(targetLower, "chinanet") {
		return "电信"
	}
	if strings.Contains(targetLower, "-cu-") || strings.Contains(targetLower, "unicom") || strings.Contains(targetLower, "10010") {
		return "联通"
	}
	if strings.Contains(targetLower, "-cm-") || strings.Contains(targetLower, "mobile") || strings.Contains(targetLower, "10086") {
		return "移动"
	}
	return "其他"
}

// [API] 获取所有监测任务列表
func ApiAllTasksHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := database.DB.Query("SELECT id, name, type, excluded_nodes, target, interval, created_at FROM monitor_tasks ORDER BY created_at DESC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type TaskInfo struct {
		ID            int      `json:"id"`
		Name          string   `json:"name"`
		Type          string   `json:"type"`
		ExcludedNodes []string `json:"excluded_nodes"`
		Target        string   `json:"target"`
		Interval      int      `json:"interval"`
		CreatedAt     int64    `json:"created_at"`
	}

	var tasks []TaskInfo
	for rows.Next() {
		var t TaskInfo
		var excludedStr string
		rows.Scan(&t.ID, &t.Name, &t.Type, &excludedStr, &t.Target, &t.Interval, &t.CreatedAt)

		if excludedStr != "" {
			json.Unmarshal([]byte(excludedStr), &t.ExcludedNodes)
		}
		if t.ExcludedNodes == nil {
			t.ExcludedNodes = []string{}
		}

		tasks = append(tasks, t)
	}
	if tasks == nil {
		tasks = []TaskInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

// [API] 添加监测任务
func ApiAddTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type TaskReq struct {
		Name          string   `json:"name"`
		Type          string   `json:"type"`
		ExcludedNodes []string `json:"excluded_nodes"`
		Target        string   `json:"target"`
		Interval      int      `json:"interval"`
	}

	var req TaskReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}

	// 🌟 修复：检查 Target 格式防范命令注入
    if !isValidTarget(req.Target) {
        http.Error(w, "非法的目标格式，仅支持 IP 或域名", http.StatusBadRequest)
        return
    }

	excludedJSON, _ := json.Marshal(req.ExcludedNodes)

	networkType := classifyNetwork(req.Target) // 🌟 获取分类
	_, err := database.DB.Exec("INSERT INTO monitor_tasks (name, type, excluded_nodes, target, interval, network_type, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		req.Name, req.Type, string(excludedJSON), req.Target, req.Interval, networkType, time.Now().Unix()) // 🌟 更新 SQL 和参数

		if err != nil {
			logger.Log.Error("添加监测任务失败",
				zap.String("module", "DB"),
				zap.Error(err),
			)
			http.Error(w, "Database write error", http.StatusInternalServerError)
			return
		}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"success"}`))
}

// 🌟 修复：增加了 DELETE 方法限制
// [API] 删除监测任务
func ApiDeleteTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idStr := r.URL.Query().Get("id")
	id, _ := strconv.Atoi(idStr)
	database.DB.Exec("DELETE FROM monitor_tasks WHERE id = ?", id)
	database.DB.Exec("DELETE FROM task_results WHERE task_id = ?", id)
	w.WriteHeader(http.StatusOK)
}

// 🌟 修复：重构了获取分配任务的逻辑，修复了查无此列的崩溃，并增加了节点鉴权
// 探针拉取分配给自己的任务 (GET /api/tasks/pull?node_id=xxx&token=xxx)
func ApiPullTasksHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	token := r.URL.Query().Get("token")
	if nodeID == "" || token == "" {
		http.Error(w, "Missing node_id or token", http.StatusBadRequest)
		return
	}

	// 鉴权
	var dbToken string
	err := database.DB.QueryRow("SELECT auth_token FROM servers WHERE node_id = ?", nodeID).Scan(&dbToken)
	if err != nil || dbToken != token {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 读取所有任务，前端筛选排除名单
	rows, err := database.DB.Query("SELECT id, type, target, interval, excluded_nodes FROM monitor_tasks")
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type ProbeTask struct {
		ID       int    `json:"id"`
		Type     string `json:"type"`
		Target   string `json:"target"`
		Interval int    `json:"interval"`
	}

	var tasks []ProbeTask
	for rows.Next() {
		var t ProbeTask
		var excludedStr string
		rows.Scan(&t.ID, &t.Type, &t.Target, &t.Interval, &excludedStr)
		
		isExcluded := false
		if excludedStr != "" {
			var excludedNodes []string
			json.Unmarshal([]byte(excludedStr), &excludedNodes)
			for _, ex := range excludedNodes {
				if ex == nodeID {
					isExcluded = true
					break
				}
			}
		}

		if !isExcluded {
			tasks = append(tasks, t)
		}
	}
	if tasks == nil {
		tasks = []ProbeTask{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

// 🌟 修复：修复了 SQL 字段名错误 (latency_ms -> ping_ms)，缺少 node_id，并增加鉴权
// 探针上报测速结果 (POST /api/tasks/push)
func ApiTaskResultHandler(w http.ResponseWriter, r *http.Request) {
	agentToken := r.Header.Get("X-Agent-Token")
	if agentToken == "" {
		agentToken = r.URL.Query().Get("token")
	}

	type TaskResult struct {
		TaskID int     `json:"task_id"`
		PingMs float64 `json:"ping_ms"` 
		Loss   float64 `json:"loss"`
		Jitter float64 `json:"jitter"`
		P50    float64 `json:"p50"`
		P99    float64 `json:"p99"`
		MinMs  float64 `json:"min_ms"`
		MaxMs  float64 `json:"max_ms"`
		Status string  `json:"status"`
	}

	var payload struct {
		NodeID  string       `json:"node_id"`
		Results []TaskResult `json:"results"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 鉴权
	var dbToken string
	err := database.DB.QueryRow("SELECT auth_token FROM servers WHERE node_id = ?", payload.NodeID).Scan(&dbToken)
	if err != nil || dbToken != agentToken {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	now := time.Now().Unix()
	tx, _ := database.DB.Begin()
	// 👇 更新 INSERT 语句
	stmt, _ := tx.Prepare(`
	INSERT INTO task_results (
		task_id, node_id, ping_ms, status, timestamp, loss, jitter, extra_data
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	defer stmt.Close()

	for _, res := range payload.Results {
		extraMap := map[string]float64{
			"p50":    res.P50,
			"p99":    res.P99,
			"min_ms": res.MinMs,
			"max_ms": res.MaxMs,
		}
		extraBytes, _ := json.Marshal(extraMap)
        
		// 👇 更新 stmt.Exec 参数，追加 res.Loss 和 res.Jitter
		stmt.Exec(res.TaskID, payload.NodeID, res.PingMs, res.Status, now, res.Loss, res.Jitter, string(extraBytes))
	}
	tx.Commit()

	w.WriteHeader(http.StatusOK)
}

// [API] 编辑监测任务
func ApiEditTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID            int      `json:"id"`
		Name          string   `json:"name"`
		Type          string   `json:"type"`
		Target        string   `json:"target"`
		ExcludedNodes []string `json:"excluded_nodes"`
		Interval      int      `json:"interval"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 || req.Name == "" || req.Target == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}
	
	// 🌟 修复：检查 Target 格式防范命令注入
    if !isValidTarget(req.Target) {
        http.Error(w, "非法的目标格式，仅支持 IP 或域名", http.StatusBadRequest)
        return
    }

	excludedJSON, _ := json.Marshal(req.ExcludedNodes)

	networkType := classifyNetwork(req.Target) // 🌟 获取分类
	query := `UPDATE monitor_tasks SET name=?, type=?, target=?, excluded_nodes=?, interval=?, network_type=? WHERE id=?`
	_, err := database.DB.Exec(query, req.Name, req.Type, req.Target, string(excludedJSON), req.Interval, networkType, req.ID) // 🌟 更新 SQL 和参数
	if err != nil {
		logger.Log.Error("Update task error",
			zap.String("module", "DB"),
			zap.Error(err),
		)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}