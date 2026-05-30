package handlers

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"
	
	
	"net/url"
	"Float/internal/core"
	"Float/internal/database"
	"Float/internal/logger"
	"go.uber.org/zap"

	"github.com/gorilla/websocket"
)

// ── WebSocket 升级器 ────────────────────────────────────
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}

		u, err := url.Parse(origin)
		if err != nil {
			return false
		}

		// 1. 匹配直连时的 Host
		if u.Host == r.Host {
			return true
		}

		// 2. 匹配反向代理层传递的真实 Host
		forwardedHost := r.Header.Get("X-Forwarded-Host")
		if forwardedHost != "" && u.Host == forwardedHost {
			return true
		}

		if u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" {
			return true
		}

		return false
	},
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// ── JSON-RPC 结构 ───────────────────────────────────────
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      interface{}     `json:"id,omitempty"`
}

type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── 探针上报的指标结构 ───────────────────────────────────
type AgentReport struct {
	NodeID string `json:"node_id"`
	Data   struct {
		Timestamp    int64   `json:"timestamp"`
		IPv4         string  `json:"ipv4"`
		IPv6         string  `json:"ipv6"`
		OS           string  `json:"os"`
		Uptime       int64   `json:"uptime"`
		CPU          float64 `json:"cpu"`
		Mem          float64 `json:"mem"`
		MemUsed      float64 `json:"mem_used"`
		MemTotal     float64 `json:"mem_total"`
		Disk         float64 `json:"disk"`
		DiskUsed     float64 `json:"disk_used"`
		DiskTotal    float64 `json:"disk_total"`
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
		AgentVersion string  `json:"agent_version"`
		// 🌟 新增 Docker 字段
		DockerContainers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			State  string `json:"state"`
			Status string `json:"status"`
			// 🌟 为详细资源预留的字段
			CPU    float64 `json:"cpu,omitempty"`
			Mem    float64 `json:"mem,omitempty"`
			MemPct float64 `json:"mem_pct,omitempty"`
		} `json:"docker_containers,omitempty"`
		TerminalEnabled bool `json:"terminal_enabled"`
	} `json:"data"`
}

type AgentTaskResult struct {
	NodeID string  `json:"node_id"`
	TaskID int     `json:"task_id"`
	PingMs float64 `json:"ping_ms"`
	Loss   float64 `json:"loss"`
	Jitter float64 `json:"jitter"`
	P50    float64 `json:"p50"`
	P99    float64 `json:"p99"`
	MinMs  float64 `json:"min_ms"`
	MaxMs  float64 `json:"max_ms"`
}

type AgentConn struct {
	conn   *websocket.Conn
	nodeID string
	mu     sync.Mutex
}

var (
	agentConns   = make(map[string]*AgentConn)
	agentConnsMu sync.RWMutex
)

// ── 前端 WebSocket 广播中心 ───────────────────────────────
type FrontendClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

var (
	frontendClients   = make(map[*FrontendClient]bool)
	frontendClientsMu sync.RWMutex
)

// BroadcastRealtimeData 向所有已连接的前端页面广播实时指标
func BroadcastRealtimeData(data interface{}) {
	msg, err := json.Marshal(data)
	if err != nil {
		return
	}

	frontendClientsMu.RLock()
	for client := range frontendClients {
		// 采用异步发送，防止某个由于网络波动的慢节点阻塞整个广播队列
		go func(c *FrontendClient) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			c.conn.WriteMessage(websocket.TextMessage, msg)
		}(client)
	}
	frontendClientsMu.RUnlock()
}

func pushTasksToAgent(nodeID string, tasks []MonitorTask) {
	agentConnsMu.RLock()
	ac, ok := agentConns[nodeID]
	agentConnsMu.RUnlock()
	if !ok {
		return
	}

	msg := RPCRequest{
		JSONRPC: "2.0",
		Method:  "tasks.push",
		Params:  mustMarshal(tasks),
	}

	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.conn.WriteJSON(msg)
}

