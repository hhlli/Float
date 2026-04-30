package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
	"log"
)

// 处理探针发来的指标数据
// 处理探针发来的指标数据
func apiReceiveHandler(w http.ResponseWriter, r *http.Request) {
	// 1. 声明并获取客户端 IP
	clientIP := getClientIP(r)

	var payload struct {
		NodeID string `json:"node_id"`
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
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 2. 触发异步 GeoIP 查询
	go fetchAndSaveGeoIP(payload.NodeID, clientIP)

	now := time.Now().Unix()

	// 1. 更新 servers 表（当前状态）
	query := `
		INSERT INTO servers (
			node_id, name, region, cost, currency, billing_date, monthly_bw, bw_reset_day, notes, 
			last_active, cpu, mem, mem_used, mem_total, disk, disk_used, disk_total, 
			os, uptime, net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
			swap_used, swap_total, tcp_conn, udp_conn, kernel, arch, virt, cpu_model, processes, load_1, load_5, load_15, status
		) VALUES (
			?, ?, 'UN', 0, 'CNY', '', 0, 1, '', 
			?, ?, ?, ?, ?, ?, ?, ?, 
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'online'
		)
		ON CONFLICT(node_id) DO UPDATE SET 
			last_active=excluded.last_active,
			cpu=excluded.cpu,
			mem=excluded.mem,
			mem_used=excluded.mem_used,
			mem_total=excluded.mem_total,
			disk=excluded.disk,
			disk_used=excluded.disk_used,
			disk_total=excluded.disk_total,
			os=excluded.os,
			uptime=excluded.uptime,
			net_rx_speed=excluded.net_rx_speed,
			net_tx_speed=excluded.net_tx_speed,
			net_rx_total=excluded.net_rx_total,
			net_tx_total=excluded.net_tx_total,
			swap_used=excluded.swap_used,
			swap_total=excluded.swap_total,
			tcp_conn=excluded.tcp_conn,
			udp_conn=excluded.udp_conn,
			kernel=excluded.kernel,
			arch=excluded.arch,
			virt=excluded.virt,
			cpu_model=excluded.cpu_model,
			processes=excluded.processes,
			load_1=excluded.load_1,
			load_5=excluded.load_5,
			load_15=excluded.load_15,
			status='online';
	`

	_, err := db.Exec(query,
		payload.NodeID, payload.NodeID,
		now, payload.Data.CPU, payload.Data.Mem, payload.Data.MemUsed, payload.Data.MemTotal,
		payload.Data.Disk, payload.Data.DiskUsed, payload.Data.DiskTotal,
		payload.Data.OS, payload.Data.Uptime, payload.Data.NetRxSpeed, payload.Data.NetTxSpeed, payload.Data.NetRxTotal, payload.Data.NetTxTotal,
		payload.Data.SwapUsed, payload.Data.SwapTotal, payload.Data.TCPConn, payload.Data.UDPConn,
		payload.Data.Kernel, payload.Data.Arch, payload.Data.Virt, payload.Data.CPUModel,
		payload.Data.Processes, payload.Data.Load1, payload.Data.Load5, payload.Data.Load15,
	)
	if err != nil {
		log.Println("写入节点指标失败:", err)
		http.Error(w, "Database write error", http.StatusInternalServerError)
		return
	}

	// 2. 同时写一条历史记录进 metrics 表（供图表查询）
	_, err = db.Exec(`
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
		log.Println("写入 metrics 历史失败:", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

// 获取节点历史指标（供图表使用）
func apiDataHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	// 🌟 修改：支持解析前端传来的 "1h", "1d" 等字符串参数
	rangeStr := r.URL.Query().Get("range")
	var rangeSeconds int64 = 300 // 默认 5 分钟 (实时)

	switch rangeStr {
	case "realtime":
		rangeSeconds = 300
	case "1h":
		rangeSeconds = 3600
	case "6h":
		rangeSeconds = 6 * 3600
	case "12h":
		rangeSeconds = 12 * 3600
	case "1d", "24h":
		rangeSeconds = 86400
	case "7d":
		rangeSeconds = 7 * 86400
	case "30d", "720h":
		rangeSeconds = 30 * 86400
	default:
		// 如果都不是，尝试降级兼容纯数字秒数解析
		if v, err := strconv.ParseInt(rangeStr, 10, 64); err == nil && v > 0 {
			rangeSeconds = v
		}
	}

	since := time.Now().Unix() - rangeSeconds

	rows, err := db.Query(`
		SELECT timestamp, cpu_usage, mem_usage, disk_usage,
		       net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
		       tcp_conn, udp_conn, processes, load_1, load_5, load_15,
		       swap_used, swap_total
		FROM metrics
		WHERE node_id = ? AND timestamp >= ?
		ORDER BY timestamp ASC`,
		nodeID, since,
	)
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
		TCPConn    int     `json:"tcp_conn"`
		UDPConn    int     `json:"udp_conn"`
		Processes  int     `json:"processes"`
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

// [API] 获取节点的延迟/Ping历史数据
func apiPingDataHandler(w http.ResponseWriter, r *http.Request) {
    nodeID := r.URL.Query().Get("node_id")
    rangeStr := r.URL.Query().Get("range")

    if nodeID == "" {
        http.Error(w, "Missing node_id", http.StatusBadRequest)
        return
    }

    // 1. 解析时间范围
    var timeLimit int64 = 3600 
    switch rangeStr {
    case "realtime":
        timeLimit = 5 * 60
    case "1h":
        timeLimit = 3600
    case "6h":
        timeLimit = 6 * 3600
    case "1d", "24h":
        timeLimit = 86400
    case "7d":
        timeLimit = 7 * 86400
    case "30d", "720h":
        timeLimit = 30 * 86400
    }

    startTime := time.Now().Unix() - timeLimit

// 🌟 修改 SQL：使用 r.node_id 筛选指定节点的数据
query := `
SELECT t.target, t.name, r.ping_ms, r.status, r.timestamp
FROM task_results r
JOIN monitor_tasks t ON r.task_id = t.id
WHERE r.node_id = ? AND r.timestamp >= ?
ORDER BY r.timestamp ASC
`

    rows, err := db.Query(query, nodeID, startTime)
    if err != nil {
        log.Println("Query Ping Data Error:", err)
        http.Error(w, "Database error", http.StatusInternalServerError)
        return
    }
    defer rows.Close()

    // 3. 🌟 修改结构体：增加 NodeName 字段，json 标签必须是 node_name
    type PingRecord struct {
		Target    string  `json:"target"`
        NodeName  string  `json:"node_name"` 
        PingMs    float64 `json:"ping_ms"` 
        Status    string  `json:"status"`
        Timestamp int64   `json:"timestamp"`
    }

    var results []PingRecord
    for rows.Next() {
        var rec PingRecord
        // 🌟 修改 Scan：增加对 rec.NodeName 的映射
		err := rows.Scan(&rec.Target, &rec.NodeName, &rec.PingMs, &rec.Status, &rec.Timestamp)
        if err != nil {
            log.Println("Scan error:", err)
            continue
        }
        results = append(results, rec)
    }
    
    if results == nil {
        results = []PingRecord{}
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(results)
}