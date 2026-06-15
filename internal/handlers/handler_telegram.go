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
	UpdateID      int              `json:"update_id"`
	Message       *tgMessage       `json:"message,omitempty"`
	CallbackQuery *tgCallbackQuery `json:"callback_query,omitempty"` // 新增：用于接收按钮点击事件
}

type tgMessage struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	Chat      tgChat `json:"chat"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

// 新增：回调查询的基础结构
type tgCallbackQuery struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"` // 按钮依附的原消息
	Data    string     `json:"data"`    // 按钮中绑定的回调动作标识
}

type tgUser struct {
	ID int64 `json:"id"`
}

// WebhookBindRequest 用于接收前端的绑定/解绑请求
type WebhookBindRequest struct {
	Action string `json:"action"` // bind 或 unbind
	Domain string `json:"domain"` // 当前公网域名
}

// ── 1. Webhook 接收端逻辑 (数据面) ──────────────────────────────────────────

// ApiTelegramWebhookHandler 接收来自 Telegram 的推送
func ApiTelegramWebhookHandler(w http.ResponseWriter, r *http.Request) {
	// 无论发生什么，都向 Telegram 返回 200 OK，防止其不断重试
	defer w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodPost {
		return
	}

	var update tgUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		logger.Log.Error("解析 Telegram Webhook 失败", zap.Error(err))
		return
	}

	// ==========================================
    // 1. 读取系统配置并执行回退逻辑
    // ==========================================
    var token, webhookToken, notifyToken string
    var allowedChatIDStr, webhookChatID, notifyChatID string

    // 读取专属 Webhook 配置
    database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_webhook_token'").Scan(&webhookToken)
    database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_webhook_chat_id'").Scan(&webhookChatID)

    // 读取全局通知配置作为回退
    database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_bot_token'").Scan(&notifyToken)
    database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tg_chat_id'").Scan(&notifyChatID)

    // 执行凭证回退判定
    if webhookToken != "" {
        token = webhookToken
    } else {
        token = notifyToken
    }

    if webhookChatID != "" {
        allowedChatIDStr = webhookChatID
    } else {
        allowedChatIDStr = notifyChatID
    }

    // 核心拦截：如果没有任何有效凭证，放弃执行
    if token == "" || allowedChatIDStr == "" {
        logger.Log.Error("Telegram Webhook 触发，但缺少有效的 Token 或 Chat ID")
        return
    }

	// ==========================================
	// 2. 路由分发：处理纯文本指令 (如 /status)
	// ==========================================
	if update.Message != nil && update.Message.Text != "" {
		incomingChatID := fmt.Sprintf("%d", update.Message.Chat.ID)
		
		// 鉴权：拦截非白名单用户的请求
		if incomingChatID != allowedChatIDStr {
			logger.Log.Warn("拦截到未授权的文本指令", zap.String("chat_id", incomingChatID))
			return
		}

		text := strings.TrimSpace(update.Message.Text)
		if text == "/status" || strings.HasPrefix(text, "/status") {
			// 传入 0 表示这是一条全新指令，系统将发送一条带有内联按钮的新消息
			replyServerStatus(token, incomingChatID, 0)
		}
		return
	}

	// ==========================================
	// 3. 路由分发：处理内联按钮点击事件 (Callback Query)
	// ==========================================
	if update.CallbackQuery != nil {
		incomingChatID := fmt.Sprintf("%d", update.CallbackQuery.From.ID)
		
		// 鉴权：拦截非白名单用户的按钮点击
		if incomingChatID != allowedChatIDStr {
			logger.Log.Warn("拦截到未授权的按钮点击", zap.String("chat_id", incomingChatID))
			return
		}

		actionData := update.CallbackQuery.Data
		msgID := update.CallbackQuery.Message.MessageID // 获取被点击按钮所在的历史消息 ID

		switch actionData {
		case "action_refresh_status":
			// 传入 msgID，触发 Telegram 的 editMessageText API 就地修改该条消息的内容
			replyServerStatus(token, incomingChatID, msgID)
			
			// 向 Telegram 回复确认信号，消除客户端按钮上的加载动画，并触发顶部轻提示
			answerCallbackQuery(token, update.CallbackQuery.ID, "状态已刷新")
		}
		return
	}
}

func replyServerStatus(token, chatID string, editMessageID int) {
	rows, err := database.DB.Query("SELECT name, cpu, mem, net_rx_speed, net_tx_speed FROM servers WHERE status = 'online'")
	if err != nil {
		// 错误提示也保持复用现有逻辑
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

	text := strings.Join(lines, "\n")

	// 构建带有内联键盘的 Payload
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
		"reply_markup": map[string]interface{}{
			"inline_keyboard": [][]map[string]interface{}{
				{
					{"text": "🔄 刷新状态", "callback_data": "action_refresh_status"},
				},
			},
		},
	}

	var apiURL string
	if editMessageID == 0 {
		// 分支 A：发送全新消息
		apiURL = fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	} else {
		// 分支 B：就地编辑原有消息（防止刷屏）
		apiURL = fmt.Sprintf("https://api.telegram.org/bot%s/editMessageText", token)
		payload["message_id"] = editMessageID
	}

	jsonData, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
}

// answerCallbackQuery 用于向 Telegram 返回确认信号，消除按钮加载状态
func answerCallbackQuery(token, callbackQueryID, alertText string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
	payload := map[string]interface{}{
		"callback_query_id": callbackQueryID,
		"text":              alertText, // 客户端顶部会弹出 "状态已刷新" 的轻提示
	}
	jsonData, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
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