package handlers

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"strings"
	"sort"

	"Float/internal/core"     // 引入 core 包
	"Float/internal/database" // 引入 database 包
	"Float/internal/logger"
	"go.uber.org/zap"
)

// 处理探针自动发现注册请求
func ApiRegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		DiscoveryToken string `json:"discovery_token"`
		Hostname       string `json:"hostname"`
		PublicIP       string `json:"public_ip"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	globalToken := database.GetAutoDiscoveryToken()
	if globalToken == "" || req.DiscoveryToken != globalToken {
		http.Error(w, "Invalid discovery token or auto-discovery disabled", http.StatusForbidden)
		return
	}

	// 生成标准 UUID v4 作为唯一 node_id
    u := make([]byte, 16)
    _, err1 := cryptoRand.Read(u)
    if err1 != nil {
        http.Error(w, "Internal server error", http.StatusInternalServerError)
        return
    }
    // 设置 UUID v4 的规范位：版本位（Version 4）与变体位（Variant 10xx）
    u[6] = (u[6] & 0x0f) | 0x40
    u[8] = (u[8] & 0x3f) | 0x80
    
    // 格式化为标准 36 位 UUID 格式 (8-4-4-4-12)
    newNodeID := fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])

	// 2. 生成该节点专属的 auth_token (用于后续 WebSocket 握手)
	tBytes := make([]byte, 16)
	_, err2 := cryptoRand.Read(tBytes)
	if err2 != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	nodeAuthToken := hex.EncodeToString(tBytes)

	now := time.Now().Unix()

	// 3. 准备数据库插入语句 (显式追加 ipv4 字段)
	query := `
		INSERT INTO servers (
			node_id, name, region, ipv4, cost, currency, billing_date, monthly_bw, bw_reset_day, notes, 
			last_active, created_at, status, cpu, mem, mem_used, mem_total, disk, disk_used, disk_total,
			auth_token
		) VALUES (
			?, ?, 'UN', ?, 0, 'CNY', '', 0, 1, '', 
			?, ?, 'online', 0, 0, 0, 0, 0, 0, 0,
			?
		)
	`

	// 4. 执行数据库插入 (将 req.PublicIP 传入第 4 个占位符)
	_, errExec := database.DB.Exec(query, newNodeID, req.Hostname, req.PublicIP, now, now, nodeAuthToken)
	if errExec != nil {
		logger.Log.Error("自动发现注册节点失败", 
			zap.String("module", "Registration"), 
			zap.Error(errExec),
		)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

    // 5. 触发地理位置解析
    clientIP := strings.TrimSpace(req.PublicIP)
    if clientIP == "" {
        clientIP = core.GetClientIP(r) 
    }
    go core.FetchAndSaveGeoIP(newNodeID, clientIP)

	// 5. 返回持久化所需信息给探针
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"node_id":      newNodeID,
		"server_token": nodeAuthToken,
	})
}

// 处理探针发来的指标数据
func ApiReceiveHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)


	var payload struct {
		NodeID string `json:"node_id"`
		PublicIP string `json:"public_ip"` // 🌟 新增：允许上报时携带公网 IP
		Data   struct {
			Timestamp  int64   `json:"timestamp"`
			OS         string  `json:"os"`
			Uptime     int64   `json:"uptime"`
			CPU        float64 `json:"cpu"`
			Mem        float64 `json:"mem"`
			MemUsed    float64 `json:"mem_used"`
			MemTotal   float64 `json:"mem_total"`
			Disk       float64 `json:"disk"`
			DiskUsed   float64 `json:"disk_used"`
			DiskTotal  float64 `json:"disk_total"`
			NetRxSpeed float64 `json:"net_rx_speed"`
			NetTxSpeed float64 `json:"net_tx_speed"`
			NetRxTotal float64 `json:"net_rx_total"`
			NetTxTotal float64 `json:"net_tx_total"`
			SwapUsed   float64 `json:"swap_used"`
			SwapTotal  float64 `json:"swap_total"`
			TCPConn    int     `json:"tcp_conn"`
			UDPConn    int     `json:"udp_conn"`
			Kernel     string  `json:"kernel"`
			Arch       string  `json:"arch"`
			Virt       string  `json:"virt"`
			CPUModel   string  `json:"cpu_model"`
			Processes  int     `json:"processes"`
			Load1      float64 `json:"load_1"`
			Load5      float64 `json:"load_5"`
			Load15     float64 `json:"load_15"`
		} `json:"data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload or payload too large", http.StatusBadRequest)
		return
	}

	// 🌟 新增：强制校验 Token 与 NodeID 是否匹配，且节点是否存活
	authHeader := r.Header.Get("Authorization")
	token := ""
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		token = authHeader[7:]
	}
	var isValid bool
	errDb := database.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM servers WHERE node_id = ? AND auth_token = ?)", payload.NodeID, token).Scan(&isValid)
	if errDb != nil || !isValid {
		http.Error(w, "Unauthorized: Node deleted or invalid token", http.StatusUnauthorized)
		return
	}
	
	// go core.FetchAndSaveGeoIP(payload.NodeID, clientIP)

	now := time.Now().Unix()

	// 变更为纯 UPDATE 逻辑
	query := `
		UPDATE servers SET 
			last_active=?, cpu=?, mem=?, mem_used=?, mem_total=?, 
			disk=?, disk_used=?, disk_total=?, os=?, uptime=?, 
			net_rx_speed=?, net_tx_speed=?, net_rx_total=?, net_tx_total=?, 
			swap_used=?, swap_total=?, tcp_conn=?, udp_conn=?, 
			kernel=?, arch=?, virt=?, cpu_model=?, processes=?, 
			load_1=?, load_5=?, load_15=?, status='online'
		WHERE node_id=?;
	`

	res, err := database.DB.Exec(query,
		now, payload.Data.CPU, payload.Data.Mem, payload.Data.MemUsed, payload.Data.MemTotal,
		payload.Data.Disk, payload.Data.DiskUsed, payload.Data.DiskTotal,
		payload.Data.OS, payload.Data.Uptime, payload.Data.NetRxSpeed, payload.Data.NetTxSpeed, payload.Data.NetRxTotal, payload.Data.NetTxTotal,
		payload.Data.SwapUsed, payload.Data.SwapTotal, payload.Data.TCPConn, payload.Data.UDPConn,
		payload.Data.Kernel, payload.Data.Arch, payload.Data.Virt, payload.Data.CPUModel,
		payload.Data.Processes, payload.Data.Load1, payload.Data.Load5, payload.Data.Load15,
		payload.NodeID,
	)
	if err != nil {
		logger.Log.Error("更新节点指标失败", 
			zap.String("module", "DB"), 
			zap.Error(err),
		)
		http.Error(w, "Database write error", http.StatusInternalServerError)
		return
	}

	// 检查受影响行数，若为 0 说明该 node_id 未在系统中注册
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		http.Error(w, "Unauthorized node ID", http.StatusUnauthorized)
		return
	}

	_, err = database.DB.Exec(`
		INSERT INTO metrics (
			node_id, timestamp, cpu_usage, mem_usage, disk_usage,
			net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
			tcp_conn, udp_conn, processes, load_1, load_5, load_15,
			swap_used, swap_total
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		payload.NodeID, now,
		payload.Data.CPU, payload.Data.Mem, payload.Data.Disk,
		payload.Data.NetRxSpeed, payload.Data.NetTxSpeed, payload.Data.NetRxTotal, payload.Data.NetTxTotal,
		payload.Data.TCPConn, payload.Data.UDPConn, payload.Data.Processes,
		payload.Data.Load1, payload.Data.Load5, payload.Data.Load15,
		payload.Data.SwapUsed, payload.Data.SwapTotal,
	)
	if err != nil {
		logger.Log.Error("写入 metrics 历史失败", 
			zap.String("module", "DB"), 
			zap.Error(err),
		)
	}

	// === 提取实时差量数据并广播给前端面板 ===
	realtimeDiff := map[string]interface{}{
		"node_id":      payload.NodeID,
		"status":       "online",
		"last_active":  now,
		"cpu":          payload.Data.CPU,
		"mem":          payload.Data.Mem,
		"mem_used":     payload.Data.MemUsed,
		"mem_total":    payload.Data.MemTotal,
		"disk":         payload.Data.Disk,
		"disk_used":    payload.Data.DiskUsed,
		"disk_total":   payload.Data.DiskTotal,
		"uptime":       payload.Data.Uptime,
		"net_rx_speed": payload.Data.NetRxSpeed,
		"net_tx_speed": payload.Data.NetTxSpeed,
		"net_rx_total": payload.Data.NetRxTotal,
		"net_tx_total": payload.Data.NetTxTotal,
		"swap_used":    payload.Data.SwapUsed,
		"swap_total":   payload.Data.SwapTotal,
		"tcp_conn":     payload.Data.TCPConn,
		"udp_conn":     payload.Data.UDPConn,
		"processes":    payload.Data.Processes,
		"load_1":       payload.Data.Load1,
		"load_5":       payload.Data.Load5,
		"load_15":      payload.Data.Load15,
	}
	BroadcastRealtimeData(realtimeDiff)
	// ==========================================

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

// 获取节点历史指标（供图表使用，带降采样）
func ApiDataHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	rangeStr := r.URL.Query().Get("range")
	var bucketSeconds int64 = 5
	var rangeSeconds int64 = 300

	switch rangeStr {
	case "realtime":
		bucketSeconds = 5
		rangeSeconds = 300
	case "1h":
		bucketSeconds = 60
		rangeSeconds = 3600
	case "6h":
		bucketSeconds = 600
		rangeSeconds = 6 * 3600
	case "12h":
		bucketSeconds = 600
		rangeSeconds = 12 * 3600
	case "1d", "24h":
		bucketSeconds = 1200
		rangeSeconds = 86400
	case "7d":
		bucketSeconds = 3600
		rangeSeconds = 7 * 86400
	case "30d", "720h":
		bucketSeconds = 7200
		rangeSeconds = 30 * 86400
	default:
		if v, err := strconv.ParseInt(rangeStr, 10, 64); err == nil && v > 0 {
			rangeSeconds = v
		}
	}

	var since int64
	afterStr := r.URL.Query().Get("after")
	if afterStr != "" {
		if a, err := strconv.ParseInt(afterStr, 10, 64); err == nil && a > 0 {
			since = a
		}
	}
	if since == 0 {
		since = time.Now().Unix() - rangeSeconds
	}

	query := `
		SELECT 
			(timestamp / ?) * ? AS bucket_time,
			AVG(cpu_usage) AS cpu,
			AVG(mem_usage) AS mem,
			AVG(disk_usage) AS disk,
			AVG(net_rx_speed) AS net_rx_speed,
			AVG(net_tx_speed) AS net_tx_speed,
			MAX(net_rx_total) AS net_rx_total,
			MAX(net_tx_total) AS net_tx_total,
			AVG(tcp_conn) AS tcp_conn,
			AVG(udp_conn) AS udp_conn,
			AVG(processes) AS processes,
			AVG(load_1) AS load_1,
			AVG(load_5) AS load_5,
			AVG(load_15) AS load_15,
			AVG(swap_used) AS swap_used,
			AVG(swap_total) AS swap_total
		FROM metrics
		WHERE node_id = ? AND timestamp >= ?
		GROUP BY bucket_time
		ORDER BY bucket_time ASC`

	rows, err := database.DB.Query(query, bucketSeconds, bucketSeconds, nodeID, since)
	if err != nil {
		http.Error(w, "Database query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type MetricRow struct {
		Timestamp  int64   `json:"timestamp"`
		CPU        float64 `json:"cpu"`
		Mem        float64 `json:"mem"`
		Disk       float64 `json:"disk"`
		NetRxSpeed float64 `json:"net_rx_speed"`
		NetTxSpeed float64 `json:"net_tx_speed"`
		NetRxTotal float64 `json:"net_rx_total"`
		NetTxTotal float64 `json:"net_tx_total"`
		TCPConn    float64 `json:"tcp_conn"`
		UDPConn    float64 `json:"udp_conn"`
		Processes  float64 `json:"processes"`
		Load1      float64 `json:"load_1"`
		Load5      float64 `json:"load_5"`
		Load15     float64 `json:"load_15"`
		SwapUsed   float64 `json:"swap_used"`
		SwapTotal  float64 `json:"swap_total"`
	}

	var metrics []MetricRow
	for rows.Next() {
		var m MetricRow
		if err := rows.Scan(
			&m.Timestamp, &m.CPU, &m.Mem, &m.Disk,
			&m.NetRxSpeed, &m.NetTxSpeed, &m.NetRxTotal, &m.NetTxTotal,
			&m.TCPConn, &m.UDPConn, &m.Processes,
			&m.Load1, &m.Load5, &m.Load15,
			&m.SwapUsed, &m.SwapTotal,
			); err != nil {
				logger.Log.Error("Scan error in ApiDataHandler", 
					zap.String("module", "API"), 
					zap.Error(err),
				)
				continue
			}
		metrics = append(metrics, m)
	}

	if metrics == nil {
		metrics = []MetricRow{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// 获取节点的延迟/Ping历史数据（带降采样）
func ApiPingDataHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	rangeStr := r.URL.Query().Get("range")

	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	var bucketSeconds int64 = 5
	var timeLimit int64 = 3600

	switch rangeStr {
	case "realtime":
		bucketSeconds = 5
		timeLimit = 5 * 60
	case "1h":
		bucketSeconds = 60
		timeLimit = 3600
	case "6h":
		bucketSeconds = 60
		timeLimit = 6 * 3600
	case "12h":
		bucketSeconds = 600
		timeLimit = 12 * 3600
	case "1d", "24h":
		bucketSeconds = 180
		timeLimit = 86400
	case "7d":
		bucketSeconds = 3600
		timeLimit = 7 * 86400
	case "30d", "720h":
		bucketSeconds = 7200
		timeLimit = 30 * 86400
	}

	var startTime int64
	afterStr := r.URL.Query().Get("after")
	if afterStr != "" {
		if a, err := strconv.ParseInt(afterStr, 10, 64); err == nil && a > 0 {
			startTime = a
		}
	}
	if startTime == 0 {
		startTime = time.Now().Unix() - timeLimit
	}

	query := `
    SELECT 
        t.target, 
        t.name, 
        AVG(r.ping_ms) AS ping_ms, 
        AVG(COALESCE(NULLIF(r.loss, 0), json_extract(r.extra_data, '$.loss'), 0)) AS loss,
        AVG(COALESCE(NULLIF(r.jitter, 0), json_extract(r.extra_data, '$.jitter'), 0)) AS jitter,
        MAX(r.status) AS status, 
        (r.timestamp / ?) * ? AS bucket_time
    FROM task_results r
    JOIN monitor_tasks t ON r.task_id = t.id
    WHERE r.node_id = ? AND r.timestamp >= ?
    GROUP BY t.target, t.name, bucket_time
    ORDER BY bucket_time ASC
    `

    rows, err := database.DB.Query(query, bucketSeconds, bucketSeconds, nodeID, startTime)
    if err != nil {
		logger.Log.Error("Query Ping Data Error", 
			zap.String("module", "DB"), 
			zap.Error(err),
		)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
    defer rows.Close()

	type PingRecord struct {
        Target    string  `json:"target"`
        NodeName  string  `json:"node_name"`
        PingMs    float64 `json:"ping_ms"`
        Loss      float64 `json:"loss"`   // 新增
        Jitter    float64 `json:"jitter"` // 新增
        Status    string  `json:"status"`
        Timestamp int64   `json:"timestamp"`
    }

    var results []PingRecord
    for rows.Next() {
        var rec PingRecord
        // 3. 更新 Scan 参数顺序，必须与 SQL SELECT 的字段顺序严格对应
        err := rows.Scan(
            &rec.Target, 
            &rec.NodeName, 
            &rec.PingMs, 
            &rec.Loss,   // 新增
            &rec.Jitter, // 新增
            &rec.Status, 
            &rec.Timestamp,
        )
        if err != nil {
			logger.Log.Error("Query Ping Data Error", 
				zap.String("module", "DB"), 
				zap.Error(err),
			)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

        // 🌟 补充缺失的切片追加逻辑，并闭合 for 循环
        results = append(results, rec)
    }

    if results == nil {
        results = []PingRecord{}
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(results)
}

// --- 辅助函数：计算中位数 P50 ---
func getMedianP50(arr []float64) float64 {
	if len(arr) == 0 {
		return 0
	}
	sort.Float64s(arr)
	mid := len(arr) / 2
	if len(arr)%2 != 0 {
		return arr[mid]
	}
	return (arr[mid-1] + arr[mid]) / 2.0
}

// --- 辅助函数：生成样式状态 ---
func getLatencyStatus(ping float64) (string, string, string) {
	if ping <= 0 {
		return "超时", "#64748b", "#f1f5f9"
	}
	if ping <= 80 {
		return "优秀", "#059669", "#dcfce7"
	}
	if ping <= 200 {
		return "一般", "#d97706", "#fef3c7"
	}
	return "较差", "#dc2626", "#fee2e2"
}

// [API] 为前端概览面板专供的轻量化三网延迟摘要 (动态间隔自适应版)
func ApiPingSummaryHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	// 核心逻辑：使用 ROW_NUMBER 窗口函数，按 task_id 分组，提取每个任务最近的 10 条记录。
	// rn = 1 代表最新的一次执行，rn = 10 代表第 10 新的执行。
	query := `
	WITH RankedResults AS (
		SELECT 
			t.network_type, 
			r.ping_ms,
			ROW_NUMBER() OVER (PARTITION BY t.id ORDER BY r.timestamp DESC) as rn
		FROM task_results r
		JOIN monitor_tasks t ON r.task_id = t.id
		WHERE r.node_id = ? AND t.network_type IN ('电信', '联通', '移动')
	)
	SELECT network_type, ping_ms, rn
	FROM RankedResults
	WHERE rn <= 10
	ORDER BY rn DESC` // ORDER BY rn DESC 确保后端读取顺序从旧到新

	rows, err := database.DB.Query(query, nodeID)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	// 数据结构：timeline[次序深度][网络类型] = []延迟列表
	type Bucket map[string][]float64
	timeline := make(map[int]Bucket)

	for rows.Next() {
		var netName string
		var ping float64
		var rn int
		rows.Scan(&netName, &ping, &rn)

		if timeline[rn] == nil {
			timeline[rn] = make(Bucket)
		}
		// 不论是否超时（包括 0），全部计入切片，以准确反映当前次序的真实断流率
		timeline[rn][netName] = append(timeline[rn][netName], ping)
	}

	type Summary struct {
		Name       string    `json:"name"`
		Ping       string    `json:"ping"`
		Count      int       `json:"count"`
		History    []float64 `json:"history"`
		StatusText string    `json:"statusText"`
		Color      string    `json:"color"`
		BgColor    string    `json:"bgColor"`
	}

	var results []Summary
	for _, netName := range []string{"电信", "联通", "移动"} {
		var history []float64

		// 倒序遍历 rn (10 到 1)，确保推入 history 数组时，尾部始终为最新数据
		for i := 10; i >= 1; i-- {
			if pings, exists := timeline[i][netName]; exists && len(pings) > 0 {
				history = append(history, getMedianP50(pings))
			}
		}

		currentPing := 0.0
		if len(history) > 0 {
			currentPing = history[len(history)-1]
		}

		st, col, bg := getLatencyStatus(currentPing)
		
		// 查询该类别的测速节点基数
		countQuery := `SELECT COUNT(id) FROM monitor_tasks WHERE network_type = ?`
		var netCount int
		database.DB.QueryRow(countQuery, netName).Scan(&netCount)

		results = append(results, Summary{
			Name:       netName,
			Ping:       fmt.Sprintf("%.1f", currentPing),
			Count:      netCount,
			History:    history,
			StatusText: st,
			Color:      col,
			BgColor:    bg,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}