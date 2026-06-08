package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	// "io/ioutil"
	"net/http"
	"time"
	"strings"
	"net"       // 新增
    "net/url"   // 新增
	"os"            // 新增
	"os/exec"       // 新增
	"path/filepath" // 新增
	"archive/zip"   // 新增
	"io"            // 新增

	"Float/internal/core"     // 新增这行
	"Float/internal/database"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"Float/internal/notify"
)

// 获取后台所有配置
func ApiGetSettingsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := database.DB.Query("SELECT key, value FROM settings")
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

// 🌟 修改：更新后台配置 (增加 2FA 验证码拦截逻辑 + 密码 bcrypt 哈希处理)
func ApiUpdateSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var newSettings map[string]string
	if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 🌟 拦截 2FA 验证码
	if code, ok := newSettings["tfa_code"]; ok && code != "" {
		var secret string
		// 直接取出数据库中暂存的 2FA 明文密钥
		database.DB.QueryRow("SELECT value FROM settings WHERE key = 'tfa_secret'").Scan(&secret)

		// 直接使用明文密钥进行 TOTP 校验
		if secret == "" || !totp.Validate(code, secret) {
			http.Error(w, "2FA 验证码错误或已失效", http.StatusBadRequest)
			return
		}
		// 校验通过：删除 map 中的 tfa_code
		delete(newSettings, "tfa_code")
	}

	// 🌟 拦截 admin_password：若前端传来新密码，则转为 bcrypt 哈希后再写入
	if newPass, exists := newSettings["admin_password"]; exists && newPass != "" {
		// 增加判断：如果已经是 bcrypt 的 hash（以 $2a$ 开头），就不再进行哈希
		if !strings.HasPrefix(newPass, "$2a$") {
			hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
			if err != nil {
				http.Error(w, "Failed to process password", http.StatusInternalServerError)
				return
			}
			newSettings["admin_password"] = string(hashedPassword)
		}
	}

	tx, _ := database.DB.Begin()
	stmt, _ := tx.Prepare("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)")
	defer stmt.Close()

	for k, v := range newSettings {
		stmt.Exec(k, v)
	}
	tx.Commit()

	// 记录系统日志
	database.DB.Exec("INSERT INTO logs (level, message, timestamp) VALUES (?, ?, ?)",
		"INFO", "管理员更新了系统站点配置", time.Now().Unix())

	w.WriteHeader(http.StatusOK)
}

// 🌟 新增：生成 2FA 密钥和二维码链接的接口
// 1. 生成 2FA 临时密钥 (不更改正式配置)
func ApiGenerateTFAHandler(w http.ResponseWriter, r *http.Request) {
    key, _ := totp.Generate(totp.GenerateOpts{
		Issuer:      "Float-Monitor",
		AccountName: "admin",
	})

    // 将生成的密钥暂时存入 settings 表的临时键中
    // 注意：此时不要修改 tfa_enabled 和 tfa_secret
    database.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('pending_tfa_secret', ?)", key.Secret())

    json.NewEncoder(w).Encode(map[string]string{
        "url": key.URL(),
    })
}

