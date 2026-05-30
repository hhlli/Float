package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
	"Float/internal/core"

	"Float/internal/database"
	"github.com/gorilla/websocket"
	"Float/internal/logger"
	"go.uber.org/zap"
)

// TerminalSession 表示一个完整的终端会话
type TerminalSession struct {
	ID        string
	NodeID    string
	FrontConn *websocket.Conn
	AgentConn *websocket.Conn

	agentReady chan struct{}
	closeOnce  sync.Once
}

var (
	termSessions   = make(map[string]*TerminalSession)
	termSessionsMu sync.RWMutex
)

// generateSessionID 生成随机的 16 字节十六进制字符串
func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ── 1. 供 Web 前端连接的 WebSocket 接口 ────────────────────
func WsTerminalFrontendHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	ticket := r.URL.Query().Get("ticket") // 原先为 token，现改为 ticket

	if nodeID == "" {
		http.Error(w, "Missing node_id", http.StatusBadRequest)
		return
	}

	// 使用一次性 Ticket 鉴权，替代原先的 admin_session_token 比对
	if ticket == "" || !core.ValidateAndConsumeTicket(ticket, nodeID) {
		http.Error(w, "Unauthorized: Invalid or Expired Ticket", http.StatusUnauthorized)
		return
	}

	frontConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Log.Error("前端 WS 升级失败", 
			zap.String("module", "Terminal"), 
			zap.Error(err),
		)
		return
	}

	sessionID := generateSessionID()
	session := &TerminalSession{
		ID:         sessionID,
		NodeID:     nodeID,
		FrontConn:  frontConn,
		agentReady: make(chan struct{}),
	}

	termSessionsMu.Lock()
	termSessions[sessionID] = session
	termSessionsMu.Unlock()

	// 退出时统一清理连接
	defer cleanupSession(sessionID)

	if !RequestAgentTerminal(nodeID, sessionID) {
		frontConn.WriteMessage(websocket.TextMessage, []byte("\r\n[Error] 探针未在线或拒绝指令\r\n"))
		return
	}

	frontConn.WriteMessage(websocket.TextMessage, []byte("\r\n[Info] 正在连接目标服务器...\r\n"))

	select {
	case <-session.agentReady:
		// 探针反连成功
	case <-time.After(10 * time.Second):
		frontConn.WriteMessage(websocket.TextMessage, []byte("\r\n[Error] 探针连接超时\r\n"))
		return
	}

	frontConn.WriteMessage(websocket.TextMessage, []byte("\r\n[Info] 终端连接成功\r\n"))

	// 🌟 修复：此循环仅负责 【读前端输入 -> 写给探针】
	for {
		msgType, p, err := frontConn.ReadMessage()
		if err != nil {
			break
		}
		if session.AgentConn != nil {
			session.AgentConn.WriteMessage(msgType, p)
		}
	}
}

// ── 2. 供探针反向连接的 WebSocket 接口 ────────────────────
func WsTerminalAgentHandler(w http.ResponseWriter, r *http.Request) {
    sessionID := r.URL.Query().Get("session_id")
    token := r.URL.Query().Get("token")

    // 1. 先获取会话信息
    termSessionsMu.RLock()
    session, ok := termSessions[sessionID]
    termSessionsMu.RUnlock()

    if !ok {
        http.Error(w, "Session not found", http.StatusNotFound)
        return
    }

    // 2. 🌟 修复：查询此 Token 绑定的真实 node_id，防止横向越权
    var dbNodeID string
    err := database.DB.QueryRow("SELECT node_id FROM servers WHERE auth_token = ?", token).Scan(&dbNodeID)
    if err != nil {
        // 查不到说明 Token 无效
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }

    // 3. 🌟 修复：比对 Token 绑定的节点与当前会话请求的节点是否一致
    if dbNodeID != session.NodeID {
        http.Error(w, "Forbidden: Node mismatch", http.StatusForbidden)
        return
    }

    // 4. 鉴权通过后升级连接
    agentConn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
		logger.Log.Error("探针反连 WS 升级失败", 
			zap.String("module", "Terminal"), 
			zap.Error(err),
		)
		return
	}

    session.AgentConn = agentConn
    close(session.agentReady) // 唤醒前端

    // 退出时统一清理连接
    defer cleanupSession(sessionID)

    // 此循环仅负责 【读探针屏幕流 -> 写给前端】
    for {
        msgType, p, err := agentConn.ReadMessage()
        if err != nil {
            break
        }
        if session.FrontConn != nil {
            session.FrontConn.WriteMessage(msgType, p)
        }
    }
}

// 清理函数：确保双端连接同时释放
func cleanupSession(sessionID string) {
	termSessionsMu.Lock()
	session, ok := termSessions[sessionID]
	if ok {
		delete(termSessions, sessionID)
	}
	termSessionsMu.Unlock()

	if ok {
		session.closeOnce.Do(func() {
			if session.FrontConn != nil {
				session.FrontConn.Close()
			}
			if session.AgentConn != nil {
				session.AgentConn.Close()
			}
		})
	}
}