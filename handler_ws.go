package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ── WebSocket 升级器 ────────────────────────────────────
var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
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
		Timestamp  int64   `json:"timestamp"`
		IPv4       string  `json:"ipv4"` // 🌟 新增
		IPv6       string  `json:"ipv6"` // 🌟 新增
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
		AgentVersion string `json:"agent_version"` // 🌟 新增：接收探针版本
	} `json:"data"`
}

// ── 任务结果结构 ─────────────────────────────────────────
type AgentTaskResult struct {
	NodeID string  `json:"node_id"`
	TaskID int     `json:"task_id"`
	PingMs float64 `json:"ping_ms"`
}

// ── 连接管理 ─────────────────────────────────────────────
type AgentConn struct {
	conn   *websocket.Conn
	nodeID string
	mu     sync.Mutex
}

var (
	agentConns   = make(map[string]*AgentConn)
	agentConnsMu sync.RWMutex
)

// 向指定探针推送任务
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
func wsAgentHandler(w http.ResponseWriter, r *http.Request) {
	// 1. 鉴权
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Agent-Token")
	}
	expectedToken := getServerToken()
	if token != expectedToken {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	clientIP := getClientIP(r)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket 升级失败:", err)
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

	// 3. 心跳 goroutine
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

	// 4. 消息循环
	for {
		var req RPCRequest
		if err := conn.ReadJSON(&req); err != nil {
			if nodeID != "" {
				agentConnsMu.Lock()
				delete(agentConns, nodeID)
				agentConnsMu.Unlock()
				log.Printf("[WS] 探针 %s 断开连接\n", nodeID)
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

			if nodeID == "" && report.NodeID != "" {
				nodeID = report.NodeID
				ac.nodeID = nodeID
				agentConnsMu.Lock()
				agentConns[nodeID] = ac
				agentConnsMu.Unlock()
				log.Printf("[WS] 探针 %s 已连接\n", nodeID)

				go fetchAndSaveGeoIP(nodeID, clientIP)
			}

		now := time.Now().Unix()
		d := report.Data
		_, err := db.Exec(`
			INSERT INTO servers (
				node_id, name, region, cost, currency, billing_date, monthly_bw, bw_reset_day, notes,
				last_active, cpu, mem, mem_used, mem_total, disk, disk_used, disk_total,
				os, uptime, net_rx_speed, net_tx_speed, net_rx_total, net_tx_total,
				swap_used, swap_total, tcp_conn, udp_conn, kernel, arch, virt, cpu_model, processes, load_1, load_5, load_15,
				ipv4, ipv6, agent_version, status
			) VALUES (?,?,  'UN',0,'CNY','',0,1,'',
				?,?,?,?,?,?,?,?,
				?,?,?,?,?,?,
				?,?,?,?,?,?,?,?,?,?,?,?,
				?,?,?, 'online'
			)
			ON CONFLICT(node_id) DO UPDATE SET
				last_active=excluded.last_active, cpu=excluded.cpu, mem=excluded.mem,
				mem_used=excluded.mem_used, mem_total=excluded.mem_total,
				disk=excluded.disk, disk_used=excluded.disk_used, disk_total=excluded.disk_total,
				os=excluded.os, uptime=excluded.uptime,
				net_rx_speed=excluded.net_rx_speed, net_tx_speed=excluded.net_tx_speed,
				net_rx_total=excluded.net_rx_total, net_tx_total=excluded.net_tx_total,
				swap_used=excluded.swap_used, swap_total=excluded.swap_total,
				tcp_conn=excluded.tcp_conn, udp_conn=excluded.udp_conn,
				kernel=excluded.kernel, arch=excluded.arch, virt=excluded.virt,
				cpu_model=excluded.cpu_model, processes=excluded.processes,
				load_1=excluded.load_1, load_5=excluded.load_5, load_15=excluded.load_15,
				ipv4=excluded.ipv4, ipv6=excluded.ipv6, 
				agent_version=excluded.agent_version,
				status='online';
		`,
			report.NodeID, report.NodeID,
			now, d.CPU, d.Mem, d.MemUsed, d.MemTotal,
			d.Disk, d.DiskUsed, d.DiskTotal,
			d.OS, d.Uptime, d.NetRxSpeed, d.NetTxSpeed, d.NetRxTotal, d.NetTxTotal,
			d.SwapUsed, d.SwapTotal, d.TCPConn, d.UDPConn,
			d.Kernel, d.Arch, d.Virt, d.CPUModel,
			d.Processes, d.Load1, d.Load5, d.Load15,
			d.IPv4, d.IPv6, d.AgentVersion,
		)
		if err != nil {
			log.Println("[WS] 写入 servers 失败:", err)
		}

			// 写入 metrics 历史
			db.Exec(`INSERT INTO metrics (
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

			// 下发该节点的监测任务
			tasks := getTasksForNode(report.NodeID)
			sendRPCResult(conn, ac, req.ID, map[string]interface{}{
				"status": "ok",
				"tasks":  tasks,
			})

		// ── 探针回传任务结果 ──
		// ── 探针回传任务结果 ──
	case "task.result":
		var result AgentTaskResult
		if err := json.Unmarshal(req.Params, &result); err != nil {
			sendRPCError(conn, ac, req.ID, -32600, "invalid params")
			continue
		}
		// 统一使用 ping_ms 字段，并补充 status 字段（探针回传即表示在线）
		_, err := db.Exec(
			"INSERT INTO task_results (task_id, node_id, ping_ms, status, timestamp) VALUES (?,?,?,?,?)",
			result.TaskID, result.NodeID, result.PingMs, "online", time.Now().Unix(),
		)
		if err != nil {
			log.Println("[WS] 插入任务结果失败:", err)
		}
		sendRPCResult(conn, ac, req.ID, "ok")

		default:
			sendRPCError(conn, ac, req.ID, -32601, "method not found")
		}
	}
}

// ── 查询节点任务列表 ─────────────────────────────────────
// ── 查询节点任务列表 ─────────────────────────────────────
func getTasksForNode(nodeID string) []MonitorTask {
	rows, err := db.Query("SELECT id, target, type, interval, excluded_nodes FROM monitor_tasks")
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
			if excludedStr != "" {
				json.Unmarshal([]byte(excludedStr), &excludedNodes)
			}

			// 检查当前节点是否在排除名单中
			isExcluded := false
			for _, exID := range excludedNodes {
				if exID == nodeID {
					isExcluded = true
					break
				}
			}

			// 如果没被排除，则分配给该探针
			if !isExcluded {
				tasks = append(tasks, t)
			}
		}
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
