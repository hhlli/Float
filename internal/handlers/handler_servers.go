package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time" // 🌟 新增导入

	"Float/internal/database"
	"Float/internal/logger"
	"go.uber.org/zap"
)

// 提取公共请求体结构，避免代码冗余
type ServerFormReq struct {
	NodeID      string  `json:"node_id"`
	Name        string  `json:"name"`
	Region      string  `json:"region"` // 🌟 修复：确保所有表单操作都包含 Region
	Cost        float64 `json:"cost"`
	Currency    string  `json:"currency"`
	BillingCycle string `json:"billing_cycle"` // 🌟 新增
	TerminalEnabled int `json:"terminal_enabled"` // 🌟 1. 新增终端状态字段
	BillingDate string  `json:"billing_date"`
	MonthlyBW   float64 `json:"monthly_bw"`
	BWResetDay  int     `json:"bw_reset_day"`
	Notes       string  `json:"notes"`
	IsHidden    bool    `json:"is_hidden"` // 🌟 新增：对接前端的隐藏状态
}

type AdminStaticServerNode struct {
	NodeID           string          `json:"node_id"`
	AuthToken        string          `json:"auth_token"`
	Name             string          `json:"name"`
	IPv4             string          `json:"ipv4"`
	IPv6             string          `json:"ipv6"`
	Region           string          `json:"region"`
	Cost             float64         `json:"cost"`
	Currency         string          `json:"currency"`
	BillingDate      string          `json:"billing_date"`
	MonthlyBW        float64         `json:"monthly_bw"`
	BWResetDay       int             `json:"bw_reset_day"`
	Notes            string          `json:"notes"`
	OS               string          `json:"os"`
	Kernel           string          `json:"kernel"`
	Arch             string          `json:"arch"`
	Virt             string          `json:"virt"`
	CPUModel         string          `json:"cpu_model"`
	AgentVersion     string          `json:"agent_version"`
	IsHidden         bool            `json:"is_hidden"`
	DockerContainers json.RawMessage `json:"docker_containers"`
	BillingCycle     string          `json:"billing_cycle"`
	TerminalEnabled  int             `json:"terminal_enabled"`
}

