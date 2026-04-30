package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"
)

// 获取后台所有配置
func apiGetSettingsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT key, value FROM settings")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		settings[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// 更新后台配置 (采用 INSERT OR REPLACE 兼容新字段)
func apiUpdateSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var newSettings map[string]string
	if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	tx, _ := db.Begin()
	stmt, _ := tx.Prepare("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)")
	defer stmt.Close()

	for k, v := range newSettings {
		stmt.Exec(k, v)
	}
	tx.Commit()

	// 记录系统日志
	db.Exec("INSERT INTO logs (level, message, timestamp) VALUES (?, ?, ?)", 
		"INFO", "管理员更新了系统站点配置", time.Now().Unix())

	w.WriteHeader(http.StatusOK)
}

// 获取系统运行日志
func apiGetLogsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT level, message, timestamp FROM logs ORDER BY timestamp DESC LIMIT 100")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var logs []map[string]interface{}
	for rows.Next() {
		var level, message string
		var ts int64
		rows.Scan(&level, &message, &ts)
		logs = append(logs, map[string]interface{}{
			"level":     level,
			"message":   message,
			"timestamp": ts,
		})
	}
	if logs == nil { logs = []map[string]interface{}{} }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

// [API] 发送测试通知
func apiTestNotifyHandler(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		TgBotToken    string `json:"tg_bot_token"`
		TgChatID      string `json:"tg_chat_id"`
		TgApiEndpoint string `json:"tg_api_endpoint"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if payload.TgBotToken == "" || payload.TgChatID == "" {
		http.Error(w, "Missing token or chat_id", http.StatusBadRequest)
		return
	}

	endpoint := payload.TgApiEndpoint
	if endpoint == "" {
		endpoint = "https://api.telegram.org/bot"
	}

	apiURL := fmt.Sprintf("%s%s/sendMessage", endpoint, payload.TgBotToken)
	msgPayload := map[string]string{
		"chat_id": payload.TgChatID,
		"text":    "🚀 测试消息：您的 Monitor Agent 通知配置成功！",
	}
	jsonData, _ := json.Marshal(msgPayload)

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		http.Error(w, "Request failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("Telegram API Error (%d): %s", resp.StatusCode, string(bodyBytes)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}