// 2. 验证并正式启用 2FA
func ApiVerifyAndEnableTFAHandler(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Code string `json:"code"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    var pendingSecret string
    database.DB.QueryRow("SELECT value FROM settings WHERE key = 'pending_tfa_secret'").Scan(&pendingSecret)

    if pendingSecret == "" {
        http.Error(w, "请先生成 2FA 二维码", http.StatusBadRequest)
        return
    }

    // 校验验证码是否正确
    if totp.Validate(req.Code, pendingSecret) {
        
        // 校验通过，正式启用
        tx, _ := database.DB.Begin()
        
        // 直接存入明文 pendingSecret
        tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tfa_secret', ?)", pendingSecret)
        
        tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tfa_enabled', 'true')")
        tx.Exec("DELETE FROM settings WHERE key = 'pending_tfa_secret'") // 清理临时密钥
        tx.Commit()
        
        json.NewEncoder(w).Encode(map[string]string{"status": "success"})
    } else {
        http.Error(w, "验证码错误，绑定失败", http.StatusUnauthorized)
    }
}

// 获取系统运行日志
func ApiGetLogsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := database.DB.Query("SELECT level, message, timestamp FROM logs ORDER BY timestamp DESC LIMIT 100")
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
	if logs == nil {
		logs = []map[string]interface{}{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

// 校验 URL 是否安全，阻断对内网及本地环回地址的请求 (防范 SSRF)
func isSafeURL(targetURL string) bool {
    parsedURL, err := url.Parse(targetURL)
    if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
        return false
    }

    hostname := parsedURL.Hostname()
    
    // 解析域名或直接读取 IP
    ips, err := net.LookupIP(hostname)
    if err != nil || len(ips) == 0 {
        return false
    }

    // 检查是否指向私有 IP、环回地址或链路本地地址
    for _, ip := range ips {
        if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
            return false
        }
    }
    return true
}

// [API] 发送测试通知
func ApiTestNotifyHandler(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Channel       string `json:"channel"`
		TgBotToken    string `json:"tg_bot_token"`
		TgChatID      string `json:"tg_chat_id"`
		TgApiEndpoint string `json:"tg_api_endpoint"`
		BarkURL       string `json:"bark_url"`
		BarkKey       string `json:"bark_key"`
		EmailHost     string `json:"email_smtp_host"`
		EmailPort     string `json:"email_smtp_port"`
		EmailUser     string `json:"email_username"`
		EmailPass     string `json:"email_password"`
		EmailFrom     string `json:"email_from"`
		EmailTo       string `json:"email_to"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	var err error

	switch payload.Channel {
	case "telegram":
		if payload.TgBotToken == "" || payload.TgChatID == "" {
			http.Error(w, "Missing token or chat_id", http.StatusBadRequest)
			return
		}
		
		endpoint := payload.TgApiEndpoint
        if endpoint == "" {
            endpoint = "https://api.telegram.org/bot"
        } else if !isSafeURL(endpoint) { // 🌟 新增：拦截非法的自定义代理端点
            http.Error(w, "非法的 API 端点地址，禁止访问内部网络", http.StatusForbidden)
            return
        }
		
		// 修正处：去掉两变量之间的斜杠
		apiURL := fmt.Sprintf("%s%s/sendMessage", endpoint, payload.TgBotToken)
		
		msgPayload := map[string]string{
			"chat_id":    payload.TgChatID,
			"text":       "🚀 恭喜，Telegram 接入测试成功！",
			"parse_mode": "HTML",
		}
		jsonData, _ := json.Marshal(msgPayload)
		resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			http.Error(w, "Telegram Request failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			http.Error(w, fmt.Sprintf("Telegram API Error (%d)", resp.StatusCode), http.StatusInternalServerError)
			return
		}

	case "bark":
		if payload.BarkKey == "" {
			http.Error(w, "Missing Bark Key", http.StatusBadRequest)
			return
		}

		// 🌟 新增：拦截非法的 Bark 自建服务器地址
        if payload.BarkURL != "" && !isSafeURL(payload.BarkURL) {
            http.Error(w, "非法的 Bark URL，禁止访问内部网络", http.StatusForbidden)
            return
        }
		
		notifier := &notify.BarkNotifier{
			URL: payload.BarkURL,
			Key: payload.BarkKey,
		}
		err = notifier.Send("Float 测试", "🚀 恭喜，Bark 接入测试成功！")

	case "email":
		if payload.EmailHost == "" || payload.EmailTo == "" {
			http.Error(w, "Missing Email Host or To Address", http.StatusBadRequest)
			return
		}
		notifier := &notify.EmailNotifier{
			Host: payload.EmailHost,
			Port: payload.EmailPort,
			User: payload.EmailUser,
			Pass: payload.EmailPass,
			From: payload.EmailFrom,
			To:   payload.EmailTo,
		}
		err = notifier.Send("Float 测试", "🚀 恭喜，Email 接入测试成功！")

	default:
		http.Error(w, "Unknown channel", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write([]byte(`{"status":"success"}`))
}
// ApiUpdateGeoIPDBHandler 手动更新 GeoIP 数据库接口
func ApiUpdateGeoIPDBHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		LicenseKey string `json:"license_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.LicenseKey == "" {
		// 如果前端没传，尝试从数据库读取
		database.DB.QueryRow("SELECT value FROM settings WHERE key = 'geoip_license_key'").Scan(&req.LicenseKey)
	}

	if req.LicenseKey == "" {
		http.Error(w, "缺少 MaxMind License Key", http.StatusBadRequest)
		return
	}

	// 异步或同步下载，由于下载较慢，此处采用同步并返回状态
	err := core.UpdateGeoIPDB(req.LicenseKey)
	if err != nil {
		http.Error(w, "更新数据库失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	database.InsertLog("INFO", "管理员成功更新了本地 GeoIP 数据库")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

// ApiTestGeoIPHandler 测试 GeoIP 解析接口
func ApiTestGeoIPHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		IP       string `json:"ip"`
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.IP == "" {
		http.Error(w, "IP 不能为空", http.StatusBadRequest)
		return
	}

	result, err := core.ParseIPLocation(req.IP, req.Provider)
if err != nil {
    http.Error(w, "解析出错: "+err.Error(), http.StatusInternalServerError)
    return
}

w.Header().Set("Content-Type", "application/json")
json.NewEncoder(w).Encode(map[string]interface{}{
    "status":       "success",
    "ip":           req.IP,
    "provider":     req.Provider,
    "country_code": result.CountryCode,
    "lat":          result.Lat,
    "lon":          result.Lon,
})
}

// 在文件末尾追加
type UpdateThemeReq struct {
	Theme string `json:"theme" binding:"required"`
}

// ApiUpdateThemeHandler 专门处理主题切换，增加安全检查
func ApiUpdateThemeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req UpdateThemeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 安全校验：防止目录穿越攻击
	if req.Theme == "" || strings.Contains(req.Theme, "..") || strings.Contains(req.Theme, "/") {
		http.Error(w, "非法的主题名称", http.StatusBadRequest)
		return
	}

	// 更新 settings 表中的 theme 记录
	_, err := database.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('theme', ?)", req.Theme)
	if err != nil {
		http.Error(w, "保存主题配置失败", http.StatusInternalServerError)
		return
	}

	// 记录日志
	database.InsertLog("INFO", fmt.Sprintf("管理员将前台主题切换为: %s", req.Theme))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message": "主题切换成功"}`))
}

