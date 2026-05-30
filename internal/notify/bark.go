package notify

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type BarkNotifier struct {
	URL string
	Key string
}

func (b *BarkNotifier) Name() string {
	return "Bark"
}

func (b *BarkNotifier) Send(title, content string) error {
	barkURL := b.URL
	if barkURL == "" {
		barkURL = "https://api.day.app"
	}
	barkURL = strings.TrimRight(barkURL, "/")

	cleanText := strings.ReplaceAll(content, "<b>", "")
	cleanText = strings.ReplaceAll(cleanText, "</b>", "")

	if title == "" {
		title = "Float 监控告警"
	}

	apiURL := fmt.Sprintf("%s/%s/%s/%s", barkURL, b.Key, url.PathEscape(title), url.PathEscape(cleanText))

	resp, err := http.Get(apiURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	return nil
}