type MonitorTask struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	Target   string `json:"target"`
	Interval int    `json:"interval"`
}

func mustMarshal(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ── WebSocket 主处理器 ───────────────────────────────────
func WsAgentHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Agent-Token")
	}
	if token == "" {
		http.Error(w, "Missing Token", http.StatusUnauthorized)
		return
	}

	var dbNodeID, dbIPv4 string
	err := database.DB.QueryRow("SELECT node_id, ipv4 FROM servers WHERE auth_token = ? AND auth_token != ''", token).Scan(&dbNodeID, &dbIPv4)
	if err == sql.ErrNoRows {
		http.Error(w, "Unauthorized: Invalid Token", http.StatusUnauthorized)
		return
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	clientIP := core.GetClientIP(r)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Log.Error("WebSocket 升级失败", zap.String("module", "WS"), zap.Error(err))
		return
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})

	var nodeID string
	ac := &AgentConn{conn: conn}

	stopPing := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ac.mu.Lock()
				conn.WriteMessage(websocket.PingMessage, nil)
				ac.mu.Unlock()
			case <-stopPing:
				return
			}
		}
	}()
	defer close(stopPing)

	for {
		var req RPCRequest
		if err := conn.ReadJSON(&req); err != nil {
			if nodeID != "" {
				agentConnsMu.Lock()
				delete(agentConns, nodeID)
				agentConnsMu.Unlock()
				logger.Log.Info("Agent disconnected", zap.String("module", "WS"), zap.String("node_id", nodeID))
			}
			break
		}

		conn.SetReadDeadline(time.Now().Add(120 * time.Second))

		switch req.Method {
		case "report":
			var report AgentReport
			if err := json.Unmarshal(req.Params, &report); err != nil {
				sendRPCError(conn, ac, req.ID, -32600, "invalid params")
				continue
			}

			if report.NodeID != dbNodeID {
				sendRPCError(conn, ac, req.ID, -32602, "NodeID mismatch with Auth Token")
				continue
			}

			if nodeID == "" && report.NodeID != "" {
				nodeID = report.NodeID
				ac.nodeID = nodeID
				agentConnsMu.Lock()
				agentConns[nodeID] = ac
				agentConnsMu.Unlock()
				logger.Log.Info("Agent connected", zap.String("module", "WS"), zap.String("node_id", nodeID))

				// 修复了此处断裂的匿名函数
				go func(nid, cip string) {
					var currentIP string
					var currentRegion string
					err := database.DB.QueryRow("SELECT ipv4, region FROM servers WHERE node_id = ?", nid).Scan(&currentIP, &currentRegion)

					var ipToUse string
					if err == nil && currentRegion != "UN" && currentRegion != "" {
						ipToUse = currentIP
					} else {
						ipToUse = cip
					}

					core.FetchAndSaveGeoIP(nid, ipToUse)
				}(nodeID, clientIP)
			}

			isPrivateIP := func(ipStr string) bool {
				parsed := net.ParseIP(ipStr)
				if parsed == nil {
					return true
				}
				return parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast()
			}

			finalIPv4 := report.Data.IPv4
			if finalIPv4 == "" || isPrivateIP(finalIPv4) {
				if dbIPv4 != "" && !isPrivateIP(dbIPv4) {
					finalIPv4 = dbIPv4
				} else {
					finalIPv4 = clientIP
				}
			}
			d := report.Data

			dockerJSON, _ := json.Marshal(d.DockerContainers)
			if string(dockerJSON) == "null" {
				dockerJSON = []byte("[]")
			}

			now := time.Now().Unix()
			terminalStatus := 0
			if d.TerminalEnabled {
				terminalStatus = 1
			}

			res, err := database.DB.Exec(`
				UPDATE servers SET 
					last_active=?, cpu=?, mem=?, mem_used=?, mem_total=?, 
					disk=?, disk_used=?, disk_total=?, os=?, uptime=?, 
					net_rx_speed=?, net_tx_speed=?, net_rx_total=?, net_tx_total=?, 
					swap_used=?, swap_total=?, tcp_conn=?, udp_conn=?, 
					kernel=?, arch=?, virt=?, cpu_model=?, processes=?, 
					load_1=?, load_5=?, load_15=?, ipv4=?, ipv6=?, 
					agent_version=?, docker_containers=?, status='online', terminal_enabled=?
				WHERE node_id=?;
			`,
				now, d.CPU, d.Mem, d.MemUsed, d.MemTotal,
				d.Disk, d.DiskUsed, d.DiskTotal, d.OS, d.Uptime,
				d.NetRxSpeed, d.NetTxSpeed, d.NetRxTotal, d.NetTxTotal,
				d.SwapUsed, d.SwapTotal, d.TCPConn, d.UDPConn,
				d.Kernel, d.Arch, d.Virt, d.CPUModel, d.Processes,
				d.Load1, d.Load5, d.Load15, finalIPv4, d.IPv6,
				d.AgentVersion, string(dockerJSON), terminalStatus,
				report.NodeID,
			)

			if err != nil {
				logger.Log.Error("Failed to update servers table", zap.String("module", "DB"), zap.Error(err))
			} else {
				rowsAffected, _ := res.RowsAffected()
				if rowsAffected == 0 {
					logger.Log.Warn("Node deleted, forcing WS disconnect", zap.String("module", "WS"), zap.String("node_id", report.NodeID))
					sendRPCError(conn, ac, req.ID, -32602, "Node has been deleted")
					break
				}
			}

			// 补充了指标插入的错误拦截
			_, metricsErr := database.DB.Exec(`INSERT INTO metrics (
				node_id, timestamp, cpu_usage, mem_usage, disk_usage,
				net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
				tcp_conn, udp_conn, processes, load_1, load_5, load_15,
				swap_used, swap_total
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				report.NodeID, now,
				d.CPU, d.Mem, d.Disk,
				d.NetRxSpeed, d.NetTxSpeed, d.NetRxTotal, d.NetTxTotal,
				d.TCPConn, d.UDPConn, d.Processes,
				d.Load1, d.Load5, d.Load15,
				d.SwapUsed, d.SwapTotal,
			)

			if metricsErr != nil {
				logger.Log.Error("Failed to insert metrics",
					zap.String("module", "DB"),
					zap.String("node_id", report.NodeID),
					zap.Error(metricsErr),
				)
			}

			realtimeDiff := map[string]interface{}{
				"node_id":      report.NodeID,
				"status":       "online",
				"last_active":  now,
				"cpu":          d.CPU,
				"mem":          d.Mem,
				"mem_used":     d.MemUsed,
				"mem_total":    d.MemTotal,
				"disk":         d.Disk,
				"disk_used":    d.DiskUsed,
				"disk_total":   d.DiskTotal,
				"uptime":       d.Uptime,
				"net_rx_speed": d.NetRxSpeed,
				"net_tx_speed": d.NetTxSpeed,
				"net_rx_total": d.NetRxTotal,
				"net_tx_total": d.NetTxTotal,
				"swap_used":    d.SwapUsed,
				"swap_total":   d.SwapTotal,
				"tcp_conn":     d.TCPConn,
				"udp_conn":     d.UDPConn,
				"processes":    d.Processes,
				"load_1":       d.Load1,
				"load_5":       d.Load5,
				"load_15":      d.Load15,
			}
			BroadcastRealtimeData(realtimeDiff)

			tasks := getTasksForNode(report.NodeID)
			sendRPCResult(conn, ac, req.ID, map[string]interface{}{
				"status": "ok",
				"tasks":  tasks,
			})

		case "task.result":
			var result AgentTaskResult
			if err := json.Unmarshal(req.Params, &result); err != nil {
				sendRPCError(conn, ac, req.ID, -32600, "invalid params")
				continue
			}

			if result.NodeID != dbNodeID {
				sendRPCError(conn, ac, req.ID, -32602, "NodeID mismatch with Auth Token")
				continue
			}

			extraMap := map[string]float64{
				"p50":    result.P50,
				"p99":    result.P99,
				"min_ms": result.MinMs,
				"max_ms": result.MaxMs,
			}
			extraBytes, _ := json.Marshal(extraMap)

			_, err := database.DB.Exec(
				"INSERT INTO task_results (task_id, node_id, ping_ms, status, timestamp, loss, jitter, extra_data) VALUES (?,?,?,?,?,?,?,?)",
				result.TaskID, result.NodeID, result.PingMs, "online", time.Now().Unix(), result.Loss, result.Jitter, string(extraBytes),
			)
			if err != nil {
				logger.Log.Error("Failed to insert task result", zap.String("module", "DB"), zap.Error(err))
			}
			sendRPCResult(conn, ac, req.ID, "ok")

		default:
			sendRPCError(conn, ac, req.ID, -32601, "method not found")
		}
	}
}

// ── 查询节点任务列表 ─────────────────────────────────────
func getTasksForNode(nodeID string) []MonitorTask {
	rows, err := database.DB.Query("SELECT id, target, type, interval, excluded_nodes FROM monitor_tasks")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tasks []MonitorTask
	for rows.Next() {
		var t MonitorTask
		var excludedStr string
		if err := rows.Scan(&t.ID, &t.Target, &t.Type, &t.Interval, &excludedStr); err == nil {
			var excludedNodes []string
			if excludedStr != "" && excludedStr != "[]" {
				json.Unmarshal([]byte(excludedStr), &excludedNodes)
			}

			isExcluded := false
			for _, exID := range excludedNodes {
				if exID == nodeID {
					isExcluded = true
					break
				}
			}

			if !isExcluded {
				tasks = append(tasks, t)
			}
		}
	}

	if tasks == nil {
		tasks = []MonitorTask{}
	}

	return tasks
}

// ── 工具函数 ─────────────────────────────────────────────
func sendRPCResult(conn *websocket.Conn, ac *AgentConn, id interface{}, result interface{}) {
	resp := RPCResponse{JSONRPC: "2.0", ID: id, Result: result}
	ac.mu.Lock()
	defer ac.mu.Unlock()
	conn.WriteJSON(resp)
}

func sendRPCError(conn *websocket.Conn, ac *AgentConn, id interface{}, code int, msg string) {
	resp := RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
	ac.mu.Lock()
	defer ac.mu.Unlock()
	conn.WriteJSON(resp)
}

// ── 暴露给终端模块的辅助函数 ──────────────────────────────────
func RequestAgentTerminal(nodeID, sessionID string) bool {
	agentConnsMu.RLock()
	ac, ok := agentConns[nodeID]
	agentConnsMu.RUnlock()

	if !ok {
		return false
	}

	msg := RPCRequest{
		JSONRPC: "2.0",
		Method:  "terminal.request",
		Params:  mustMarshal(map[string]string{"session_id": sessionID}),
	}

	ac.mu.Lock()
	defer ac.mu.Unlock()
	if err := ac.conn.WriteJSON(msg); err != nil {
		return false
	}
	return true
}

func ApiGenerateWsTicketHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	ticket := core.GenerateWSTicket(nodeID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"ticket": ticket,
	})
}

// ── 前端面板 WebSocket 处理器 ─────────────────────────────
func WsFrontendHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Log.Error("Frontend WS upgrade failed", zap.String("module", "WS"), zap.Error(err))
		return
	}

	client := &FrontendClient{conn: conn}

	frontendClientsMu.Lock()
	frontendClients[client] = true
	frontendClientsMu.Unlock()

	defer func() {
		frontendClientsMu.Lock()
		delete(frontendClients, client)
		frontendClientsMu.Unlock()
		conn.Close()
	}()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			client.mu.Lock()
			err := conn.WriteMessage(websocket.PingMessage, nil)
			client.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}