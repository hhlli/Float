package notify

import (
	"fmt"
	"strings"

	"Float/internal/database"
)

type Notifier interface {
	Send(title, content string) error
	Name() string
}

func Dispatch(title, content string) {
	settings := getSettings(
		"tg_bot_token", "tg_chat_id", "tg_api_endpoint",
		"bark_url", "bark_key",
		"email_smtp_host", "email_smtp_port", "email_username", "email_password", "email_from", "email_to",
	)

	var notifiers []Notifier

	if settings["tg_bot_token"] != "" && settings["tg_chat_id"] != "" {
		notifiers = append(notifiers, &TelegramNotifier{
			Token:    settings["tg_bot_token"],
			ChatID:   settings["tg_chat_id"],
			Endpoint: settings["tg_api_endpoint"],
		})
	}

	if settings["bark_key"] != "" {
		notifiers = append(notifiers, &BarkNotifier{
			URL: settings["bark_url"],
			Key: settings["bark_key"],
		})
	}

	if settings["email_smtp_host"] != "" && settings["email_to"] != "" {
		notifiers = append(notifiers, &EmailNotifier{
			Host: settings["email_smtp_host"],
			Port: settings["email_smtp_port"],
			User: settings["email_username"],
			Pass: settings["email_password"],
			From: settings["email_from"],
			To:   settings["email_to"],
		})
	}

	for _, n := range notifiers {
		go func(notifier Notifier) {
			err := notifier.Send(title, content)
			if err != nil {
				database.InsertLog("ERROR", fmt.Sprintf("%s 推送失败: %v", notifier.Name(), err))
			} else {
				database.InsertLog("INFO", fmt.Sprintf("%s 告警推送成功", notifier.Name()))
			}
		}(n)
	}
}

func getSettings(keys ...string) map[string]string {
	settings := make(map[string]string)
	if len(keys) == 0 {
		return settings
	}

	placeholders := make([]string, len(keys))
	args := make([]interface{}, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}

	query := fmt.Sprintf("SELECT key, value FROM settings WHERE key IN (%s)", strings.Join(placeholders, ","))
	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return settings
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			settings[k] = v
		}
	}
	return settings
}