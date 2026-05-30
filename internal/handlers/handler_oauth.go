package handlers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"Float/internal/database"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// 获取动态的 OAuth 配置
func getGithubOAuthConfig(r *http.Request) *oauth2.Config {
	var clientID, clientSecret string
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'oauth_github_client_id'").Scan(&clientID)
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'oauth_github_client_secret'").Scan(&clientSecret)

	// 动态推导回调地址，适配反向代理
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	redirectURL := fmt.Sprintf("%s://%s/api/auth/github/callback", scheme, r.Host)

	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     github.Endpoint,
		RedirectURL:  redirectURL,
		Scopes:       []string{"read:user"},
	}
}

// 1. 发起 GitHub 登录请求
func OAuthGithubLoginHandler(w http.ResponseWriter, r *http.Request) {
	config := getGithubOAuthConfig(r)
	if config.ClientID == "" {
		http.Redirect(w, r, "/oauth/callback?error=未配置GitHub_Client_ID", http.StatusTemporaryRedirect)
		return
	}

	// 生成随机 State 防止 CSRF（生产环境建议存入 Cookie 校验，此处简化演示）
	state := "random-state-string"
	url := config.AuthCodeURL(state, oauth2.AccessTypeOnline)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// 2. 接收 GitHub 回调
func OAuthGithubCallbackHandler(w http.ResponseWriter, r *http.Request) {
	// 校验错误
	if err := r.FormValue("error"); err != "" {
		http.Redirect(w, r, fmt.Sprintf("/oauth/callback?error=%s", err), http.StatusTemporaryRedirect)
		return
	}

	code := r.FormValue("code")
	config := getGithubOAuthConfig(r)

	// 使用 Code 换取 Access Token
	token, err := config.Exchange(context.Background(), code)
	if err != nil {
		http.Redirect(w, r, "/oauth/callback?error=Token交换失败", http.StatusTemporaryRedirect)
		return
	}

	// 使用 Token 获取 GitHub 用户信息
	client := config.Client(context.Background(), token)
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		http.Redirect(w, r, "/oauth/callback?error=获取用户信息失败", http.StatusTemporaryRedirect)
		return
	}
	defer resp.Body.Close()

	var ghUser struct {
		Login string `json:"login"` // GitHub 用户名
	}
	json.NewDecoder(resp.Body).Decode(&ghUser)

	// 3. 白名单校验
	var whitelistStr string
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'oauth_github_whitelist'").Scan(&whitelistStr)
	
	isAllowed := false
	allowedUsers := strings.Split(whitelistStr, ",")
	for _, u := range allowedUsers {
		if strings.TrimSpace(u) == ghUser.Login && ghUser.Login != "" {
			isAllowed = true
			break
		}
	}

	if !isAllowed {
		http.Redirect(w, r, fmt.Sprintf("/oauth/callback?error=账号 %s 不在允许名单内", ghUser.Login), http.StatusTemporaryRedirect)
		return
	}

	// 4. 鉴权通过，签发本地管理员 Token
	var adminSessionToken string
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'admin_session_token'").Scan(&adminSessionToken)

	// 如果没有 Token，生成一个新的
	if adminSessionToken == "" {
		b := make([]byte, 24)
		rand.Read(b)
		adminSessionToken = fmt.Sprintf("%x", b)
		database.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('admin_session_token', ?)", adminSessionToken)
	}

	// 携带签发的 Token 重定向回前端中转页
	http.Redirect(w, r, fmt.Sprintf("/oauth/callback?token=%s", adminSessionToken), http.StatusTemporaryRedirect)
}