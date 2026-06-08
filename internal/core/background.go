package core

import (
	// "bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"Float/internal/database" // 引入 database 包
	"Float/internal/notify"
	"Float/internal/logger"
	"go.uber.org/zap"
)

// ── 辅助函数：批量读取 Settings，减少数据库 I/O ────────────────────────────────

func getSettings(keys ...string) map[string]string {
	settings := make(map[string]string)
	if len(keys) == 0 {
		return settings
	}

	placeholders := make([]string, len(keys))
	args := make([]interface{}, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}

	query := fmt.Sprintf("SELECT key, value FROM settings WHERE key IN (%s)", strings.Join(placeholders, ","))
	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return settings
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			settings[k] = v
		}
	}
	return settings
}

// ── 规则结构体 ────────────────────────────────────────────────────────────────

type LoadRule struct {
	Enabled      bool    `json:"enabled"`
	CPUThreshold float64 `json:"cpu_threshold"`
	MemThreshold float64 `json:"mem_threshold"`
	Duration     int     `json:"duration"`
}

type TrafficRule struct {
	Enabled          bool               `json:"enabled"`
	DefaultThreshold float64            `json:"default_threshold"`
	Overrides        map[string]float64 `json:"overrides"`
}

type ExpiryRule struct {
	Enabled    bool `json:"enabled"`
	DaysBefore int  `json:"days_before"`
}

func getLoadRule() LoadRule {
	r := LoadRule{CPUThreshold: 90, MemThreshold: 90, Duration: 5}
	if raw := getSettings("load_rule")["load_rule"]; raw != "" {
		json.Unmarshal([]byte(raw), &r)
	}
	return r
}

func getTrafficRule() TrafficRule {
	r := TrafficRule{DefaultThreshold: 80, Overrides: make(map[string]float64)}
	if raw := getSettings("traffic_rule")["traffic_rule"]; raw != "" {
		json.Unmarshal([]byte(raw), &r)
	}
	if r.Overrides == nil {
		r.Overrides = make(map[string]float64)
	}
	return r
}

func getExpiryRule() ExpiryRule {
	r := ExpiryRule{DaysBefore: 7}
	if raw := getSettings("expiry_rule")["expiry_rule"]; raw != "" {
		json.Unmarshal([]byte(raw), &r)
	}
	return r
}

func getOfflineRules() map[string]bool {
	rules := make(map[string]bool)
	if raw := getSettings("offline_rules")["offline_rules"]; raw != "" {
		json.Unmarshal([]byte(raw), &rules)
	}
	return rules
}

// ── 告警引擎主循环 ────────────────────────────────────────────────────────────

func StartAlertEngine() {
	ticker := time.NewTicker(1 * time.Minute)
	cooldown := make(map[string]int64)

	go func() {
		for range ticker.C {
			now := time.Now().Unix()
			
			// 👇 新增：每分钟将当前在线的节点，在 daily_stats 中的当日在线分钟数 +1
			database.DB.Exec(`
				INSERT INTO daily_stats (node_id, date, online_mins)
				SELECT node_id, date('now', 'localtime'), 1 FROM servers WHERE status = 'online'
				ON CONFLICT(node_id, date) DO UPDATE SET online_mins = daily_stats.online_mins + 1
			`)
			
			// 👇 新增：在检测离线前，先检测是否有机器恢复上线
			checkRecoveryAlerts(now, cooldown) 
			checkOfflineAlerts(now, cooldown)
			checkLoadAlerts(now, cooldown)
			checkTrafficAlerts(now, cooldown)
			checkExpiryAlerts(now, cooldown)

			// 清理内存：定期修剪超过 24 小时未触发的冷却记录
			if now%3600 == 0 { // 每小时执行一次清理
				for k, lastTs := range cooldown {
					// 👇 修改：跳过 :offline 类型的记录，确保离线很久的机器上线后依然能触发恢复通知
					if !strings.HasSuffix(k, ":offline") && now-lastTs > 86400 {
						delete(cooldown, k)
					}
				}
			}
		}
	}()
}

// ── 1. 离线告警 ───────────────────────────────────────────────────────────────

