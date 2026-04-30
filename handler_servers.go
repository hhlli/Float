package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

// 读取节点列表接口
func apiNodesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	type ServerNode struct {
		NodeID       string  `json:"node_id"`
		Name         string  `json:"name"`
		IPv4         string  `json:"ipv4"`
		IPv6         string  `json:"ipv6"`
		Region       string  `json:"region"`
		Cost         float64 `json:"cost"`
		Currency     string  `json:"currency"`
		BillingDate  string  `json:"billing_date"`
		MonthlyBW    float64 `json:"monthly_bw"`
		BWResetDay   int     `json:"bw_reset_day"`
		Notes        string  `json:"notes"`
		LastActive   int64   `json:"last_active"`
		CPU          float64 `json:"cpu"`
		Mem          float64 `json:"mem"`
		MemUsed      float64 `json:"mem_used"`
		MemTotal     float64 `json:"mem_total"`
		Disk         float64 `json:"disk"`
		DiskUsed     float64 `json:"disk_used"`
		DiskTotal    float64 `json:"disk_total"`
		OS           string  `json:"os"`
		Uptime       int64   `json:"uptime"`
		NetRxSpeed   float64 `json:"net_rx_speed"`
		NetTxSpeed   float64 `json:"net_tx_speed"`
		NetRxTotal   float64 `json:"net_rx_total"`
		NetTxTotal   float64 `json:"net_tx_total"`
		// 🌟 以下为之前缺失的字段
		SwapUsed     float64 `json:"swap_used"`
		SwapTotal    float64 `json:"swap_total"`
		TCPConn      int     `json:"tcp_conn"`
		UDPConn      int     `json:"udp_conn"`
		Kernel       string  `json:"kernel"`
		Arch         string  `json:"arch"`
		Virt         string  `json:"virt"`
		CPUModel     string  `json:"cpu_model"`
		Processes    int     `json:"processes"`
		Load1        float64 `json:"load_1"`
		Load5        float64 `json:"load_5"`
		Load15       float64 `json:"load_15"`
		AgentVersion string  `json:"agent_version"` // 🌟 探针版本号
	}

	// 🌟 补全 SQL 查询语句
	rows, err := db.Query(`
		SELECT node_id, name, ipv4, ipv6, region, cost, currency, billing_date, monthly_bw, bw_reset_day, notes,
		       last_active, cpu, mem, mem_used, mem_total, disk, disk_used, disk_total,
		       os, uptime, net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
		       swap_used, swap_total, tcp_conn, udp_conn, kernel, arch, virt, cpu_model, processes, load_1, load_5, load_15, agent_version
		FROM servers
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var servers []ServerNode
	for rows.Next() {
		var s ServerNode
		var ipv4, ipv6, region, currency, billingDate, notes, os sql.NullString
		var cost, monthlyBW, cpu, mem, memUsed, memTotal, disk, diskUsed, diskTotal, netRxSpeed, netTxSpeed, netRxTotal, netTxTotal sql.NullFloat64
		var bwResetDay sql.NullInt32
		var lastActive, uptime sql.NullInt64
		
		// 🌟 增加用于接收新字段的变量
		var swapUsed, swapTotal, load1, load5, load15 sql.NullFloat64
		var tcpConn, udpConn, processes sql.NullInt32
		var kernel, arch, virt, cpuModel, agentVersion sql.NullString

		// 🌟 补全 rows.Scan 里的接收参数
		err := rows.Scan(
			&s.NodeID, &s.Name, &ipv4, &ipv6, &region, &cost, &currency, &billingDate, &monthlyBW, &bwResetDay, &notes,
			&lastActive, &cpu, &mem, &memUsed, &memTotal, &disk, &diskUsed, &diskTotal,
			&os, &uptime, &netRxSpeed, &netTxSpeed, &netRxTotal, &netTxTotal,
			&swapUsed, &swapTotal, &tcpConn, &udpConn, &kernel, &arch, &virt, &cpuModel, &processes, &load1, &load5, &load15, &agentVersion,
		)
		if err != nil {
			log.Println("Scan 错误:", err)
			continue
		}

		s.IPv4 = ipv4.String
		s.IPv6 = ipv6.String
		s.Region = region.String
		s.Cost = cost.Float64
		s.Currency = currency.String
		s.BillingDate = billingDate.String
		s.MonthlyBW = monthlyBW.Float64
		s.BWResetDay = int(bwResetDay.Int32)
		s.Notes = notes.String
		s.LastActive = lastActive.Int64
		s.CPU = cpu.Float64
		s.Mem = mem.Float64
		s.MemUsed = memUsed.Float64
		s.MemTotal = memTotal.Float64
		s.Disk = disk.Float64
		s.DiskUsed = diskUsed.Float64
		s.DiskTotal = diskTotal.Float64
		s.OS = os.String
		s.Uptime = uptime.Int64
		s.NetRxSpeed = netRxSpeed.Float64
		s.NetTxSpeed = netTxSpeed.Float64
		s.NetRxTotal = netRxTotal.Float64
		s.NetTxTotal = netTxTotal.Float64
		
		// 🌟 给结构体的新字段赋值
		s.SwapUsed = swapUsed.Float64
		s.SwapTotal = swapTotal.Float64
		s.TCPConn = int(tcpConn.Int32)
		s.UDPConn = int(udpConn.Int32)
		s.Kernel = kernel.String
		s.Arch = arch.String
		s.Virt = virt.String
		s.CPUModel = cpuModel.String
		s.Processes = int(processes.Int32)
		s.Load1 = load1.Float64
		s.Load5 = load5.Float64
		s.Load15 = load15.Float64
		s.AgentVersion = agentVersion.String // 🌟 探针版本号赋值完毕

		if s.Currency == "" {
			s.Currency = "CNY"
		}
		if s.BWResetDay == 0 {
			s.BWResetDay = 1
		}

		servers = append(servers, s)
	}

	if servers == nil {
		servers = []ServerNode{}
	}

	json.NewEncoder(w).Encode(servers)
}


func apiSaveServerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type ServerReq struct {
		NodeID      string  `json:"node_id"`
		Name        string  `json:"name"`
		Region      string  `json:"region"`
		Cost        float64 `json:"cost"`
		Currency    string  `json:"currency"`
		BillingDate string  `json:"billing_date"`
		MonthlyBW   float64 `json:"monthly_bw"`
		BWResetDay  int     `json:"bw_reset_day"`
		Notes       string  `json:"notes"`
	}

	var req ServerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}

	query := `
	INSERT INTO servers (
		node_id, name, region, cost, currency, billing_date, monthly_bw, bw_reset_day, notes, status, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'online', strftime('%s','now'))
	ON CONFLICT(node_id) DO UPDATE SET
		name=excluded.name,
		region=excluded.region,
		cost=excluded.cost,
		currency=excluded.currency,
		billing_date=excluded.billing_date,
		monthly_bw=excluded.monthly_bw,
		bw_reset_day=excluded.bw_reset_day,
		notes=excluded.notes;
	`
	_, err := db.Exec(query,
		req.NodeID, req.Name, req.Region, req.Cost, req.Currency,
		req.BillingDate, req.MonthlyBW, req.BWResetDay, req.Notes,
	)
	if err != nil {
		log.Println("SaveServer 失败:", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"success"}`))
}

