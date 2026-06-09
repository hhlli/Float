package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"Float/internal/database"
	"Float/internal/logger"
	"go.uber.org/zap"
)

// ── 数据结构定义 ────────────────────────────────────────────────────────────

type tgUpdate struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	Chat      tgChat `json:"chat"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type WebhookBindRequest struct {
	Domain string `json:"domain"`
	Action string `json:"action"` // "bind" 或 "unbind"
}

// ── 1. Webhook 接收端逻辑 (数据面) ──────────────────────────────────────────

// ApiTelegramWebhookHandler 接收来自 Telegram 的推送
func ApiTelegramWebhookHandler(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodPost {
		return
	}

	var update tgUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		logger.Log.Error("解析 Telegram Webhook 失败", zap.Error(err))
		return
	}

	if update.Message == nil || update.Message.Text == "" {
		return
	}

	var token, allowedChatIDStr string
	var notifyToken, notifyChatID string

	// 读取 Webhook 专属凭证
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_webhook_token'").Scan(&token)
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_webhook_chat_id'").Scan(&allowedChatIDStr)

	// 读取通知凭证作为回退
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_bot_token'").Scan(&notifyToken)
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_chat_id'").Scan(&notifyChatID)

	// 回退逻辑
	if token == "" {
		token = notifyToken
	}
	if allowedChatIDStr == "" {
		allowedChatIDStr = notifyChatID
	}

	if token == "" || allowedChatIDStr == "" {
		return
	}

	incomingChatID := fmt.Sprintf("%d", update.Message.Chat.ID)
	if incomingChatID != allowedChatIDStr {
		logger.Log.Warn("拦截到未授权的 Telegram 访问", zap.String("chat_id", incomingChatID))
		return
	}

	text := strings.TrimSpace(update.Message.Text)

	switch {
	case strings.HasPrefix(text, "/status"):
		replyServerStatus(token, incomingChatID)
	}
}

func replyServerStatus(token, chatID string) {
	rows, err := database.DB.Query("SELECT name, cpu, mem, net_rx_speed, net_tx_speed FROM servers WHERE status = 'online'")
	if err != nil {
		sendTelegramMsg(token, chatID, "❌ 数据库查询失败")
		return
	}
	defer rows.Close()

	var lines []string
	lines = append(lines, "📊 <b>在线服务器实时状态</b>\n")
	
	count := 0
	for rows.Next() {
		var name string
		var cpu, mem, rx, tx float64
		if err := rows.Scan(&name, &cpu, &mem, &rx, &tx); err == nil {
			count++
			lines = append(lines, fmt.Sprintf("🖥 <b>%s</b>", name))
			lines = append(lines, fmt.Sprintf("├ CPU: %.1f%% | 内存: %.1f%%", cpu, mem))
			lines = append(lines, fmt.Sprintf("└ ↓ %.2f MB/s | ↑ %.2f MB/s\n", rx/1024/1024, tx/1024/1024))
		}
	}

	if count == 0 {
		lines = append(lines, "当前无在线服务器。")
	}

	sendTelegramMsg(token, chatID, strings.Join(lines, "\n"))
}

func sendTelegramMsg(token, chatID, text string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	jsonData, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
}

// ── 2. Webhook 后台管理逻辑 (控制面) ────────────────────────────────────────

// ApiManageTelegramWebhookHandler 处理前端发起的绑定/解绑请求
func ApiManageTelegramWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req WebhookBindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	var token, notifyToken string

	// 读取 Webhook 专属凭证
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_webhook_token'").Scan(&token)
	
	// 读取通知凭证作为回退
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_bot_token'").Scan(&notifyToken)

	// 回退逻辑
	if token == "" {
		token = notifyToken
	}

	if token == "" {
		http.Error(w, "请先配置 Telegram Bot Token", http.StatusBadRequest)
		return
	}

	var tgAPI string
	if req.Action == "bind" {
		if req.Domain == "" {
			http.Error(w, "绑定 Webhook 需要提供公网域名", http.StatusBadRequest)
			return
		}
		webhookURL := fmt.Sprintf("%s/api/telegram/webhook", req.Domain)
		tgAPI = fmt.Sprintf("https://api.telegram.org/bot%s/setWebhook?url=%s", token, url.QueryEscape(webhookURL))
	} else if req.Action == "unbind" {
		tgAPI = fmt.Sprintf("https://api.telegram.org/bot%s/deleteWebhook", token)
	} else {
		http.Error(w, "未知操作", http.StatusBadRequest)
		return
	}

	resp, err := http.Get(tgAPI)
	if err != nil {
		http.Error(w, fmt.Sprintf("请求 Telegram 失败: %v", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Telegram 拒绝了请求，状态码: %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"success"}`))
}