type AdminRealtimeServerNode struct {
	NodeID     string  `json:"node_id"`
	LastActive int64   `json:"last_active"`
	CPU        float64 `json:"cpu"`
	Mem        float64 `json:"mem"`
	MemUsed    float64 `json:"mem_used"`
	MemTotal   float64 `json:"mem_total"`
	Disk       float64 `json:"disk"`
	DiskUsed   float64 `json:"disk_used"`
	DiskTotal  float64 `json:"disk_total"`
	Uptime     int64   `json:"uptime"`
	NetRxSpeed float64 `json:"net_rx_speed"`
	NetTxSpeed float64 `json:"net_tx_speed"`
	NetRxTotal float64 `json:"net_rx_total"`
	NetTxTotal float64 `json:"net_tx_total"`
	SwapUsed   float64 `json:"swap_used"`
	SwapTotal  float64 `json:"swap_total"`
	TCPConn    int     `json:"tcp_conn"`
	UDPConn    int     `json:"udp_conn"`
	Processes  int     `json:"processes"`
	Load1      float64 `json:"load_1"`
	Load5      float64 `json:"load_5"`
	Load15     float64 `json:"load_15"`
}
func ApiStaticNodesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	rows, err := database.DB.Query(`
		SELECT node_id, auth_token, name, ipv4, ipv6, region, cost, currency, billing_date, monthly_bw, bw_reset_day, notes,
		       os, kernel, arch, virt, cpu_model, agent_version, is_hidden, docker_containers, billing_cycle, terminal_enabled 
		FROM servers
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var servers []AdminStaticServerNode
	for rows.Next() {
		var s AdminStaticServerNode
		var ipv4, ipv6, region, currency, billingDate, notes, os sql.NullString
		var cost, monthlyBW sql.NullFloat64
		var bwResetDay sql.NullInt32
		var isHidden sql.NullInt32
		var terminalEnabled sql.NullInt32
		var kernel, arch, virt, cpuModel, agentVersion, authToken sql.NullString
		var dockerContainers sql.NullString
		var billingCycle sql.NullString

		err := rows.Scan(
			&s.NodeID, &authToken, &s.Name, &ipv4, &ipv6, &region, &cost, &currency, &billingDate, &monthlyBW, &bwResetDay, &notes,
			&os, &kernel, &arch, &virt, &cpuModel, &agentVersion, &isHidden, &dockerContainers, &billingCycle, &terminalEnabled,
		)
		if err != nil {
			logger.Log.Error("Static row scan error", 
				zap.String("module", "DB"), 
				zap.Error(err),
			)
			continue
		}

		s.AuthToken = authToken.String
		s.IPv4 = ipv4.String
		s.IPv6 = ipv6.String
		s.Region = region.String
		s.Cost = cost.Float64
		s.Currency = currency.String
		s.BillingDate = billingDate.String
		s.MonthlyBW = monthlyBW.Float64
		s.BWResetDay = int(bwResetDay.Int32)
		s.Notes = notes.String
		s.OS = os.String
		s.Kernel = kernel.String
		s.Arch = arch.String
		s.Virt = virt.String
		s.CPUModel = cpuModel.String
		s.AgentVersion = agentVersion.String
		s.IsHidden = isHidden.Int32 == 1
		s.BillingCycle = billingCycle.String
		if s.BillingCycle == "" {
			s.BillingCycle = "month"
		}
		s.TerminalEnabled = int(terminalEnabled.Int32)

		if dockerContainers.Valid && dockerContainers.String != "" {
			s.DockerContainers = json.RawMessage(dockerContainers.String)
		} else {
			s.DockerContainers = json.RawMessage("[]")
		}

		if s.Currency == "" {
			s.Currency = "CNY"
		}
		if s.BWResetDay == 0 {
			s.BWResetDay = 1
		}

		servers = append(servers, s)
	}
	if servers == nil {
		servers = []AdminStaticServerNode{}
	}
	json.NewEncoder(w).Encode(servers)
}

func ApiRealtimeNodesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	rows, err := database.DB.Query(`
		SELECT node_id, last_active, cpu, mem, mem_used, mem_total, disk, disk_used, disk_total,
		       uptime, net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
		       swap_used, swap_total, tcp_conn, udp_conn, processes, load_1, load_5, load_15 
		FROM servers
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var servers []AdminRealtimeServerNode
	for rows.Next() {
		var s AdminRealtimeServerNode
		var cpu, mem, memUsed, memTotal, disk, diskUsed, diskTotal, netRxSpeed, netTxSpeed, netRxTotal, netTxTotal sql.NullFloat64
		var lastActive, uptime sql.NullInt64
		var swapUsed, swapTotal, load1, load5, load15 sql.NullFloat64
		var tcpConn, udpConn, processes sql.NullInt32

		err := rows.Scan(
			&s.NodeID, &lastActive, &cpu, &mem, &memUsed, &memTotal, &disk, &diskUsed, &diskTotal,
			&uptime, &netRxSpeed, &netTxSpeed, &netRxTotal, &netTxTotal,
			&swapUsed, &swapTotal, &tcpConn, &udpConn, &processes, &load1, &load5, &load15,
		)
		if err != nil {
			logger.Log.Error("Realtime row scan error", 
				zap.String("module", "DB"), 
				zap.Error(err),
			)
			continue
		}

		s.LastActive = lastActive.Int64
		s.CPU = cpu.Float64
		s.Mem = mem.Float64
		s.MemUsed = memUsed.Float64
		s.MemTotal = memTotal.Float64
		s.Disk = disk.Float64
		s.DiskUsed = diskUsed.Float64
		s.DiskTotal = diskTotal.Float64
		s.Uptime = uptime.Int64
		s.NetRxSpeed = netRxSpeed.Float64
		s.NetTxSpeed = netTxSpeed.Float64
		s.NetRxTotal = netRxTotal.Float64
		s.NetTxTotal = netTxTotal.Float64
		s.SwapUsed = swapUsed.Float64
		s.SwapTotal = swapTotal.Float64
		s.TCPConn = int(tcpConn.Int32)
		s.UDPConn = int(udpConn.Int32)
		s.Processes = int(processes.Int32)
		s.Load1 = load1.Float64
		s.Load5 = load5.Float64
		s.Load15 = load15.Float64

		servers = append(servers, s)
	}
	if servers == nil {
		servers = []AdminRealtimeServerNode{}
	}
	json.NewEncoder(w).Encode(servers)
}


func ApiSaveServerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ServerFormReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}

	b := make([]byte, 16)
	rand.Read(b)
	newToken := fmt.Sprintf("%x", b)

	// 🌟 增加布尔值到整数的转换
	isHiddenInt := 0
	if req.IsHidden {
		isHiddenInt = 1
	}

	query := `
INSERT INTO servers (
    node_id, auth_token, name, region, cost, currency, billing_cycle, billing_date, monthly_bw, bw_reset_day, notes, is_hidden, status, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'online', strftime('%s','now'))
ON CONFLICT(node_id) DO UPDATE SET
    name=excluded.name,
    region=excluded.region,
    cost=excluded.cost,
    currency=excluded.currency,
    billing_cycle=excluded.billing_cycle,  -- 🌟 新增
    billing_date=excluded.billing_date,
    monthly_bw=excluded.monthly_bw,
    bw_reset_day=excluded.bw_reset_day,
    notes=excluded.notes,
    is_hidden=excluded.is_hidden;
`
_, err := database.DB.Exec(query,
    req.NodeID, newToken, req.Name, req.Region, req.Cost, req.Currency, req.BillingCycle, 
    req.BillingDate, req.MonthlyBW, req.BWResetDay, req.Notes, isHiddenInt,
)
if err != nil {
    // 🌟 修复日志文本，并替换控制流为 HTTP 500 响应
    logger.Log.Error("保存节点配置失败", 
        zap.String("module", "DB"), 
        zap.Error(err),
    )
    http.Error(w, "Database error", http.StatusInternalServerError)
    return
}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"success"}`))
}

func ApiUpdateServerHandler(w http.ResponseWriter, r *http.Request) {
	var payload ServerFormReq
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 🌟 增加布尔值到整数的转换
	isHiddenInt := 0
	if payload.IsHidden {
		isHiddenInt = 1
	}

	_, err := database.DB.Exec(`
		UPDATE servers
		SET name = ?, region = ?, cost = ?, currency = ?, billing_cycle = ?, billing_date = ?, monthly_bw = ?, bw_reset_day = ?, notes = ?, is_hidden = ?
		WHERE node_id = ?
	`, payload.Name, payload.Region, payload.Cost, payload.Currency, payload.BillingCycle, payload.BillingDate, payload.MonthlyBW, payload.BWResetDay, payload.Notes, isHiddenInt, payload.NodeID)
	if err != nil {
		logger.Log.Error("更新节点信息失败", 
			zap.String("module", "DB"), 
			zap.Error(err),
		)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

func ApiDeleteServerHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodDelete {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    nodeID := r.URL.Query().Get("node_id")
    if nodeID == "" {
        http.Error(w, "Missing node_id", http.StatusBadRequest)
        return
    }

    // 1. 同步删除 servers 表记录
    // 立即阻断对应探针的后续鉴权，并快速响应前端完成界面更新
    _, err := database.DB.Exec("DELETE FROM servers WHERE node_id = ?", nodeID)
    if err != nil {
        http.Error(w, "Delete server failed", http.StatusInternalServerError)
        return
    }

    // 2. 开启后台 Goroutine 异步清理历史数据
    go func(id string) {
        // 延迟执行，等待当前持有锁的写入事务完全释放
        time.Sleep(2 * time.Second)
        
        // 独立执行数据表清理，不使用阻塞型全局事务
        _, err1 := database.DB.Exec("DELETE FROM metrics WHERE node_id = ?", id)
        if err1 != nil {
			logger.Log.Error("Async delete metrics failed", 
				zap.String("module", "DB"), 
				zap.String("node_id", id), 
				zap.Error(err1),
			)
		}

        _, err2 := database.DB.Exec("DELETE FROM task_results WHERE node_id = ?", id)
        if err2 != nil {
			logger.Log.Error("Async delete task_results failed", 
				zap.String("module", "DB"), 
				zap.String("node_id", id), 
				zap.Error(err2),
			)
		}
    }(nodeID)

    w.WriteHeader(http.StatusOK)
    w.Write([]byte(`{"status":"success"}`))
}
// 经纬度
type GeoServerNode struct {
	NodeID     string  `json:"node_id"`
	Name       string  `json:"name"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	Status     string  `json:"status"`
	LastActive int64   `json:"last_active"`
	IPv4       string  `json:"ipv4"`
	CPU        float64 `json:"cpu"`
	Mem        float64 `json:"mem"`
	Uptime     int64   `json:"uptime"`
}

func ApiGeoNodesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	rows, err := database.DB.Query(`
		SELECT node_id, name, latitude, longitude, status, last_active, ipv4, cpu, mem, uptime
		FROM servers
		WHERE is_hidden = 0
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var nodes []GeoServerNode
	for rows.Next() {
		var n GeoServerNode
		var lat, lng, cpu, mem sql.NullFloat64
		var lastActive, uptime sql.NullInt64
		var status, ipv4 sql.NullString

		err := rows.Scan(&n.NodeID, &n.Name, &lat, &lng, &status, &lastActive, &ipv4, &cpu, &mem, &uptime)
		if err != nil {
			logger.Log.Error("GeoNodes scan error", 
				zap.String("module", "DB"), 
				zap.Error(err),
			)
			continue
		}

		n.Latitude = lat.Float64
		n.Longitude = lng.Float64
		n.Status = status.String
		n.LastActive = lastActive.Int64
		n.IPv4 = ipv4.String
		n.CPU = cpu.Float64
		n.Mem = mem.Float64
		n.Uptime = uptime.Int64

		nodes = append(nodes, n)
	}
	if nodes == nil {
		nodes = []GeoServerNode{}
	}
	json.NewEncoder(w).Encode(nodes)
}