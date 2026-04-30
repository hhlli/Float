package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
	"strings"
)

// ── Telegram 推送（保持不变）──────────────────────────────────────────────────

func sendTelegramMsg(text string) {
	var botToken, chatID, endpoint string
	db.QueryRow("SELECT value FROM settings WHERE key = 'tg_bot_token'").Scan(&botToken)
	db.QueryRow("SELECT value FROM settings WHERE key = 'tg_chat_id'").Scan(&chatID)
	db.QueryRow("SELECT value FROM settings WHERE key = 'tg_api_endpoint'").Scan(&endpoint)

	if botToken == "" || chatID == "" {
		return
	}
	if endpoint == "" {
		endpoint = "https://api.telegram.org/bot"
	}

	apiURL := fmt.Sprintf("%s%s/sendMessage", endpoint, botToken)
	payload := map[string]string{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	jsonData, _ := json.Marshal(payload)

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		insertLog("ERROR", "Telegram 推送失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		insertLog("INFO", "Telegram 告警推送成功")
	} else {
		insertLog("ERROR", fmt.Sprintf("Telegram 推送失败，状态码: %d", resp.StatusCode))
	}
}

// ── 规则结构体（对应前端存入 settings 表的 JSON）────────────────────────────

type LoadRule struct {
	Enabled      bool    `json:"enabled"`
	CPUThreshold float64 `json:"cpu_threshold"`
	MemThreshold float64 `json:"mem_threshold"`
	Duration     int     `json:"duration"` // 分钟，当前实现用于冷却控制
}

type TrafficRule struct {
	Enabled          bool               `json:"enabled"`
	DefaultThreshold float64            `json:"default_threshold"` // 对应前端
	Overrides        map[string]float64 `json:"overrides"`         // 对应前端单节点设置
}

type ExpiryRule struct {
	Enabled    bool `json:"enabled"`
	DaysBefore int  `json:"days_before"`
}

// 从 settings 表读取并解析 JSON 规则
func getLoadRule() LoadRule {
	r := LoadRule{CPUThreshold: 90, MemThreshold: 90, Duration: 5}
	var raw string
	db.QueryRow("SELECT value FROM settings WHERE key = 'load_rule'").Scan(&raw)
	if raw != "" {
		json.Unmarshal([]byte(raw), &r)
	}
	return r
}

func getTrafficRule() TrafficRule {
	r := TrafficRule{DefaultThreshold: 80, Overrides: make(map[string]float64)}
	var raw string
	db.QueryRow("SELECT value FROM settings WHERE key = 'traffic_rule'").Scan(&raw)
	if raw != "" {
		json.Unmarshal([]byte(raw), &r)
	}
	if r.Overrides == nil {
		r.Overrides = make(map[string]float64)
	}
	return r
}

func getExpiryRule() ExpiryRule {
	r := ExpiryRule{DaysBefore: 7}
	var raw string
	db.QueryRow("SELECT value FROM settings WHERE key = 'expiry_rule'").Scan(&raw)
	if raw != "" {
		json.Unmarshal([]byte(raw), &r)
	}
	return r
}

// 读取离线规则：map[node_id]bool（前端按服务器单独开关）
func getOfflineRules() map[string]bool {
	rules := make(map[string]bool)
	var raw string
	db.QueryRow("SELECT value FROM settings WHERE key = 'offline_rules'").Scan(&raw)
	if raw != "" {
		json.Unmarshal([]byte(raw), &rules)
	}
	return rules
}

// ── 告警引擎主循环 ────────────────────────────────────────────────────────────

func startAlertEngine() {
	ticker := time.NewTicker(1 * time.Minute)

	// 内存冷却表，防止同一节点短时间内重复推送
	// key: "nodeID:type"，value: 上次推送的 Unix 时间戳
	cooldown := make(map[string]int64)

	go func() {
		for range ticker.C {
			now := time.Now().Unix()
			checkOfflineAlerts(now, cooldown)
			checkLoadAlerts(now, cooldown)
			checkTrafficAlerts(now, cooldown)
			checkExpiryAlerts(now, cooldown)
		}
	}()
}

// ── 1. 离线告警 ───────────────────────────────────────────────────────────────
// 逻辑：3 分钟内没有上报数据 → 离线
// 尊重前端 offline_rules 里每台服务器的单独开关

func checkOfflineAlerts(now int64, cooldown map[string]int64) {
	// 1. 读取离线判定阈值
    var thresholdStr string
    db.QueryRow("SELECT value FROM settings WHERE key = 'offline_threshold'").Scan(&thresholdStr)
    threshold, _ := strconv.ParseInt(thresholdStr, 10, 64)
    if threshold <= 0 { threshold = 180 } // 保底值

    // 2. 读取告警冷却时间
    var cooldownStr string
    db.QueryRow("SELECT value FROM settings WHERE key = 'offline_cooldown'").Scan(&cooldownStr)
    offlineCooldown, _ := strconv.ParseInt(cooldownStr, 10, 64)
    if offlineCooldown <= 0 { offlineCooldown = 3600 }
	// 全局离线通知总开关
	var globalSwitch string
	db.QueryRow("SELECT value FROM settings WHERE key = 'notify_offline_enable'").Scan(&globalSwitch)
	if globalSwitch != "true" {
		return
	}

	offlineRules := getOfflineRules()

	// 找出 status=online 但 3 分钟无上报的节点
	cutoff := now - threshold
	query := `
		SELECT s.node_id, s.name
		FROM servers s
		LEFT JOIN (
			SELECT node_id, MAX(timestamp) AS last_ts
			FROM metrics GROUP BY node_id
		) m ON s.node_id = m.node_id
		WHERE s.status = 'online'
		  AND (m.last_ts IS NULL OR m.last_ts < ?)
	`
	rows, err := db.Query(query, cutoff)
	if err != nil {
		return
	}

	type NodeInfo struct{ ID, Name string }
	var nodes []NodeInfo
	for rows.Next() {
		var n NodeInfo
		if rows.Scan(&n.ID, &n.Name) == nil {
			nodes = append(nodes, n)
		}
	}
	rows.Close()

	for _, n := range nodes {
		// 将节点标记为离线
		db.Exec("UPDATE servers SET status = 'offline' WHERE node_id = ?", n.ID)

		// 检查该节点是否启用了离线通知（默认 true）
		enabled, exists := offlineRules[n.ID]
		if exists && !enabled {
			continue
		}

		// 冷却：同节点 1 小时内不重复推送离线告警
		key := n.ID + ":offline"
        if last, ok := cooldown[key]; ok && (now-last) < offlineCooldown {
            continue
        }

		// ── 🌟 动态读取并解析离线模板 ──
		var tpl string
		// 从 settings 表读取用户定义的模板
		db.QueryRow("SELECT value FROM settings WHERE key = 'tpl_offline'").Scan(&tpl)

		// 如果数据库中没有配置模板，则使用保底默认值
		if tpl == "" {
			tpl = "🔴 服务器 {name} 已离线\n时间: {time}"
		}

		// 执行变量替换
		// 将模板中的 {name}, {node_id}, {time} 替换为实际数据
		msg := strings.ReplaceAll(tpl, "{name}", n.Name)
		msg = strings.ReplaceAll(msg, "{node_id}", n.ID)
		msg = strings.ReplaceAll(msg, "{time}", time.Now().Format("2006-01-02 15:04:05"))

		sendTelegramMsg(msg)
		// ──────────────────────────────

		insertLog("WARNING", fmt.Sprintf("节点 %s (%s) 已离线", n.Name, n.ID))
		cooldown[key] = now
	}
}

// ── 2. 负载告警（CPU + 内存）─────────────────────────────────────────────────
// 读取前端保存的 load_rule JSON，支持 CPU 阈值和内存阈值

func checkLoadAlerts(now int64, cooldown map[string]int64) {
	rule := getLoadRule()
	if !rule.Enabled {
		return
	}

	// 取最新一条数据（metrics 最近 2 分钟内有上报的在线节点）
	query := `
		SELECT s.node_id, s.name, s.cpu, s.mem
		FROM servers s
		WHERE s.status = 'online'
	`
	rows, err := db.Query(query)
	if err != nil {
		return
	}

	type NodeLoad struct {
		ID, Name string
		CPU, Mem float64
	}
	var nodes []NodeLoad
	for rows.Next() {
		var n NodeLoad
		if rows.Scan(&n.ID, &n.Name, &n.CPU, &n.Mem) == nil {
			nodes = append(nodes, n)
		}
	}
	rows.Close()

	// 冷却时长：rule.Duration 分钟
	cooldownSec := int64(rule.Duration) * 60
	// 移除 300 秒强制限制，改为 60 秒保底（配合 ticker 的 1 分钟轮询）
	if cooldownSec < 60 {
		cooldownSec = 60
	}

	for _, n := range nodes {
		cpuHigh := rule.CPUThreshold > 0 && n.CPU >= rule.CPUThreshold
		memHigh := rule.MemThreshold > 0 && n.Mem >= rule.MemThreshold

		if !cpuHigh && !memHigh {
			continue
		}

		key := n.ID + ":load"
		if last, ok := cooldown[key]; ok && (now-last) < cooldownSec {
			continue
		}

		// 构造详细消息
		var alerts []string
		if cpuHigh {
			alerts = append(alerts, fmt.Sprintf("CPU: %.1f%% (阈值 %.0f%%)", n.CPU, rule.CPUThreshold))
		}
		if memHigh {
			alerts = append(alerts, fmt.Sprintf("内存: %.1f%% (阈值 %.0f%%)", n.Mem, rule.MemThreshold))
		}
		alertStr := ""
		for _, a := range alerts {
			alertStr += "\n⚠ " + a
		}

		msg := fmt.Sprintf(
			"⚠️ <b>节点高负载警告</b>\n\n名称: %s\nID: %s%s\n时间: %s",
			n.Name, n.ID, alertStr, time.Now().Format("2006-01-02 15:04:05"),
		)
		sendTelegramMsg(msg)
		insertLog("WARNING", fmt.Sprintf("节点 %s 负载过高 CPU:%.1f%% MEM:%.1f%%", n.Name, n.CPU, n.Mem))
		cooldown[key] = now
	}
}

// ── 3. 流量告警 ───────────────────────────────────────────────────────────────
// 读取 traffic_rule，根据 monthly_bw（月流量限额 GB）和已用流量计算百分比
// net_rx_total + net_tx_total 为累计总流量（字节），需结合 bw_reset_day 计算当月用量

func checkTrafficAlerts(now int64, cooldown map[string]int64) {
	rule := getTrafficRule()
	if !rule.Enabled || rule.DefaultThreshold <= 0 {
		return
	}

	// 取有流量限额的服务器
	query := `
		SELECT node_id, name, monthly_bw, bw_reset_day, net_rx_total, net_tx_total
		FROM servers
		WHERE status = 'online' AND monthly_bw > 0
	`
	rows, err := db.Query(query)
	if err != nil {
		return
	}

	type NodeTraffic struct {
		ID, Name    string
		MonthlyBW   float64 // GB
		ResetDay    int
		RxTotal     float64 // 字节
		TxTotal     float64 // 字节
	}
	var nodes []NodeTraffic
	for rows.Next() {
		var n NodeTraffic
		if rows.Scan(&n.ID, &n.Name, &n.MonthlyBW, &n.ResetDay, &n.RxTotal, &n.TxTotal) == nil {
			nodes = append(nodes, n)
		}
	}
	rows.Close()

	for _, n := range nodes {
		// 计算当月起始时间戳
		t := time.Now()
		resetDay := n.ResetDay
		if resetDay <= 0 || resetDay > 28 {
			resetDay = 1
		}
		// 月重置日
		monthStart := time.Date(t.Year(), t.Month(), resetDay, 0, 0, 0, 0, t.Location())
		if t.Day() < resetDay {
			// 还没到本月重置日，用上个月的
			monthStart = monthStart.AddDate(0, -1, 0)
		}

		// 查询重置日之后的流量增量（字节）
		var usedBytes float64
		err := db.QueryRow(`
			SELECT COALESCE(
				(SELECT net_rx_total + net_tx_total FROM metrics 
				 WHERE node_id = ? ORDER BY timestamp DESC LIMIT 1), 0
			) - COALESCE(
				(SELECT net_rx_total + net_tx_total FROM metrics 
				 WHERE node_id = ? AND timestamp <= ? ORDER BY timestamp DESC LIMIT 1), 0
			)
		`, n.ID, n.ID, monthStart.Unix()).Scan(&usedBytes)

		if err != nil || usedBytes < 0 {
			// 降级：直接用 servers 表缓存的总流量（探针每次上报的累计值）
			// 这种情况下无法精确计算当月，仅用总量与限额对比做粗略判断
			usedBytes = n.RxTotal + n.TxTotal
		}

		usedGB := usedBytes / 1024 / 1024 / 1024
		pct := usedGB / n.MonthlyBW * 100

		// 🌟 核心修改：优先使用节点的独立阈值，没有则使用全局默认阈值
		threshold := rule.DefaultThreshold
		if overrideVal, exists := rule.Overrides[n.ID]; exists && overrideVal > 0 {
			threshold = overrideVal
		}

		// 判断是否超过对应的阈值
		if pct < threshold {
			continue
		}

		// 按阈值档位做冷却区分（每档每天最多一次）
		tier := strconv.Itoa(int(pct/10) * 10) // 80, 90, 95...
		key := n.ID + ":traffic:" + tier
		if last, ok := cooldown[key]; ok && (now-last) < 86400 {
			continue
		}

		msg := fmt.Sprintf(
			"📊 <b>流量使用预警</b>\n\n名称: %s\n已用: %.2f GB / %.0f GB (%.1f%%)\n时间: %s",
			n.Name, usedGB, n.MonthlyBW, pct, time.Now().Format("2006-01-02 15:04:05"),
		)
		sendTelegramMsg(msg)
		insertLog("WARNING", fmt.Sprintf("节点 %s 流量已用 %.1f%%", n.Name, pct))
		cooldown[key] = now
	}
}

// ── 4. 过期提醒 ───────────────────────────────────────────────────────────────
// 读取 expiry_rule，根据 servers.billing_date 判断距离到期天数

func checkExpiryAlerts(now int64, cooldown map[string]int64) {
	rule := getExpiryRule()
	if !rule.Enabled || rule.DaysBefore <= 0 {
		return
	}

	rows, err := db.Query(`
		SELECT node_id, name, billing_date
		FROM servers
		WHERE billing_date IS NOT NULL AND billing_date != ''
	`)
	if err != nil {
		return
	}

	type NodeExpiry struct {
		ID, Name, BillingDate string
	}
	var nodes []NodeExpiry
	for rows.Next() {
		var n NodeExpiry
		if rows.Scan(&n.ID, &n.Name, &n.BillingDate) == nil {
			nodes = append(nodes, n)
		}
	}
	rows.Close()

	today := time.Now().Truncate(24 * time.Hour)

	for _, n := range nodes {
		// billing_date 格式兼容 "2006-01-02" 和 "2006/01/02"
		billingDate, err := parseDate(n.BillingDate)
		if err != nil {
			continue
		}

		// 计算今年（或下一个）账单日
		nextBilling := time.Date(today.Year(), billingDate.Month(), billingDate.Day(), 0, 0, 0, 0, today.Location())
		if nextBilling.Before(today) {
			nextBilling = nextBilling.AddDate(1, 0, 0)
		}

		daysLeft := int(nextBilling.Sub(today).Hours() / 24)
		if daysLeft > rule.DaysBefore {
			continue
		}

		// 每天最多通知一次
		key := n.ID + ":expiry"
		if last, ok := cooldown[key]; ok && (now-last) < 86400 {
			continue
		}

		emoji := "📅"
		urgency := "即将到期"
		if daysLeft <= 3 {
			emoji = "🚨"
			urgency = "紧急：即将到期"
		}

		msg := fmt.Sprintf(
			"%s <b>服务器%s提醒</b>\n\n名称: %s\n到期日: %s\n剩余: %d 天\n时间: %s",
			emoji, urgency,
			n.Name, nextBilling.Format("2006-01-02"), daysLeft,
			time.Now().Format("2006-01-02 15:04:05"),
		)
		sendTelegramMsg(msg)
		insertLog("INFO", fmt.Sprintf("节点 %s 账单将于 %d 天后到期", n.Name, daysLeft))
		cooldown[key] = now
	}
}

// ── 工具函数 ──────────────────────────────────────────────────────────────────

func parseDate(s string) (time.Time, error) {
	// 尝试常见格式
	formats := []string{"2006-01-02", "2006/01/02", "01/02/2006", "02-01-2006"}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析日期: %s", s)
}

// ── 数据保留任务（保持不变）──────────────────────────────────────────────────

func startDataRetentionTask(retentionDays int) {
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			cutoff := time.Now().Unix() - int64(retentionDays*24*3600)
			result, err := db.Exec("DELETE FROM metrics WHERE timestamp < ?", cutoff)
			if err != nil {
				log.Printf("清理过期数据失败: %v\n", err)
				continue
			}
			rowsAffected, _ := result.RowsAffected()
			if rowsAffected > 0 {
				log.Printf("系统任务: 已清理 %d 条过期数据\n", rowsAffected)
			}
		}
	}()
}