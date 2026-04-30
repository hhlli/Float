package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

func apiPublicServersHandler(w http.ResponseWriter, r *http.Request) {
	type ServerNode struct {
		NodeID       string  `json:"node_id"`
		Name         string  `json:"name"`
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
		IPv4         string  `json:"ipv4"`          // 补齐字段
		IPv6         string  `json:"ipv6"`          // 补齐字段
		AgentVersion string  `json:"agent_version"` // 追加字段
	}

	rows, err := db.Query(`
        SELECT node_id, name, region, cost, currency, billing_date, monthly_bw, bw_reset_day, notes, 
               last_active, cpu, mem, mem_used, mem_total, disk, disk_used, disk_total, 
               os, uptime, net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
               swap_used, swap_total, tcp_conn, udp_conn, kernel, arch, virt, cpu_model, processes, load_1, load_5, load_15,
               ipv4, ipv6, agent_version
        FROM servers
    `)
	if err != nil {
		http.Error(w, "Database query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var servers []ServerNode
	for rows.Next() {
		var s ServerNode
		var region, currency, billingDate, notes, os, kernel, arch, virt, cpuModel sql.NullString
		var ipv4, ipv6, agentVersion sql.NullString // 增加 Scan 接收变量
		var cost, monthlyBW, cpu, mem, memUsed, memTotal, disk, diskUsed, diskTotal sql.NullFloat64
		var netRxSpeed, netTxSpeed, netRxTotal, netTxTotal sql.NullFloat64
		var swapUsed, swapTotal, load1, load5, load15 sql.NullFloat64
		var bwResetDay, tcpConn, udpConn, processes sql.NullInt32
		var lastActive, uptime sql.NullInt64

		err := rows.Scan(
			&s.NodeID, &s.Name, &region, &cost, &currency, &billingDate, &monthlyBW, &bwResetDay, &notes,
			&lastActive, &cpu, &mem, &memUsed, &memTotal, &disk, &diskUsed, &diskTotal,
			&os, &uptime, &netRxSpeed, &netTxSpeed, &netRxTotal, &netTxTotal,
			&swapUsed, &swapTotal, &tcpConn, &udpConn, &kernel, &arch, &virt, &cpuModel, &processes, &load1, &load5, &load15,
			&ipv4, &ipv6, &agentVersion, // 严格对应 SELECT 顺序
		)
		if err != nil {
			log.Println("Row scan error:", err)
			continue
		}

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
		
		s.IPv4 = ipv4.String                 // 赋值
		s.IPv6 = ipv6.String                 // 赋值
		s.AgentVersion = agentVersion.String // 赋值

		servers = append(servers, s)
	}

	if servers == nil {
		servers = []ServerNode{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(servers); err != nil {
		log.Println("JSON encoding error:", err)
	}
}

// [API] 获取公开站点设置（供前台访客页面使用，已脱敏）
func apiPublicSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// 只查询安全的字段
	rows, err := db.Query(`
    SELECT key, value FROM settings 
    WHERE key IN ('site_name', 'site_desc', 'custom_footer', 'site_icon', 'require_login')
`)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			settings[k] = v
		}
	}

	settings["server_version"] = ServerVersion

	json.NewEncoder(w).Encode(settings)
}