type InstallThemeReq struct {
	URL string `json:"url"`
}

// ApiInstallGithubThemeHandler 从 GitHub 拉取主题到 themes 目录
func ApiInstallGithubThemeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req InstallThemeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.URL == "" || !strings.HasPrefix(req.URL, "https://github.com/") {
		http.Error(w, `{"message": "仅支持合法的 GitHub HTTPS 链接"}`, http.StatusBadRequest)
		return
	}

	parts := strings.Split(strings.TrimSuffix(req.URL, ".git"), "/")
	if len(parts) < 2 {
		http.Error(w, `{"message": "解析 GitHub URL 失败"}`, http.StatusBadRequest)
		return
	}
	repoName := parts[len(parts)-1]

	targetDir := filepath.Join("data", "themes", repoName)
	
	// 确保父目录存在并清理旧的同名主题残余
	os.MkdirAll(filepath.Join("data", "themes"), os.ModePerm)
	os.RemoveAll(targetDir)

	cmd := exec.Command("git", "clone", "--depth", "1", req.URL, targetDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		database.InsertLog("ERROR", fmt.Sprintf("拉取主题失败: %s, %s", req.URL, stderr.String()))
		http.Error(w, fmt.Sprintf(`{"message": "Git 克隆失败: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	os.RemoveAll(filepath.Join(targetDir, ".git"))
	database.InsertLog("INFO", fmt.Sprintf("管理员从 GitHub 成功拉取新主题: %s", repoName))
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "success", "message": "主题拉取成功"}`))
}

