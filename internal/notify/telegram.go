package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type TelegramNotifier struct {
	Token    string
	ChatID   string
	Endpoint string
}

func (t *TelegramNotifier) Name() string {
	return "Telegram"
}

func (t *TelegramNotifier) Send(title, content string) error {
	endpoint := t.Endpoint
	if endpoint == "" {
		endpoint = "https://api.telegram.org/bot"
	}

	apiURL := fmt.Sprintf("%s%s/sendMessage", endpoint, t.Token)
	
	text := content
	if title != "" {
		text = fmt.Sprintf("<b>%s</b>\n\n%s", title, content)
	}

	payload := map[string]string{
		"chat_id":    t.ChatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	jsonData, _ := json.Marshal(payload)

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	return nil
}