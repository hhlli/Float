package handlers

import (
	"encoding/json"
	"net/http"

	"Float/internal/database"
)

type ExtensionInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
}

// 预设的可安装插件列表
var availableExtensions = []ExtensionInfo{
	{
		ID:          "mtr-plugin",
		Name:        "MTR 路由追踪",
		Description: "提供从探针节点到目标 IP 的全链路 MTR 路由追踪诊断能力。",
		Version:     "v1.0.0",
		Installed:   false,
	},
}

// [API] 获取插件列表与状态
func ApiListExtensionsHandler(w http.ResponseWriter, r *http.Request) {
	exts := make([]ExtensionInfo, len(availableExtensions))
	copy(exts, availableExtensions)

	// 从 settings 表中读取安装状态
	for i := range exts {
		var status string
		err := database.DB.QueryRow("SELECT value FROM settings WHERE key = ?", "ext_"+exts[i].ID).Scan(&status)
		if err == nil && status == "installed" {
			exts[i].Installed = true
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(exts)
}

// [API] 安装插件
func ApiInstallExtensionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 记录安装状态
	// TODO: 如需实现服务端自动分发，可在此处追加二进制文件下载或 WebSocket 安装指令下发逻辑
	database.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", "ext_"+req.ID, "installed")

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

// [API] 卸载插件
func ApiUninstallExtensionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 清除安装状态
	database.DB.Exec("DELETE FROM settings WHERE key = ?", "ext_"+req.ID)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}