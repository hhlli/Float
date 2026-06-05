package database

import (
	"database/sql"
	"os"
	"time"
	"sync"
	"golang.org/x/crypto/bcrypt"
	"Float/internal/logger"
	"go.uber.org/zap"

	_ "modernc.org/sqlite"
)

const ServerVersion = "v1.0.1"
// ── 全局内存缓存 (用于 SLA 和历史状态) ──────────────────────────
var (
	HeatmapCacheMap    map[string]map[string]int
	ActiveMinsCacheMap map[string]int
	HourlyCacheMap     map[string]map[string]int
	CacheMutex         sync.RWMutex
	CacheLastUpdate    time.Time
)
var DB *sql.DB

type Metric struct {
	Timestamp int64   `json:"timestamp"`
	CPUUsage  float64 `json:"cpu_usage"`
	MemUsage  float64 `json:"mem_usage"`
	DiskUsage float64 `json:"disk_usage"`
}

type Payload struct {
	NodeID string `json:"node_id"`
	Data   Metric `json:"data"`
}

type Task struct {
	ID     int    `json:"id"`
	Type   string `json:"type"`
	Target string `json:"target"`
}

type HeartbeatResponse struct {
	Status string `json:"status"`
	Tasks  []Task `json:"tasks"`
}

func InitDB() {
	if err := os.MkdirAll("./data", 0755); err != nil {
		logger.Log.Fatal("创建数据目录失败",
			zap.String("module", "Init"),
			zap.Error(err),
		)
	}

	var err error
	DB, err = sql.Open("sqlite", "./data/metrics.db?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		logger.Log.Fatal("系统初始化引发致命错误", 
			zap.String("module", "Init"),
			zap.Error(err),
		)
	}
	DB.SetMaxOpenConns(50)
	DB.SetMaxIdleConns(10)

	DB.Exec(`CREATE TABLE IF NOT EXISTS metrics (
        id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT, timestamp INTEGER,
        cpu_usage REAL, mem_usage REAL, disk_usage REAL, net_rx REAL, net_tx REAL
    );`)
	DB.Exec(`CREATE INDEX IF NOT EXISTS idx_node_time ON metrics(node_id, timestamp);`)

	DB.Exec(`CREATE TABLE IF NOT EXISTS task_results (
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		task_id INTEGER, 
		node_id TEXT, 
		ping_ms REAL,
		status TEXT,
		timestamp INTEGER,
		extra_data TEXT DEFAULT '{}'
	);`)

	// 兼容已有表结构，避免启动报错，静默追加 JSON 列
	DB.Exec(`ALTER TABLE task_results ADD COLUMN extra_data TEXT DEFAULT '{}';`)

	DB.Exec(`CREATE TABLE IF NOT EXISTS servers (
		node_id TEXT PRIMARY KEY, name TEXT, region TEXT, cost REAL, currency TEXT,
		billing_cycle TEXT DEFAULT 'month',
		billing_date TEXT, monthly_bw REAL, bw_reset_day INTEGER, notes TEXT DEFAULT '', 
		is_hidden INTEGER DEFAULT 0, 
		status TEXT, created_at INTEGER, last_active INTEGER, cpu REAL, mem REAL,
		mem_used REAL, mem_total REAL, disk REAL, disk_used REAL, disk_total REAL,
		os TEXT, uptime INTEGER, net_rx_speed REAL, net_tx_speed REAL,
		net_rx_total REAL, net_tx_total REAL, swap_used REAL DEFAULT 0,
		swap_total REAL DEFAULT 0, tcp_conn INTEGER DEFAULT 0, udp_conn INTEGER DEFAULT 0,
		kernel TEXT DEFAULT '', arch TEXT DEFAULT '', virt TEXT DEFAULT '',
		cpu_model TEXT DEFAULT '', processes INTEGER DEFAULT 0, load_1 REAL DEFAULT 0,
		load_5 REAL DEFAULT 0, load_15 REAL DEFAULT 0, ipv4 TEXT DEFAULT '', 
		ipv6 TEXT DEFAULT '', agent_version TEXT DEFAULT '', docker_containers TEXT DEFAULT '[]'
    );`)
	DB.Exec(`ALTER TABLE servers ADD COLUMN is_hidden INTEGER DEFAULT 0;`)
	DB.Exec(`ALTER TABLE servers ADD COLUMN docker_containers TEXT DEFAULT '[]';`)
	DB.Exec(`ALTER TABLE servers ADD COLUMN billing_cycle TEXT DEFAULT 'month';`)
	DB.Exec(`ALTER TABLE servers ADD COLUMN terminal_enabled INTEGER DEFAULT 0;`)
	DB.Exec(`ALTER TABLE servers ADD COLUMN auth_token TEXT DEFAULT '';`)
	DB.Exec(`CREATE TABLE IF NOT EXISTS settings (
        key TEXT PRIMARY KEY, value TEXT
    );`)

	DB.Exec(`CREATE TABLE IF NOT EXISTS logs (
        id INTEGER PRIMARY KEY AUTOINCREMENT, level TEXT, message TEXT, timestamp INTEGER
    );`)

	DB.Exec(`CREATE TABLE IF NOT EXISTS monitor_tasks (
        id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, type TEXT, target TEXT, 
        excluded_nodes TEXT DEFAULT '[]', interval INTEGER, created_at INTEGER
    );`)

	DB.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		created_at INTEGER,
		ip TEXT DEFAULT '',
		user_agent TEXT DEFAULT '',
		last_active INTEGER DEFAULT 0
	);`)
	
	DB.Exec(`ALTER TABLE sessions ADD COLUMN ip TEXT DEFAULT '';`)
	DB.Exec(`ALTER TABLE sessions ADD COLUMN user_agent TEXT DEFAULT '';`)
	DB.Exec(`ALTER TABLE sessions ADD COLUMN last_active INTEGER DEFAULT 0;`)
	// 新增经纬度
	DB.Exec(`ALTER TABLE servers ADD COLUMN latitude REAL DEFAULT 0;`)
    DB.Exec(`ALTER TABLE servers ADD COLUMN longitude REAL DEFAULT 0;`)
	// 新增 30 天聚合状态表
	DB.Exec(`CREATE TABLE IF NOT EXISTS daily_stats (
		node_id TEXT, date TEXT, online_mins INTEGER DEFAULT 1, PRIMARY KEY(node_id, date)
	);`)

	// 生成默认密码哈希
	defaultHash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
	if err != nil {
		logger.Log.Fatal("生成默认密码哈希失败",
			zap.String("module", "Init"),
			zap.Error(err),
		)
	}

	defaultSettings := map[string]string{
		"server_token":          "my-super-secret-token",
		"theme":                 "default",
		"enable_history":        "true",
		"load_retention_days":   "7",
		"ping_retention_days":   "7",
		"tg_bot_token":          "",
		"tg_chat_id":            "",
		"notify_offline_enable": "true",
		"notify_load_cpu":       "80",
		"notify_expire_days":    "7",
		"load_rule":             `{"enabled":false,"cpu_threshold":80,"mem_threshold":80,"duration":1}`,
		"site_name":             "监控面板",
		"admin_username":        "admin",
		"admin_password":        string(defaultHash), // bcrypt 哈希，替换明文 "admin"
		"offline_threshold":     "180",
		"offline_cooldown":      "3600",
		// 👇 新增 OAuth 相关默认配置
		"oauth_github_client_id":     "",
		"oauth_github_client_secret": "",
		"oauth_github_whitelist":     "", // 允许登录的 GitHub 用户名，多个用逗号分隔，如 "lcrunli,admin"
		// 👇 新增 GeoIP 相关默认配置
		"geoip_enabled":         "true",
		"geoip_provider":        "ip-api", // 可选值: ip-api, maxmind
		"geoip_license_key":     "",
		// 👇 新增自动发现密钥配置，留空代表禁用
		"auto_discovery_token":  "",
	}

	for key, value := range defaultSettings {
		query := "INSERT INTO settings (key, value) SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = ?)"
		_, err := DB.Exec(query, key, value, key)
		if err != nil {
			logger.Log.Error("写入配置项失败", 
				zap.String("module", "Init"),
				zap.String("key", key),
				zap.Error(err),
			)
		}
	}
}

func GetServerToken() string {
	var token string
	err := DB.QueryRow("SELECT value FROM settings WHERE key = 'server_token'").Scan(&token)
	if err != nil || token == "" {
		return ""
	}
	return token
}

func InsertLog(level, message string) {
    DB.Exec("INSERT INTO logs (level, message, timestamp) VALUES (?, ?, ?)", level, message, time.Now().Unix())
    
    switch level {
    case "ERROR", "error":
        logger.Log.Error(message, zap.String("module", "DB_Log"))
    case "WARN", "warn", "WARNING":
        logger.Log.Warn(message, zap.String("module", "DB_Log"))
    case "DEBUG", "debug":
        logger.Log.Debug(message, zap.String("module", "DB_Log"))
    default:
        logger.Log.Info(message, zap.String("module", "DB_Log"))
    }
}
// 2. 在文件末尾（GetServerToken 函数下方）新增以下函数
func GetAutoDiscoveryToken() string {
	var token string
	err := DB.QueryRow("SELECT value FROM settings WHERE key = 'auto_discovery_token'").Scan(&token)
	if err != nil || token == "" {
		return ""
	}
	return token
}

// ── 5. SLA 与历史数据异步计算任务 ────────────────────────────────────────

func StartSLACalculator() {
	// 系统启动时立即执行一次计算，避免最初的 5 分钟内无数据
	calculateSLA()

	// 设定为每 5 分钟计算一次
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		for range ticker.C {
			calculateSLA()
		}
	}()
}

func calculateSLA() {
	// 1. 30天历史查询 (指向每日聚合表)
	rowsHistory, err := DB.Query(`
		SELECT node_id, strftime('%m-%d', date) as log_date, online_mins
		FROM daily_stats
		WHERE date >= date('now', 'localtime', '-30 days')
	`)
	newMap := make(map[string]map[string]int)
	if err == nil {
		defer rowsHistory.Close()
		for rowsHistory.Next() {
			var nID, lDate string
			var mins int
			if err := rowsHistory.Scan(&nID, &lDate, &mins); err == nil {
				if newMap[nID] == nil {
					newMap[nID] = make(map[string]int)
				}
				newMap[nID][lDate] = mins
			}
		}
	}

	// 2. 24小时活跃分钟数查询 (🌟 去掉 database. 前缀)
	rowsMins, errMins := DB.Query(`
		SELECT node_id, COUNT(DISTINCT timestamp / 60) as active_mins
		FROM metrics
		WHERE timestamp >= strftime('%s', 'now', '-24 hours')
		GROUP BY node_id
	`)
	newMinsMap := make(map[string]int)
	if errMins == nil {
		defer rowsMins.Close()
		for rowsMins.Next() {
			var nID string
			var mins int
			if err := rowsMins.Scan(&nID, &mins); err == nil {
				newMinsMap[nID] = mins
			}
		}
	}

	// 3. 24小时按小时分布查询 (🌟 去掉 database. 前缀)
	rowsHourly, errHourly := DB.Query(`
		SELECT node_id, strftime('%H', timestamp, 'unixepoch', 'localtime') as log_hour, COUNT(DISTINCT timestamp / 60) as hourly_mins
		FROM metrics
		WHERE timestamp >= strftime('%s', 'now', '-24 hours')
		GROUP BY node_id, log_hour
	`)
	newHourlyMap := make(map[string]map[string]int)
	if errHourly == nil {
		defer rowsHourly.Close()
		for rowsHourly.Next() {
			var nID, lHour string
			var mins int
			if err := rowsHourly.Scan(&nID, &lHour, &mins); err == nil {
				if newHourlyMap[nID] == nil {
					newHourlyMap[nID] = make(map[string]int)
				}
				newHourlyMap[nID][lHour] = mins
			}
		}
	}

	// 4. 加锁写入全局变量 (🌟 确保这里也没有 database. 前缀)
	CacheMutex.Lock()
	HeatmapCacheMap = newMap
	ActiveMinsCacheMap = newMinsMap
	HourlyCacheMap = newHourlyMap
	CacheLastUpdate = time.Now()
	CacheMutex.Unlock()
}