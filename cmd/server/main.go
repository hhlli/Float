package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"runtime/debug"

	"Float/internal/core"
	"Float/internal/database"
	"Float/internal/handlers"
	"Float/internal/logger"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	
	"github.com/spf13/cobra" // 🌟 新增：引入命令行框架
)

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseRecorder) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		defer func() {
			if err := recover(); err != nil {
				logger.Log.Error("API 崩溃",
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.Any("error", err),
					zap.ByteString("stack", debug.Stack()),
				)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		next(recorder, r)

		duration := time.Since(start)
		logger.Log.Info("API Request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", recorder.statusCode),
			zap.Duration("duration", duration),
		)
	}
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			http.Error(w, "Invalid Authorization format", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, prefix)

		var exists bool
		err := database.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM sessions WHERE token = ?)", token).Scan(&exists)

		if err != nil || !exists {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
        
		_, err = database.DB.Exec("UPDATE sessions SET last_active = ? WHERE token = ?", time.Now().Unix(), token)
		if err != nil {
			logger.Log.Error("更新会话活跃时间失败", 
				zap.String("module", "DB"), 
				zap.Error(err),
			)
		}

		next.ServeHTTP(w, r)
	}
}

func migrate() {
	queries := []string{
		`ALTER TABLE servers ADD COLUMN auth_token TEXT DEFAULT '';`,
		`ALTER TABLE servers ADD COLUMN swap_used REAL DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN swap_total REAL DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN tcp_conn INTEGER DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN udp_conn INTEGER DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN kernel TEXT DEFAULT '';`,
		`ALTER TABLE servers ADD COLUMN arch TEXT DEFAULT '';`,
		`ALTER TABLE servers ADD COLUMN virt TEXT DEFAULT '';`,
		`ALTER TABLE servers ADD COLUMN cpu_model TEXT DEFAULT '';`,
		`ALTER TABLE servers ADD COLUMN processes INTEGER DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN load_1 REAL DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN load_5 REAL DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN load_15 REAL DEFAULT 0;`,
		`ALTER TABLE servers ADD COLUMN agent_version TEXT DEFAULT '';`,
		`ALTER TABLE metrics ADD COLUMN net_rx_speed REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN net_tx_speed REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN net_rx_total REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN net_tx_total REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN tcp_conn INTEGER DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN udp_conn INTEGER DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN processes INTEGER DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN load_1 REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN load_5 REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN load_15 REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN swap_used REAL DEFAULT 0;`,
		`ALTER TABLE metrics ADD COLUMN swap_total REAL DEFAULT 0;`,
		`ALTER TABLE monitor_tasks ADD COLUMN type TEXT DEFAULT 'TCP';`,
		`ALTER TABLE monitor_tasks ADD COLUMN interval INTEGER DEFAULT 60;`,
		`ALTER TABLE servers ADD COLUMN latitude REAL DEFAULT 0;`,
        `ALTER TABLE servers ADD COLUMN longitude REAL DEFAULT 0;`,
		`ALTER TABLE task_results ADD COLUMN loss REAL DEFAULT 0;`,
		`ALTER TABLE task_results ADD COLUMN jitter REAL DEFAULT 0;`,
		`CREATE INDEX IF NOT EXISTS idx_task_results_node_time ON task_results(node_id, timestamp);`,
		`CREATE INDEX IF NOT EXISTS idx_task_results_task_id ON task_results(task_id);`,
		`ALTER TABLE monitor_tasks ADD COLUMN network_type TEXT DEFAULT '其他';`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_tasks_network ON monitor_tasks(network_type);`,
	}

	for _, q := range queries {
		_, err := database.DB.Exec(q)
		if err != nil && !isDuplicateColumnError(err) {
			logger.Log.Error("migration error", 
				zap.String("module", "Migration"), 
				zap.Error(err),
			)
		}
	}

	var currentPass string
	err := database.DB.QueryRow("SELECT value FROM settings WHERE key = 'admin_password'").Scan(&currentPass)
	if err == nil && currentPass != "" && !strings.HasPrefix(currentPass, "$2") {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(currentPass), bcrypt.DefaultCost)
		if hashErr == nil {
			database.DB.Exec("UPDATE settings SET value = ? WHERE key = 'admin_password'", string(hash))
			logger.Log.Info("系统迁移: 已将明文密码升级为 bcrypt 哈希", zap.String("module", "Migration"))
		} else {
			logger.Log.Error("系统迁移: 密码哈希升级失败", 
				zap.String("module", "Migration"), 
				zap.Error(hashErr),
			)
		}
	}
}

func isDuplicateColumnError(err error) bool {
	return strings.Contains(err.Error(), "duplicate column") || strings.Contains(err.Error(), "already exists")
}

func clearExpiredSessions() {
    expirationLimit := time.Now().AddDate(0, 0, -30).Unix()
    database.DB.Exec("DELETE FROM sessions WHERE created_at < ?", expirationLimit)
}


// 🌟 新增：提取原先的 main 核心逻辑为 runServer 函数
func runServer(cmd *cobra.Command, args []string) {
	logger.Init()
	defer logger.Log.Sync()
	if _, err := os.Stat("./dist"); os.IsNotExist(err) {
		logger.Log.Warn("警告: ./dist 目录不存在，前端面板将返回 404")
	}

	database.InitDB()
	defer database.DB.Close()

	migrate()
	
	database.StartSLACalculator()
	core.StartDataRetentionTask()
	core.StartAlertEngine()
	core.StartVersionCheckTask() // 新增此行
	

	// 1. 需要鉴权的管理 API
	http.HandleFunc("/api/admin/servers/static", withLogging(authMiddleware(handlers.ApiStaticNodesHandler)))
	http.HandleFunc("/api/admin/settings/theme", withLogging(authMiddleware(handlers.ApiUpdateThemeHandler)))
    http.HandleFunc("/api/admin/servers/realtime", withLogging(authMiddleware(handlers.ApiRealtimeNodesHandler)))
	http.HandleFunc("/api/admin/servers/save", withLogging(authMiddleware(handlers.ApiSaveServerHandler)))
	http.HandleFunc("/api/admin/servers/update", withLogging(authMiddleware(handlers.ApiUpdateServerHandler)))
	http.HandleFunc("/api/admin/servers/delete", withLogging(authMiddleware(handlers.ApiDeleteServerHandler)))
	http.HandleFunc("/api/admin/settings/get", withLogging(authMiddleware(handlers.ApiGetSettingsHandler)))
	http.HandleFunc("/api/admin/settings/update", withLogging(authMiddleware(handlers.ApiUpdateSettingsHandler)))
	http.HandleFunc("/api/admin/settings/2fa/generate", withLogging(authMiddleware(handlers.ApiGenerateTFAHandler)))
	http.HandleFunc("/api/admin/settings/2fa/verify", withLogging(authMiddleware(handlers.ApiVerifyAndEnableTFAHandler)))
	http.HandleFunc("/api/admin/notify/test", withLogging(authMiddleware(handlers.ApiTestNotifyHandler)))
	http.HandleFunc("/api/admin/logs", withLogging(authMiddleware(handlers.ApiGetLogsHandler)))
	http.HandleFunc("/api/admin/tasks/all", withLogging(authMiddleware(handlers.ApiAllTasksHandler)))
	http.HandleFunc("/api/admin/tasks/add", withLogging(authMiddleware(handlers.ApiAddTaskHandler)))
	http.HandleFunc("/api/admin/tasks/edit", withLogging(authMiddleware(handlers.ApiEditTaskHandler)))
	http.HandleFunc("/api/admin/tasks/delete", withLogging(authMiddleware(handlers.ApiDeleteTaskHandler)))
	http.HandleFunc("/api/admin/terminal/ticket", withLogging(authMiddleware(handlers.ApiGenerateWsTicketHandler)))
    http.HandleFunc("/api/admin/settings/geoip/update", withLogging(authMiddleware(handlers.ApiUpdateGeoIPDBHandler)))
    http.HandleFunc("/api/admin/settings/geoip/test", withLogging(authMiddleware(handlers.ApiTestGeoIPHandler)))
    http.HandleFunc("/api/admin/sessions", withLogging(authMiddleware(handlers.ApiGetSessionsHandler)))
	http.HandleFunc("/api/admin/sessions/revoke", withLogging(authMiddleware(handlers.ApiRevokeSessionHandler)))

	// 2. 公开 API (探针和访客)
	http.HandleFunc("/api/admin/login", withLogging(handlers.ApiLoginHandler))
	http.HandleFunc("/api/public/servers/static", withLogging(handlers.ApiPublicStaticServersHandler))
    http.HandleFunc("/api/public/servers/realtime", withLogging(handlers.ApiPublicRealtimeServersHandler))
	http.HandleFunc("/api/public/settings", withLogging(handlers.ApiPublicSettingsHandler))
	http.HandleFunc("/api/public/servers/geo", withLogging(handlers.ApiGeoNodesHandler)) 
	http.HandleFunc("/api/data", withLogging(handlers.ApiDataHandler))
	http.HandleFunc("/api/data/ping", withLogging(handlers.ApiPingDataHandler))
	http.HandleFunc("/api/data/ping/summary", withLogging(handlers.ApiPingSummaryHandler)) // 🌟 新增这一行
	http.HandleFunc("/api/report", withLogging(handlers.ApiReceiveHandler))
    http.HandleFunc("/agent/register", withLogging(handlers.ApiRegisterHandler))
	http.HandleFunc("/api/tasks/pull", withLogging(handlers.ApiPullTasksHandler))
	http.HandleFunc("/api/tasks/push", withLogging(handlers.ApiTaskResultHandler))
	http.HandleFunc("/api/auth/github/login", withLogging(handlers.OAuthGithubLoginHandler))
	http.HandleFunc("/api/auth/github/callback", withLogging(handlers.OAuthGithubCallbackHandler))
    
	// 3. WebSocket 路由
	http.HandleFunc("/agent/ws", handlers.WsAgentHandler)
	http.HandleFunc("/api/public/ws", handlers.WsFrontendHandler)
	http.HandleFunc("/api/terminal/ws", handlers.WsTerminalFrontendHandler)
	http.HandleFunc("/agent/terminal/ws", handlers.WsTerminalAgentHandler)

	// 4. 探针二进制文件与安装脚本
	serveFile := func(path string, contentType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, path)
		}
	}
	http.HandleFunc("/install.sh", serveFile("./scripts/install.sh", "text/plain"))
	http.HandleFunc("/install.mac.sh", serveFile("./scripts/install.mac.sh", "text/plain"))
	http.HandleFunc("/install.ps1", serveFile("./scripts/install.ps1", "text/plain"))

	http.HandleFunc("/float-agent-linux-amd64", serveFile("./float-agent-linux-amd64", "application/octet-stream"))
	http.HandleFunc("/float-agent-linux-arm64", serveFile("./float-agent-linux-arm64", "application/octet-stream"))
	http.HandleFunc("/float-agent-windows-amd64.exe", serveFile("./float-agent-windows-amd64.exe", "application/octet-stream"))
	http.HandleFunc("/float-agent-darwin-amd64", serveFile("./float-agent-darwin-amd64", "application/octet-stream"))
	http.HandleFunc("/float-agent-darwin-arm64", serveFile("./float-agent-darwin-arm64", "application/octet-stream"))
	http.HandleFunc("/api/admin/settings/theme/install", withLogging(authMiddleware(handlers.ApiInstallGithubThemeHandler)))
	http.HandleFunc("/api/admin/settings/theme/upload", withLogging(authMiddleware(handlers.ApiUploadZipThemeHandler)))
	http.HandleFunc("/api/admin/settings/theme/list", withLogging(authMiddleware(handlers.ApiGetLocalThemesHandler)))

	// 5. 静态前端资源处理与动态主题分发
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Clean(r.URL.Path)

		// 1. 强制拦截：后台管理与核心静态资源走内置 dist
		if strings.HasPrefix(path, "/admin") || strings.HasPrefix(path, "/assets") {
			target := filepath.Join("dist", path)
			if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
				http.ServeFile(w, r, target)
				return
			}
			http.ServeFile(w, r, "dist/index.html")
			return
		}

		// 2. 读取当前激活的主题
		var currentTheme string
		err := database.DB.QueryRow("SELECT value FROM settings WHERE key = 'theme'").Scan(&currentTheme)
		if err != nil || currentTheme == "" {
			currentTheme = "default"
		}

		// 3. 内置主题回退：直接使用内置 dist
		if currentTheme == "default" || currentTheme == "matrix" {
			target := filepath.Join("dist", path)
			if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
				http.ServeFile(w, r, target)
				return
			}
			http.ServeFile(w, r, "dist/index.html")
			return
		}

		// 4. 第三方主题代理：将请求映射至 data/themes/{theme}/dist
		themeDir := filepath.Join("data", "themes", currentTheme, "dist")
		target := filepath.Join(themeDir, path)
		if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, target)
			return
		}

		// 缺失具体文件时，返回第三方主题的 index.html 交由其内部 router 处理
		http.ServeFile(w, r, filepath.Join(themeDir, "index.html"))
	})
	

	port := "8080"
	logger.Log.Info("Backend API Server is starting", zap.String("port", port))
	
	if err := http.ListenAndServe(":"+port, nil); err != nil && err != http.ErrServerClosed {
		logger.Log.Error("HTTP server failed", zap.Error(err))
	}
}

// 🌟 新增：重构 main 函数为主命令路由中心
func main() {
	var rootCmd = &cobra.Command{
		Use:   "Float-server",
		Short: "Float Server Management System",
	}

	// 子命令 1：启动服务
	var serveCmd = &cobra.Command{
		Use:   "serve",
		Short: "Start the Float backend server",
		Run:   runServer,
	}

	// 子命令 2：强制取消 2FA
	var disable2FACmd = &cobra.Command{
		Use:   "disable-2fa",
		Short: "Force disable 2FA authentication",
		Run: func(cmd *cobra.Command, args []string) {
			logger.Init()
			database.InitDB()
			defer database.DB.Close()

			_, err := database.DB.Exec("DELETE FROM settings WHERE key IN ('tfa_enabled', 'tfa_secret', 'pending_tfa_secret')")
			if err != nil {
				fmt.Printf("❌ 取消 2FA 失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✅ 2FA 已强制取消！您可以直接使用账号密码登录。")
		},
	}

	// 子命令 3：强制重置密码
	var newPassword string
	var chpasswdCmd = &cobra.Command{
		Use:   "chpasswd",
		Short: "Force change admin password",
		Run: func(cmd *cobra.Command, args []string) {
			if newPassword == "" {
				fmt.Println("❌ 错误: 请使用 -p 或 --password 参数提供新密码")
				os.Exit(1)
			}

			logger.Init()
			database.InitDB()
			defer database.DB.Close()

			hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
			if err != nil {
				fmt.Printf("❌ 密码加密失败: %v\n", err)
				os.Exit(1)
			}

			_, err = database.DB.Exec("UPDATE settings SET value = ? WHERE key = 'admin_password'", string(hash))
			if err != nil {
				fmt.Printf("❌ 密码重置失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✅ 管理员密码重置成功！")
		},
	}
	chpasswdCmd.Flags().StringVarP(&newPassword, "password", "p", "", "New password for admin")

	// 注册所有子命令
	rootCmd.AddCommand(serveCmd, disable2FACmd, chpasswdCmd)

	// 执行命令拦截
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}