func checkOfflineAlerts(now int64, cooldown map[string]int64) {
	settings := getSettings("offline_threshold", "offline_cooldown", "notify_offline_enable", "tpl_offline")

	if settings["notify_offline_enable"] != "true" {
		return
	}

	threshold, _ := strconv.ParseInt(settings["offline_threshold"], 10, 64)
	if threshold <= 0 {
		threshold = 180
	}

	offlineCooldown, _ := strconv.ParseInt(settings["offline_cooldown"], 10, 64)
	if offlineCooldown <= 0 {
		offlineCooldown = 3600
	}

	offlineRules := getOfflineRules()
	cutoff := now - threshold

	// 优化：直接查 servers 表的 last_active，避免全表扫描 metrics
	query := `SELECT node_id, name FROM servers WHERE status = 'online' AND last_active < ?`
	rows, err := database.DB.Query(query, cutoff)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) != nil {
			continue
		}

		database.DB.Exec("UPDATE servers SET status = 'offline' WHERE node_id = ?", id)

		if enabled, exists := offlineRules[id]; exists && !enabled {
			continue
		}

		key := id + ":offline"
		if last, ok := cooldown[key]; ok && (now-last) < offlineCooldown {
			continue
		}

		tpl := settings["tpl_offline"]
		if tpl == "" {
			tpl = "🔴 服务器 {name} 已离线\n时间: {time}"
		}

		msg := strings.ReplaceAll(tpl, "{name}", name)
		msg = strings.ReplaceAll(msg, "{node_id}", id)
		msg = strings.ReplaceAll(msg, "{time}", time.Now().Format("2006-01-02 15:04:05"))

		notify.Dispatch("", msg)
		database.InsertLog("WARNING", fmt.Sprintf("节点 %s (%s) 已离线", name, id))
		cooldown[key] = now
	}
}
// ── 1.5 恢复上线通知 ──────────────────────────────────────────────────────────

func checkRecoveryAlerts(now int64, cooldown map[string]int64) {
	settings := getSettings("notify_offline_enable", "tpl_online")

	// 如果全局关闭了离线通知，恢复通知也一并静默
	if settings["notify_offline_enable"] != "true" {
		return
	}

	// 1. 从 cooldown 内存中找出所有近期触发过“离线告警”的节点 ID
	var offlineNodeIDs []string
	for key := range cooldown {
		if strings.HasSuffix(key, ":offline") {
			id := strings.TrimSuffix(key, ":offline")
			offlineNodeIDs = append(offlineNodeIDs, id)
		}
	}

	if len(offlineNodeIDs) == 0 {
		return
	}

	// 2. 去数据库查询这些节点，看看哪些状态已经变回 'online'
	placeholders := make([]string, len(offlineNodeIDs))
	args := make([]interface{}, len(offlineNodeIDs))
	for i, id := range offlineNodeIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf("SELECT node_id, name FROM servers WHERE status = 'online' AND node_id IN (%s)", strings.Join(placeholders, ","))
	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return
	}
	defer rows.Close()

	// 3. 遍历已恢复的节点，发送上线通知并清理离线标记
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			tpl := settings["tpl_online"]
			if tpl == "" {
				tpl = "🟢 服务器 {name} 已恢复上线\n时间: {time}"
			}

			msg := strings.ReplaceAll(tpl, "{name}", name)
			msg = strings.ReplaceAll(msg, "{node_id}", id)
			msg = strings.ReplaceAll(msg, "{time}", time.Now().Format("2006-01-02 15:04:05"))

			// 发送通知并记录日志
			notify.Dispatch("", msg)
			database.InsertLog("INFO", fmt.Sprintf("节点 %s (%s) 已恢复上线", name, id))

			// 🌟 核心：移除离线冷却标记。这样它不仅能退出恢复检测循环，下次若再掉线也能正常触发离线通知
			delete(cooldown, id+":offline")
		}
	}
}
// ── 2. 负载告警 ───────────────────────────────────────────────────────────────

