package handlers

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"strings"
	"net"

	"Float/internal/database"
	"github.com/pquerna/otp/totp" // 🌟 引入 TOTP 库
	"golang.org/x/crypto/bcrypt"
	"Float/internal/core"
	"Float/internal/logger"
	"go.uber.org/zap"
)

// 标准化 IP 提取，兼容 IPv4 / IPv6
// 🌟 修复：安全的 IP 提取逻辑，防止伪造 IP 绕过限流记录
func getClientIP(r *http.Request) string {
	// 1. 提取物理直连 IP
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return remoteIP
	}

	// 2. 仅当直连来源是本地/内网代理时，信任 HTTP 头部
	if ip.IsLoopback() || ip.IsPrivate() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ips := strings.Split(xff, ",")
			if len(ips) > 0 {
				return strings.TrimSpace(ips[0])
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	// 3. 公网直连，强制使用真实物理 IP
	if remoteIP == "::1" {
		return "127.0.0.1"
	}
	return remoteIP
}
// 🌟 抽离：统一的 Token 签发逻辑，供密码登录和 OAuth 登录共同使用
func generateAdminToken(r *http.Request) string {
    b := make([]byte, 32)
    rand.Read(b)
    token := fmt.Sprintf("%x", b)
    
    // 调用新增加的 getClientIP 函数提取 IP
    ip := getClientIP(r)
    
    ua := r.UserAgent()
    if len(ua) > 255 {
        ua = ua[:255]
    }
    now := time.Now().Unix()

    // 强制打印终端日志进行排查
    logger.Log.Debug("会话记录测试",
    zap.String("module", "Session"),
    zap.String("ip", ip),
    zap.String("ua", ua),
)

    // 补全 SQL 插入字段
    _, err := database.DB.Exec("INSERT INTO sessions (token, created_at, ip, user_agent, last_active) VALUES (?, ?, ?, ?, ?)", 
        token, now, ip, ua, now)
		if err != nil {
			logger.Log.Error("Session 存储失败",
				zap.String("module", "Session"),
				zap.Error(err),
			)
		}
    return token
}

// 管理员登录：集成 2FA 校验
func ApiLoginHandler(w http.ResponseWriter, r *http.Request) {
	// 1. 前置拦截：检查 IP 是否已被封禁
	locked, waitTime := core.IsIPLocked(r)
	if locked {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "error",
			"message": fmt.Sprintf("尝试次数过多，请在 %d 分钟后重试", int(waitTime.Minutes())+1),
		})
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"` // 🌟 新增：接收前端传来的 2FA 验证码
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}

	var adminUser, adminPassHash string
	// 从 settings 获取管理员账号、密码哈希
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'admin_username'").Scan(&adminUser)
	database.DB.QueryRow("SELECT value FROM settings WHERE key = 'admin_password'").Scan(&adminPassHash)

	// 2. 第一层：校验用户名，并使用 bcrypt 比对哈希密码
	err := bcrypt.CompareHashAndPassword([]byte(adminPassHash), []byte(creds.Password))
	if creds.Username == adminUser && err == nil {

		var tfaEnabled, tfaSecret string
		database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tfa_enabled'").Scan(&tfaEnabled)
		database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tfa_secret'").Scan(&tfaSecret) // 此时获取到的是密文

		if tfaEnabled == "true" {
			if creds.Code == "" {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  "need_2fa",
					"message": "请输入双重身份验证码",
				})
				return
			}

			// 验证 2FA 验证码（直接使用数据库中取出的明文 tfaSecret）
			if !totp.Validate(creds.Code, tfaSecret) {
				core.RecordFailedLogin(r)
				http.Error(w, "双重验证码错误", http.StatusUnauthorized)
				return
			}
		}

		// 4. 校验全部通过：清除该 IP 的失败记录，签发 Token
		core.ClearLoginAttempts(r)

		adminSessionToken := generateAdminToken(r)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "success",
			"token":  adminSessionToken,
		})
	} else {
		// 5. 用户名或密码错误，记录失败次数
		core.RecordFailedLogin(r)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
}

type SessionInfo struct {
	Token      string `json:"token"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  int64  `json:"created_at"`
	LastActive int64  `json:"last_active"`
	IsCurrent  bool   `json:"is_current"`
}

// ApiGetSessionsHandler 获取所有活动的登录会话
func ApiGetSessionsHandler(w http.ResponseWriter, r *http.Request) {
	// 从请求头获取当前会话 token，用于标记当前设备
	authHeader := r.Header.Get("Authorization")
    currentToken := strings.TrimPrefix(authHeader, "Bearer ")

	rows, err := database.DB.Query("SELECT token, ip, user_agent, created_at, last_active FROM sessions ORDER BY last_active DESC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var s SessionInfo
		rows.Scan(&s.Token, &s.IP, &s.UserAgent, &s.CreatedAt, &s.LastActive)
		
		s.IsCurrent = (s.Token == currentToken)
		
		// 截断完整 token，仅返回前6后4用于前端识别，避免完整凭证泄露
		if len(s.Token) > 10 {
			s.Token = s.Token[:6] + "******" + s.Token[len(s.Token)-4:]
		}
		
		sessions = append(sessions, s)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// ApiRevokeSessionHandler 踢出指定会话
func ApiRevokeSessionHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 执行删除匹配 token 的会话
	// 实际业务中前端传来的 Token 需与数据库匹配，如果前端拿到的是脱敏 token，应通过其他唯一标识(如自增ID)删除。
	// 若使用原生 token 匹配，确保前端发送未脱敏的标识符。
	_, err := database.DB.Exec("DELETE FROM sessions WHERE token LIKE ?", req.Token[:6]+"%")
	if err != nil {
		http.Error(w, "删除会话失败", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}