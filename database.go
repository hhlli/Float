package main

import (
    "database/sql"
    "log"
    "time"

    _ "modernc.org/sqlite"
)

var db *sql.DB

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

// --- 新增的任务数据结构 ---
type Task struct {
    ID     int    `json:"id"`
    Type   string `json:"type"`
    Target string `json:"target"`
}

type HeartbeatResponse struct {
    Status string `json:"status"`
    Tasks  []Task `json:"tasks"`
}

func initDB() {
    var err error
    db, err = sql.Open("sqlite", "./metrics.db?_journal=WAL&_sync=NORMAL")
    if err != nil {
        log.Fatal(err)
    }
    db.SetMaxOpenConns(1)

    db.Exec(`CREATE TABLE IF NOT EXISTS metrics (
        id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT, timestamp INTEGER,
        cpu_usage REAL, mem_usage REAL, disk_usage REAL, net_rx REAL, net_tx REAL
    );`)
    db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_time ON metrics(node_id, timestamp);`)
    
    db.Exec(`CREATE TABLE IF NOT EXISTS task_results (
        id INTEGER PRIMARY KEY AUTOINCREMENT, 
        task_id INTEGER, 
        node_id TEXT, 
        ping_ms REAL,
        status TEXT,
        timestamp INTEGER
    );`)
    // 更新 servers 表结构，加入自动注册必需的 last_active, cpu, mem 字段
// 更新 servers 表结构，加入系统、存储与网络统计字段
db.Exec(`CREATE TABLE IF NOT EXISTS servers (
    node_id TEXT PRIMARY KEY, 
    name TEXT, 
    region TEXT, 
    cost REAL,
    currency TEXT,
    billing_date TEXT, 
    monthly_bw REAL,
    bw_reset_day INTEGER,
    notes TEXT DEFAULT '', 
    status TEXT, 
    created_at INTEGER,
    last_active INTEGER,
    cpu REAL,
    mem REAL,
    mem_used REAL,
    mem_total REAL,
    disk REAL,
    disk_used REAL,
    disk_total REAL,
    os TEXT,
    uptime INTEGER,
    net_rx_speed REAL,
    net_tx_speed REAL,
    net_rx_total REAL,
    net_tx_total REAL,
    swap_used REAL DEFAULT 0,
    swap_total REAL DEFAULT 0,
    tcp_conn INTEGER DEFAULT 0,
    udp_conn INTEGER DEFAULT 0,
    kernel TEXT DEFAULT '',
    arch TEXT DEFAULT '',
    virt TEXT DEFAULT '',
    cpu_model TEXT DEFAULT '',
    processes INTEGER DEFAULT 0,
    load_1 REAL DEFAULT 0,
    load_5 REAL DEFAULT 0,
    load_15 REAL DEFAULT 0
    );`)

    // 为了兼容已有的数据库文件，必须添加 ALTER TABLE 语句
    // 放在 CREATE TABLE 语句的下面
    db.Exec(`ALTER TABLE servers ADD COLUMN swap_used REAL DEFAULT 0;`)
    db.Exec(`ALTER TABLE servers ADD COLUMN swap_total REAL DEFAULT 0;`)
    db.Exec(`ALTER TABLE servers ADD COLUMN tcp_conn INTEGER DEFAULT 0;`)
    db.Exec(`ALTER TABLE servers ADD COLUMN udp_conn INTEGER DEFAULT 0;`)
    db.Exec(`ALTER TABLE servers ADD COLUMN kernel TEXT DEFAULT '';`)
    db.Exec(`ALTER TABLE servers ADD COLUMN arch TEXT DEFAULT '';`)
    db.Exec(`ALTER TABLE servers ADD COLUMN virt TEXT DEFAULT '';`)
    db.Exec(`ALTER TABLE servers ADD COLUMN cpu_model TEXT DEFAULT '';`)
    db.Exec(`ALTER TABLE servers ADD COLUMN processes INTEGER DEFAULT 0;`)
    db.Exec(`ALTER TABLE servers ADD COLUMN load_1 REAL DEFAULT 0;`)
    db.Exec(`ALTER TABLE servers ADD COLUMN load_5 REAL DEFAULT 0;`)
    db.Exec(`ALTER TABLE servers ADD COLUMN load_15 REAL DEFAULT 0;`)
    // 找到 ALTER TABLE servers 的区域，追加以下两行：
    db.Exec(`ALTER TABLE servers ADD COLUMN ipv4 TEXT DEFAULT '';`)
    db.Exec(`ALTER TABLE servers ADD COLUMN ipv6 TEXT DEFAULT '';`)

    db.Exec(`CREATE TABLE IF NOT EXISTS settings (
        key TEXT PRIMARY KEY, value TEXT
    );`)

    db.Exec(`CREATE TABLE IF NOT EXISTS logs (
        id INTEGER PRIMARY KEY AUTOINCREMENT, level TEXT, message TEXT, timestamp INTEGER
    );`)

    // --- 新增的任务表 ---
    db.Exec(`CREATE TABLE IF NOT EXISTS monitor_tasks (
        id INTEGER PRIMARY KEY AUTOINCREMENT, 
        name TEXT,
        type TEXT,
        target TEXT, 
        excluded_nodes TEXT DEFAULT '[]', 
        interval INTEGER,
        created_at INTEGER
    );`)
    db.Exec(`ALTER TABLE monitor_tasks ADD COLUMN excluded_nodes TEXT DEFAULT '[]';`)
// 在 database.go 的 initDB 函数末尾追加
    db.Exec(`ALTER TABLE servers ADD COLUMN agent_version TEXT DEFAULT '';`)
    // 在 database.go 的 initDB 函数末尾追加默认配置
    db.Exec("INSERT INTO settings (key, value) SELECT 'offline_threshold', '180' WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'offline_threshold')")
    db.Exec("INSERT INTO settings (key, value) SELECT 'offline_cooldown', '3600' WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'offline_cooldown')")
    
    var count int
    db.QueryRow("SELECT COUNT(*) FROM settings").Scan(&count)
    if count == 0 {
        log.Println("系统初始化: 写入默认配置")
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "server_token", "my-super-secret-token")
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "retention_days", "7")
        
        // 通知设置
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "tg_bot_token", "")
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "tg_chat_id", "")
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "notify_offline_enable", "true")
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "notify_load_cpu", "80")
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "notify_expire_days", "7")
        // 🌟 新增：注入负载告警的全局默认 JSON 配置
        db.Exec("INSERT INTO settings (key, value) SELECT 'load_rule', '{\"enabled\":false,\"cpu_threshold\":80,\"mem_threshold\":80,\"duration\":1}' WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'load_rule')")
        // 个性化设置
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "site_name", "监控面板")
        db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "theme", "light")
        // 确保默认账号密码存在 (如果不存在则插入)
        db.Exec("INSERT INTO settings (key, value) SELECT 'admin_username', 'admin' WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'admin_username')")
        db.Exec("INSERT INTO settings (key, value) SELECT 'admin_password', 'admin' WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'admin_password')")
    }
}

// 从数据库获取全局鉴权 Token
func getServerToken() string {
    var token string
    err := db.QueryRow("SELECT value FROM settings WHERE key = 'server_token'").Scan(&token)
    if err != nil {
        return "my-super-secret-token"
    }
    return token
}

// 写入系统日志
func insertLog(level, message string) {
    db.Exec("INSERT INTO logs (level, message, timestamp) VALUES (?, ?, ?)", level, message, time.Now().Unix())
    log.Printf("[%s] %s\n", level, message)
}