func checkLoadAlerts(now int64, cooldown map[string]int64) {
	rule := getLoadRule()
	if !rule.Enabled {
		return
	}

	query := `SELECT node_id, name, cpu, mem FROM servers WHERE status = 'online'`
	rows, err := database.DB.Query(query)
	if err != nil {
		return
	}
	defer rows.Close()

	cooldownSec := int64(rule.Duration) * 60
	if cooldownSec < 60 {
		cooldownSec = 60
	}

	for rows.Next() {
		var id, name string
		var cpu, mem float64
		if rows.Scan(&id, &name, &cpu, &mem) != nil {
			continue
		}

		cpuHigh := rule.CPUThreshold > 0 && cpu >= rule.CPUThreshold
		memHigh := rule.MemThreshold > 0 && mem >= rule.MemThreshold

		if !cpuHigh && !memHigh {
			continue
		}

		key := id + ":load"
		if last, ok := cooldown[key]; ok && (now-last) < cooldownSec {
			continue
		}

		var alerts []string
		if cpuHigh {
			alerts = append(alerts, fmt.Sprintf("CPU: %.1f%% (阈值 %.0f%%)", cpu, rule.CPUThreshold))
		}
		if memHigh {
			alerts = append(alerts, fmt.Sprintf("内存: %.1f%% (阈值 %.0f%%)", mem, rule.MemThreshold))
		}

		msg := fmt.Sprintf(
			"⚠️ <b>节点高负载警告</b>\n\n名称: %s\nID: %s\n%s\n时间: %s",
			name, id, "⚠ "+strings.Join(alerts, "\n⚠ "), time.Now().Format("2006-01-02 15:04:05"),
		)
		notify.Dispatch("", msg)
		database.InsertLog("WARNING", fmt.Sprintf("节点 %s 负载过高 CPU:%.1f%% MEM:%.1f%%", name, cpu, mem))
		cooldown[key] = now
	}
}

// ── 3. 流量告警 ───────────────────────────────────────────────────────────────

func checkTrafficAlerts(now int64, cooldown map[string]int64) {
	rule := getTrafficRule()
	if !rule.Enabled || rule.DefaultThreshold <= 0 {
		return
	}

	query := `SELECT node_id, name, monthly_bw, bw_reset_day, net_rx_total, net_tx_total FROM servers WHERE status = 'online' AND monthly_bw > 0`
	rows, err := database.DB.Query(query)
	if err != nil {
		return
	}
	defer rows.Close()

	t := time.Now()
	for rows.Next() {
		var id, name string
		var monthlyBW, rxTotal, txTotal float64
		var resetDay int

		if rows.Scan(&id, &name, &monthlyBW, &resetDay, &rxTotal, &txTotal) != nil {
			continue
		}

		if resetDay <= 0 || resetDay > 28 {
			resetDay = 1
		}

		monthStart := time.Date(t.Year(), t.Month(), resetDay, 0, 0, 0, 0, t.Location())
		if t.Day() < resetDay {
			monthStart = monthStart.AddDate(0, -1, 0)
		}

		var usedBytes float64
		err := database.DB.QueryRow(`
			SELECT COALESCE(
				(SELECT net_rx_total + net_tx_total FROM metrics WHERE node_id = ? ORDER BY timestamp DESC LIMIT 1), 0
			) - COALESCE(
				(SELECT net_rx_total + net_tx_total FROM metrics WHERE node_id = ? AND timestamp <= ? ORDER BY timestamp DESC LIMIT 1), 0
			)
		`, id, id, monthStart.Unix()).Scan(&usedBytes)

		if err != nil || usedBytes < 0 {
			usedBytes = rxTotal + txTotal
		}

		usedGB := usedBytes / 1024 / 1024 / 1024
		pct := usedGB / monthlyBW * 100

		threshold := rule.DefaultThreshold
		if overrideVal, exists := rule.Overrides[id]; exists && overrideVal > 0 {
			threshold = overrideVal
		}

		if pct < threshold {
			continue
		}

		tier := strconv.Itoa(int(pct/10) * 10)
		key := id + ":traffic:" + tier
		if last, ok := cooldown[key]; ok && (now-last) < 86400 {
			continue
		}

		msg := fmt.Sprintf(
			"📊 <b>流量使用预警</b>\n\n名称: %s\n已用: %.2f GB / %.0f GB (%.1f%%)\n时间: %s",
			name, usedGB, monthlyBW, pct, time.Now().Format("2006-01-02 15:04:05"),
		)
		notify.Dispatch("", msg)
		database.InsertLog("WARNING", fmt.Sprintf("节点 %s 流量已用 %.1f%%", name, pct))
		cooldown[key] = now
	}
}

// ── 4. 过期提醒 ───────────────────────────────────────────────────────────────

