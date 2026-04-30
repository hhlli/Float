package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// 新增服务端版本号常量
const ServerVersion = "v1.0.5"

// withLogging 是一个 HTTP 中间件，用于全局日志记录和异常捕获
func withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 1. 全局异常捕获 (Panic Recovery)
		defer func() {
			if err := recover(); err != nil {
				errMsg := fmt.Sprintf("API 崩溃 [%s] %s: %v", r.Method, r.URL.Path, err)
				log.Println(errMsg)
				// 将致命错误写入数据库面板
				insertLog("ERROR", errMsg)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		// 2. 执行实际的路由处理逻辑
		next(w, r)

		// 3. 记录 API 访问日志
		duration := time.Since(start)
		// 常规请求只打印到控制台，避免撑爆 SQLite 数据库
		// 如果你想把所有请求都存入面板，可以将下面这行换成 insertLog("INFO", ...)
		log.Printf("[API] %s %s - %v\n", r.Method, r.URL.Path, duration)
	}
}
// ===== 鉴权中间件 =====
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expectedToken := getServerToken()
		authHeader := r.Header.Get("Authorization")

		if authHeader == "" {
			http.Error(w, "Missing Authorization", http.StatusUnauthorized)
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			http.Error(w, "Invalid Authorization format", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, prefix)
		if token != expectedToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	}
}

// ===== 数据库迁移 =====
func migrate() {
	queries := []string{
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
		// monitor_tasks 增加 type 和 interval 字段
		`ALTER TABLE monitor_tasks ADD COLUMN type TEXT DEFAULT 'TCP';`,
		`ALTER TABLE monitor_tasks ADD COLUMN interval INTEGER DEFAULT 60;`,
	}

	for _, q := range queries {
		_, err := db.Exec(q)
		if err != nil {
			if !isDuplicateColumnError(err) {
				log.Println("migration error:", err)
			}
		}
	}
}

func isDuplicateColumnError(err error) bool {
	return err != nil &&
		(strings.Contains(err.Error(), "duplicate column") ||
			strings.Contains(err.Error(), "already exists"))
}

// ===== 主函数 =====
func main() {
	initDB()
	defer db.Close()

	migrate()

	startDataRetentionTask(7)
	startAlertEngine()

// ===== 需要鉴权的管理 API =====
http.HandleFunc("/api/admin/servers", withLogging(authMiddleware(apiNodesHandler)))
http.HandleFunc("/api/admin/servers/save", withLogging(authMiddleware(apiSaveServerHandler)))
http.HandleFunc("/api/admin/servers/update", withLogging(authMiddleware(apiUpdateServerHandler)))
http.HandleFunc("/api/admin/servers/delete", withLogging(authMiddleware(apiDeleteServerHandler)))

http.HandleFunc("/api/admin/settings/get", withLogging(authMiddleware(apiGetSettingsHandler)))
http.HandleFunc("/api/admin/settings/update", withLogging(authMiddleware(apiUpdateSettingsHandler)))
http.HandleFunc("/api/admin/notify/test", withLogging(authMiddleware(apiTestNotifyHandler)))
http.HandleFunc("/api/admin/logs", withLogging(authMiddleware(apiGetLogsHandler)))

http.HandleFunc("/api/admin/tasks/all", withLogging(authMiddleware(apiAllTasksHandler)))
http.HandleFunc("/api/admin/tasks/add", withLogging(authMiddleware(apiAddTaskHandler)))
http.HandleFunc("/api/admin/tasks/edit", withLogging(authMiddleware(apiEditTaskHandler)))
http.HandleFunc("/api/admin/tasks/delete", withLogging(authMiddleware(apiDeleteTaskHandler)))

// ===== 公开 API (探针和访客) =====
// 注意：探针上报和任务结果通常使用 Token 鉴权，由处理函数或独立中间件完成，此处暂归为公开层
http.HandleFunc("/api/admin/login", withLogging(apiLoginHandler))
http.HandleFunc("/api/public/servers", withLogging(apiPublicServersHandler))
http.HandleFunc("/api/public/settings", withLogging(apiPublicSettingsHandler))

http.HandleFunc("/api/data", withLogging(apiDataHandler))
http.HandleFunc("/api/data/ping", withLogging(apiPingDataHandler))

http.HandleFunc("/api/report", withLogging(apiReceiveHandler)) // 探针上报节点状态
http.HandleFunc("/api/tasks/pull", withLogging(apiPullTasksHandler)) // 探针拉取任务
http.HandleFunc("/api/tasks/push", withLogging(apiTaskResultHandler)) // 探针推送测速结果

// WebSocket 路由 (内部有独立鉴权，不需要日志中间件)
http.HandleFunc("/agent/ws", wsAgentHandler)

	// ===== 静态文件 =====
	// ===== 1. 部署脚本路由 =====
    http.HandleFunc("/install.sh", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        http.ServeFile(w, r, "./install.sh")
    })
    http.HandleFunc("/install.mac.sh", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        http.ServeFile(w, r, "./install.mac.sh")
    })
    http.HandleFunc("/install.ps1", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        http.ServeFile(w, r, "./install.ps1")
    })

    // ===== 2. 探针二进制文件路由 (必须与脚本中的下载链接对应) =====
    // Linux
    http.HandleFunc("/probe-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/octet-stream")
        http.ServeFile(w, r, "./probe-linux-amd64")
    })
	// 👇👇👇 请把这几行老老实实加进来 👇👇👇
    http.HandleFunc("/probe-linux-arm64", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/octet-stream")
        http.ServeFile(w, r, "./probe-linux-arm64")
    })
    // 👆👆👆👆👆👆👆👆👆👆👆👆👆👆👆👆👆👆
    // Windows
    http.HandleFunc("/probe-windows-amd64.exe", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/octet-stream")
        http.ServeFile(w, r, "./probe-windows-amd64.exe")
    })
    // macOS
    http.HandleFunc("/probe-darwin-amd64", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/octet-stream")
        http.ServeFile(w, r, "./probe-darwin-amd64")
    })
    http.HandleFunc("/probe-darwin-arm64", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/octet-stream")
        http.ServeFile(w, r, "./probe-darwin-arm64")
    })

	fmt.Println("Backend API Server is running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
