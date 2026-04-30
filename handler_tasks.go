package main

import (
	"encoding/json"
	"net/http"
	"time"
    "strconv"
	"log"
)

// 获取所有监测任务列表
// [API] 获取所有监测任务列表 (替换原有的 apiTasksListHandler)
func apiAllTasksHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name, type, excluded_nodes, target, interval, created_at FROM monitor_tasks ORDER BY created_at DESC")
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

// 添加监测任务
// [API] 添加监测任务
func apiAddTaskHandler(w http.ResponseWriter, r *http.Request) {
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

	excludedJSON, _ := json.Marshal(req.ExcludedNodes)

	_, err := db.Exec("INSERT INTO monitor_tasks (name, type, excluded_nodes, target, interval, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		req.Name, req.Type, string(excludedJSON), req.Target, req.Interval, time.Now().Unix())

	if err != nil {
		log.Println("添加监测任务失败:", err)
		http.Error(w, "Database write error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"success"}`))
}

// 删除监测任务
func apiDeleteTaskHandler(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
    id, _ := strconv.Atoi(idStr)
	db.Exec("DELETE FROM monitor_tasks WHERE id = ?", id)
	db.Exec("DELETE FROM task_results WHERE task_id = ?", id)
	w.WriteHeader(http.StatusOK)
}
// 探针拉取分配给自己的任务 (GET /api/tasks/pull?node_id=xxx)
func apiPullTasksHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	rows, err := db.Query("SELECT id, type, target, interval FROM monitor_tasks WHERE node_id = ?", nodeID)
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
		rows.Scan(&t.ID, &t.Type, &t.Target, &t.Interval)
		tasks = append(tasks, t)
	}
	if tasks == nil {
		tasks = []ProbeTask{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

// 探针上报测速结果 (POST /api/tasks/push)
func apiTaskResultHandler(w http.ResponseWriter, r *http.Request) {
	type TaskResult struct {
		TaskID    int     `json:"task_id"`
		LatencyMs float64 `json:"latency_ms"`
		Status    string  `json:"status"` // "online" 或 "offline"
	}

	var payload struct {
		NodeID  string       `json:"node_id"`
		Results []TaskResult `json:"results"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	now := time.Now().Unix()
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare("INSERT INTO task_results (task_id, latency_ms, status, timestamp) VALUES (?, ?, ?, ?)")
	defer stmt.Close()

	for _, res := range payload.Results {
		stmt.Exec(res.TaskID, res.LatencyMs, res.Status, now)
	}
	tx.Commit()

	w.WriteHeader(http.StatusOK)
}
// [API] 编辑监测任务
// [API] 编辑监测任务
func apiEditTaskHandler(w http.ResponseWriter, r *http.Request) {
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

	excludedJSON, _ := json.Marshal(req.ExcludedNodes)

	query := `UPDATE monitor_tasks SET name=?, type=?, target=?, excluded_nodes=?, interval=? WHERE id=?`
	_, err := db.Exec(query, req.Name, req.Type, req.Target, string(excludedJSON), req.Interval, req.ID)
	if err != nil {
		log.Println("Update task error:", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}