// ApiGetLocalThemesHandler 获取已安装的主题列表
func ApiGetLocalThemesHandler(w http.ResponseWriter, r *http.Request) {
	themesDir := filepath.Join("data", "themes")
	entries, err := os.ReadDir(themesDir)
	
	themes := []map[string]string{}
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				configPath := filepath.Join(themesDir, entry.Name(), "theme.json")
				configData, err := os.ReadFile(configPath)
				themeInfo := map[string]string{
					"id": entry.Name(),
					"name": entry.Name(),
				}
				if err == nil {
					var parsed map[string]interface{}
					if json.Unmarshal(configData, &parsed) == nil {
						if name, ok := parsed["name"].(string); ok { themeInfo["name"] = name }
						if version, ok := parsed["version"].(string); ok { themeInfo["version"] = version }
						if author, ok := parsed["author"].(string); ok { themeInfo["author"] = author }
						if desc, ok := parsed["description"].(string); ok { themeInfo["description"] = desc }
					}
				}
				themes = append(themes, themeInfo)
			}
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(themes)
}

// ApiUploadZipThemeHandler 处理 ZIP 主题上传与解压
func ApiUploadZipThemeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 限制上传大小为 50MB
	r.ParseMultipartForm(50 << 20)
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "获取上传文件失败", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		http.Error(w, "仅支持 .zip 格式的主题包", http.StatusBadRequest)
		return
	}

	// 提取不带后缀的文件名作为主题目录名
	themeName := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	targetDir := filepath.Join("data", "themes", themeName)

	// 保存为临时文件
	tempZipPath := filepath.Join(os.TempDir(), header.Filename)
	tempFile, err := os.Create(tempZipPath)
	if err != nil {
		http.Error(w, "创建临时文件失败", http.StatusInternalServerError)
		return
	}
	io.Copy(tempFile, file)
	tempFile.Close()
	defer os.Remove(tempZipPath) // 执行完毕后自动清理临时压缩包

	// 清理旧的同名主题目录并创建新目录
	os.RemoveAll(targetDir)
	os.MkdirAll(targetDir, os.ModePerm)

	zipReader, err := zip.OpenReader(tempZipPath)
	if err != nil {
		http.Error(w, "读取 ZIP 压缩包失败", http.StatusInternalServerError)
		return
	}
	defer zipReader.Close()

	// 智能判断：如果 ZIP 内部全部被包裹在一个顶层文件夹下（例如 GitHub 默认的 repo-main），则剥离该层级
	var topLevelDir string
	hasSingleTopLevel := true
	for i, f := range zipReader.File {
		if strings.HasPrefix(f.Name, "__MACOSX") { continue }
		parts := strings.Split(strings.Trim(f.Name, "/"), "/")
		if i == 0 {
			topLevelDir = parts[0]
		} else if len(parts) > 0 && parts[0] != topLevelDir {
			hasSingleTopLevel = false
			break
		}
	}

	for _, f := range zipReader.File {
		// 忽略 macOS 的缓存文件
		if strings.HasPrefix(f.Name, "__MACOSX") { continue }

		relPath := f.Name
		if hasSingleTopLevel && strings.HasPrefix(relPath, topLevelDir+"/") {
			relPath = strings.TrimPrefix(relPath, topLevelDir+"/")
		}
		if relPath == "" || relPath == topLevelDir { continue }

		fpath := filepath.Join(targetDir, relPath)

		// 防御 Zip Slip 目录穿越漏洞
		if !strings.HasPrefix(fpath, filepath.Clean(targetDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil { continue }

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil { continue }

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			continue
		}

		io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
	}

	database.InsertLog("INFO", fmt.Sprintf("管理员通过 ZIP 上传安装了新主题: %s", themeName))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "success", "message": "ZIP 主题解压并安装成功"}`))
}