func checkExpiryAlerts(now int64, cooldown map[string]int64) {
	rule := getExpiryRule()
	if !rule.Enabled || rule.DaysBefore <= 0 {
		return
	}

	query := `SELECT node_id, name, billing_date FROM servers WHERE billing_date IS NOT NULL AND billing_date != ''`
	rows, err := database.DB.Query(query)
	if err != nil {
		return
	}
	defer rows.Close()

	today := time.Now().Truncate(24 * time.Hour)

	for rows.Next() {
		var id, name, billingDateStr string
		if rows.Scan(&id, &name, &billingDateStr) != nil {
			continue
		}

		billingDate, err := parseDate(billingDateStr)
		if err != nil {
			continue
		}

		nextBilling := time.Date(today.Year(), billingDate.Month(), billingDate.Day(), 0, 0, 0, 0, today.Location())
		if nextBilling.Before(today) {
			nextBilling = nextBilling.AddDate(1, 0, 0)
		}

		daysLeft := int(nextBilling.Sub(today).Hours() / 24)
		if daysLeft > rule.DaysBefore {
			continue
		}

		key := id + ":expiry"
		if last, ok := cooldown[key]; ok && (now-last) < 86400 {
			continue
		}

		emoji, urgency := "📅", "即将到期"
		if daysLeft <= 3 {
			emoji, urgency = "🚨", "紧急：即将到期"
		}

		msg := fmt.Sprintf(
			"%s <b>服务器%s提醒</b>\n\n名称: %s\n到期日: %s\n剩余: %d 天\n时间: %s",
			emoji, urgency, name, nextBilling.Format("2006-01-02"), daysLeft, time.Now().Format("2006-01-02 15:04:05"),
		)
		notify.Dispatch("", msg)
		database.InsertLog("INFO", fmt.Sprintf("节点 %s 账单将于 %d 天后到期", name, daysLeft))
		cooldown[key] = now
	}
}

// ── 工具函数 ──────────────────────────────────────────────────────────────────

func parseDate(s string) (time.Time, error) {
	formats := []string{"2006-01-02", "2006/01/02", "01/02/2006", "02-01-2006"}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析日期: %s", s)
}

// ── 数据保留任务 ──────────────────────────────────────────────────────────────

// ── 数据保留任务 ──────────────────────────────────────────────────────────────

func StartDataRetentionTask() { // 注意：移除了传参，改为内部动态获取配置
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			// 动态获取最新配置
			settings := getSettings("enable_history", "load_retention_days", "ping_retention_days")
			
			enableHistory := settings["enable_history"] == "true"
			
			loadDays, _ := strconv.Atoi(settings["load_retention_days"])
			if loadDays <= 0 { loadDays = 7 }
			
			pingDays, _ := strconv.Atoi(settings["ping_retention_days"])
			if pingDays <= 0 { pingDays = 7 }

			var loadCutoff, pingCutoff int64

			if !enableHistory {
				// 如果关闭了历史记录，仅保留最近 1 小时的数据供实时面板显示
				loadCutoff = time.Now().Unix() - 3600
				pingCutoff = time.Now().Unix() - 3600
			} else {
				// 按照设定的天数计算过期时间戳
				loadCutoff = time.Now().Unix() - int64(loadDays*24*3600)
				pingCutoff = time.Now().Unix() - int64(pingDays*24*3600)
			}

			// 清理负载过期数据 (metrics 表)
			resMetrics, err1 := database.DB.Exec("DELETE FROM metrics WHERE timestamp < ?", loadCutoff)
			// 清理 Ping 过期数据 (task_results 表)
			resTasks, err2 := database.DB.Exec("DELETE FROM task_results WHERE timestamp < ?", pingCutoff)

			if err1 != nil || err2 != nil {
				logger.Log.Error("清理过期数据出现错误", 
					zap.String("module", "Retention"),
					zap.NamedError("metrics_err", err1),
					zap.NamedError("tasks_err", err2),
				)
				continue
			}

			rowsMetrics, _ := resMetrics.RowsAffected()
			rowsTasks, _ := resTasks.RowsAffected()

			if rowsMetrics > 0 || rowsTasks > 0 {
				logger.Log.Info("已清理过期数据", 
					zap.String("module", "Retention"),
					zap.Int64("metrics_deleted", rowsMetrics),
					zap.Int64("tasks_deleted", rowsTasks),
				)
			}
		}
	}()
}

func StartVersionCheckTask() {
	checkLatestVersion()
	ticker := time.NewTicker(12 * time.Hour)
	go func() {
		for range ticker.C {
			checkLatestVersion()
		}
	}()
}

func checkLatestVersion() {
	resp, err := http.Get("https://api.github.com/repos/hhlli/Float/releases/latest")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var release struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&release); err == nil && release.TagName != "" {
			database.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('latest_version', ?)", release.TagName)
		}
	}
}