func apiUpdateServerHandler(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		NodeID      string  `json:"node_id"`
		Name        string  `json:"name"`
		Cost        float64 `json:"cost"`
		Currency    string  `json:"currency"`
		BillingDate string  `json:"billing_date"`
		MonthlyBW   float64 `json:"monthly_bw"`
		BWResetDay  int     `json:"bw_reset_day"`
		Notes       string  `json:"notes"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	_, err := db.Exec(`
		UPDATE servers
		SET name = ?, cost = ?, currency = ?, billing_date = ?, monthly_bw = ?, bw_reset_day = ?, notes = ?
		WHERE node_id = ?
	`, payload.Name, payload.Cost, payload.Currency, payload.BillingDate, payload.MonthlyBW, payload.BWResetDay, payload.Notes, payload.NodeID)

	if err != nil {
		log.Println("更新节点信息失败:", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

func apiDeleteServerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	
	// 1. 删除节点基本信息
	_, err = tx.Exec("DELETE FROM servers WHERE node_id = ?", nodeID)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Delete server failed", http.StatusInternalServerError)
		return
	}
	
	// 2. 删除该节点上报的硬件指标历史
	_, err = tx.Exec("DELETE FROM metrics WHERE node_id = ?", nodeID)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Delete metrics failed", http.StatusInternalServerError)
		return
	}
	
	// 3. 删除该节点执行的测速结果 (task_results 拥有 node_id，可以直接删)
	_, err = tx.Exec("DELETE FROM task_results WHERE node_id = ?", nodeID)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Delete task results failed", http.StatusInternalServerError)
		return
	}
	
	// 🌟 核心修复：移除了对全局 monitor_tasks 表的错误删除逻辑

	tx.Commit()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"success"}`))
}