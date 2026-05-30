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

type DailyStatus struct {
	Date   string `json:"date"`
	Status string `json:"status"`
}

type HourlyStatus struct {
	Hour   string `json:"hour"`
	Status string `json:"status"`
}

type PublicStaticServerNode struct {
	NodeID           string          `json:"node_id"`
	Name             string          `json:"name"`
	Region           string          `json:"region"`
	Cost             float64         `json:"cost"`
	Currency         string          `json:"currency"`
	BillingCycle     string          `json:"billing_cycle"`
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
	DockerContainers json.RawMessage `json:"docker_containers"`
	History          []DailyStatus   `json:"history"`
	SLA              string          `json:"sla"`
	SLA24H           string          `json:"sla_24h"`
	History24H       []HourlyStatus  `json:"history_24h"`
	LastActive       int64           `json:"last_active"` // 供状态计算逻辑使用
}

// 动态实时指标结构体
type PublicRealtimeServerNode struct {
	NodeID     string  `json:"node_id"`
	Status     string  `json:"status"`
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

func ApiPublicStaticServersHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := database.DB.Query(`
        SELECT node_id, name, region, cost, currency, billing_cycle, billing_date, monthly_bw, bw_reset_day, notes, 
               os, kernel, arch, virt, cpu_model, agent_version, docker_containers, last_active 
        FROM servers WHERE is_hidden = 0
    `)
	if err != nil {
		http.Error(w, "Database query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	servers := make([]PublicStaticServerNode, 0)

	for rows.Next() {
		var s PublicStaticServerNode
		var region, currency, billingCycle, billingDate, notes, os, kernel, arch, virt, cpuModel, agentVersion sql.NullString
		var cost, monthlyBW sql.NullFloat64
		var bwResetDay sql.NullInt32
		var lastActive sql.NullInt64
		var dockerContainers sql.NullString

		err := rows.Scan(
			&s.NodeID, &s.Name, &region, &cost, &currency, &billingCycle, &billingDate, &monthlyBW, &bwResetDay, &notes,
			&os, &kernel, &arch, &virt, &cpuModel, &agentVersion, &dockerContainers, &lastActive,
		)
		if err != nil {
			logger.Log.Error("Row scan error", 
				zap.String("module", "DB"), 
				zap.Error(err),
			)
			continue
		}

		s.Region = region.String
		s.Cost = cost.Float64
		s.Currency = currency.String
		s.BillingCycle = billingCycle.String
		if s.BillingCycle == "" {
			s.BillingCycle = "month"
		}
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
		s.LastActive = lastActive.Int64

		if dockerContainers.Valid && dockerContainers.String != "" {
			s.DockerContainers = json.RawMessage(dockerContainers.String)
		} else {
			s.DockerContainers = json.RawMessage("[]")
		}
		servers = append(servers, s)
	}

	// === 🌟 性能核心：纯内存缓存读取 ===
	database.CacheMutex.RLock()
	localHistoryMap := database.HeatmapCacheMap
	localActiveMins := database.ActiveMinsCacheMap
	localHourlyMap := database.HourlyCacheMap
	database.CacheMutex.RUnlock()
	// ===================================

	now := time.Now()
	nowUnix := now.Unix()

	type dateMeta struct {
		DateStr    string
		TargetUnix int64
		IsToday    bool
	}
	var last30Days [30]dateMeta
	for d := 29; d >= 0; d-- {
		tDate := now.AddDate(0, 0, -d)
		last30Days[29-d] = dateMeta{
			DateStr:    tDate.Format("01-02"),
			TargetUnix: tDate.Unix(),
			IsToday:    d == 0,
		}
	}

	for i := range servers {
		srv := &servers[i]
		nodeHistMap := localHistoryMap[srv.NodeID]

		// 30天历史状态计算
		hist := make([]DailyStatus, 0, 30)
		onlineCount := 0
		for _, meta := range last30Days {
			status := "nodata"
			if nodeHistMap != nil && nodeHistMap[meta.DateStr] {
				status = "online"
				onlineCount++
			} else {
				if meta.IsToday && (nowUnix-srv.LastActive < 180) {
					status = "online"
					onlineCount++
				} else if srv.LastActive > 0 && meta.TargetUnix < nowUnix {
					status = "offline"
				}
			}
			hist = append(hist, DailyStatus{Date: meta.DateStr, Status: status})
		}
		srv.History = hist

		sla := 100.00
		if srv.LastActive > 0 {
			sla = (float64(onlineCount) / 30.0) * 100
		}
		srv.SLA = fmt.Sprintf("%.2f", sla)

		// 24小时 SLA 计算
		activeMins := localActiveMins[srv.NodeID]
		if activeMins > 1440 {
			activeMins = 1440
		}
		sla24h := 100.00
		if srv.LastActive > 0 {
			sla24h = (float64(activeMins) / 1440.0) * 100
		} else {
			sla24h = 0.00
		}
		srv.SLA24H = fmt.Sprintf("%.2f", sla24h)

		// 24小时时间轴组装
		nodeHourlyMap := localHourlyMap[srv.NodeID]
		hist24 := make([]HourlyStatus, 0, 24)
		for h := 23; h >= 0; h-- {
			hourStr := now.Add(-time.Duration(h) * time.Hour).Format("15")
			status := "nodata"
			minsInHour := 0
			if nodeHourlyMap != nil {
				minsInHour = nodeHourlyMap[hourStr]
			}
			if minsInHour >= 45 {
				status = "online"
			} else if minsInHour > 0 {
				status = "warning"
			} else if srv.LastActive > 0 {
				status = "offline"
			}
			hist24 = append(hist24, HourlyStatus{Hour: hourStr, Status: status})
		}
		if nowUnix-srv.LastActive < 180 {
			hist24[23].Status = "online"
		}
		srv.History24H = hist24
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(servers)
}

func ApiPublicRealtimeServersHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := database.DB.Query(`
		SELECT node_id, status, last_active, cpu, mem, mem_used, mem_total, 
		       disk, disk_used, disk_total, uptime, net_rx_speed, net_tx_speed, 
		       net_rx_total, net_tx_total, swap_used, swap_total, tcp_conn, 
		       udp_conn, processes, load_1, load_5, load_15 
		FROM servers WHERE is_hidden = 0
	`)
	if err != nil {
		http.Error(w, "Database query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	realtimeMetrics := make([]PublicRealtimeServerNode, 0)

	for rows.Next() {
		var s PublicRealtimeServerNode
		var status sql.NullString
		var lastActive, uptime sql.NullInt64
		var cpu, mem, memUsed, memTotal, disk, diskUsed, diskTotal sql.NullFloat64
		var netRxSpeed, netTxSpeed, netRxTotal, netTxTotal sql.NullFloat64
		var swapUsed, swapTotal, load1, load5, load15 sql.NullFloat64
		var tcpConn, udpConn, processes sql.NullInt32

		err := rows.Scan(
			&s.NodeID, &status, &lastActive, &cpu, &mem, &memUsed, &memTotal,
			&disk, &diskUsed, &diskTotal, &uptime, &netRxSpeed, &netTxSpeed,
			&netRxTotal, &netTxTotal, &swapUsed, &swapTotal, &tcpConn,
			&udpConn, &processes, &load1, &load5, &load15,
		)
		if err != nil {
			logger.Log.Error("Realtime row scan error", 
				zap.String("module", "DB"), 
				zap.Error(err),
			)
			continue
		}

		s.Status = status.String
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

		realtimeMetrics = append(realtimeMetrics, s)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(realtimeMetrics)
}

func ApiPublicSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	rows, err := database.DB.Query(`
    SELECT key, value FROM settings 
    WHERE key IN ('site_name', 'site_desc', 'custom_footer', 'site_icon', 'require_login', 'theme')
`)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	settings := make(map[string]string, 8) // 扩大容量以容纳坐标字段
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			settings[k] = v
		}
	}

	// 默认兜底坐标
	settings["server_lat"] = "31.2304"
	settings["server_lon"] = "121.4737"

	// 后端请求不带 IP 的 JSON 接口，直接获取服务器本土公网位置
	if resp, err := http.Get("http://ip-api.com/json/"); err == nil {
		var geo struct {
			Status string  `json:"status"`
			Lat    float64 `json:"lat"`
			Lon    float64 `json:"lon"`
		}
		if json.NewDecoder(resp.Body).Decode(&geo) == nil && geo.Status == "success" {
			settings["server_lat"] = fmt.Sprintf("%.4f", geo.Lat)
			settings["server_lon"] = fmt.Sprintf("%.4f", geo.Lon)
		}
		resp.Body.Close()
	}

	settings["server_version"] = database.ServerVersion
	json.NewEncoder(w).Encode(settings)
}