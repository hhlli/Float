package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"Float/internal/database"
)

type ExtensionInfo struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Version          string   `json:"version"`
	DownloadURL      string   `json:"download_url"`
	RequirePrivilege bool     `json:"require_privilege"`
	InstalledNodes   []string `json:"installed_nodes"`
}

var availableExtensions = []ExtensionInfo{
	{
		ID:               "mtr-plugin",
		Name:             "MTR 路由追踪",
		Description:      "提供从探针节点到目标 IP 的全链路 MTR 路由追踪诊断能力。",
		Version:          "v1.0.0",
		DownloadURL:      "https://mirror.ghproxy.com/https://github.com/hhlli/float-mtr-plugin/releases/latest/download/float-mtr-plugin",
		RequirePrivilege: true,
		InstalledNodes:   []string{},
	},
}

// 辅助函数：从数据库读取指定插件的已安装节点列表
func getInstalledNodes(extID string) []string {
	var val string
	err := database.DB.QueryRow("SELECT value FROM settings WHERE key = ?", "ext_"+extID).Scan(&val)
	var nodes []string
	if err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), &nodes)
	}
	if nodes == nil {
		nodes = []string{}
	}
	return nodes
}

// 辅助函数：将节点列表保存至数据库
func saveInstalledNodes(extID string, nodes []string) error {
	b, _ := json.Marshal(nodes)
	_, err := database.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", "ext_"+extID, string(b))
	return err
}

// processExtensionInstallSuccess 探针回传安装成功后，真实写入数据库 (闭环落地)
func processExtensionInstallSuccess(nodeID, extID string) {
	nodes := getInstalledNodes(extID)
	for _, n := range nodes {
		if n == nodeID {
			return // 已存在
		}
	}
	nodes = append(nodes, nodeID)
	saveInstalledNodes(extID, nodes)
}

// processExtensionUninstallSuccess 探针回传卸载成功后，从数据库剔除 (闭环落地)
func processExtensionUninstallSuccess(nodeID, extID string) {
	nodes := getInstalledNodes(extID)
	var keptNodes []string
	for _, n := range nodes {
		if n != nodeID {
			keptNodes = append(keptNodes, n)
		}
	}
	saveInstalledNodes(extID, keptNodes)
}

// SyncNodeExtensions 供新探针上线时调用，检查自身是否在安装列表中
func SyncNodeExtensions(nodeID string) {
	for _, ext := range availableExtensions {
		nodes := getInstalledNodes(ext.ID)
		for _, id := range nodes {
			if id == nodeID {
				agentConnsMu.RLock()
				ac, ok := agentConns[nodeID]
				agentConnsMu.RUnlock()

				if ok {
					msg := RPCRequest{
						JSONRPC: "2.0",
						Method:  "extension.install",
						Params: mustMarshal(map[string]interface{}{
							"id":                ext.ID,
							"download_url":      ext.DownloadURL,
							"require_privilege": ext.RequirePrivilege,
						}),
						ID: time.Now().UnixNano(),
					}
					ac.mu.Lock()
					_ = ac.conn.WriteJSON(msg)
					ac.mu.Unlock()
				}
				break
			}
		}
	}
}

// ApiListExtensionsHandler 获取插件列表及安装节点
func ApiListExtensionsHandler(w http.ResponseWriter, r *http.Request) {
	exts := make([]ExtensionInfo, len(availableExtensions))
	copy(exts, availableExtensions)

	for i := range exts {
		exts[i].InstalledNodes = getInstalledNodes(exts[i].ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(exts)
}

// ApiInstallExtensionHandler 按节点列表安装插件 (剥离了直接落库逻辑)
func ApiInstallExtensionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID          string   `json:"id"`
		TargetNodes []string `json:"target_nodes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" || len(req.TargetNodes) == 0 {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	var currentExt ExtensionInfo
	found := false
	for _, e := range availableExtensions {
		if e.ID == req.ID {
			currentExt = e
			found = true
			break
		}
	}

	if !found {
		http.Error(w, "Unknown extension ID", http.StatusBadRequest)
		return
	}

	// 仅向本次选中的在线节点下发安装指令，不再直接写入数据库
	go func(ext ExtensionInfo, targets []string) {
		agentConnsMu.RLock()
		defer agentConnsMu.RUnlock()

		msg := RPCRequest{
			JSONRPC: "2.0",
			Method:  "extension.install",
			Params: mustMarshal(map[string]interface{}{
				"id":                ext.ID,
				"download_url":      ext.DownloadURL,
				"require_privilege": ext.RequirePrivilege,
			}),
			ID: time.Now().UnixNano(),
		}

		for _, nodeID := range targets {
			if ac, ok := agentConns[nodeID]; ok {
				go func(conn *AgentConn) {
					conn.mu.Lock()
					defer conn.mu.Unlock()
					_ = conn.conn.WriteJSON(msg)
				}(ac)
			}
		}
	}(currentExt, req.TargetNodes)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

// ApiUninstallExtensionHandler 按节点列表卸载插件 (剥离了直接落库逻辑)
func ApiUninstallExtensionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID          string   `json:"id"`
		TargetNodes []string `json:"target_nodes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" || len(req.TargetNodes) == 0 {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 仅向本次选中的在线节点下发卸载指令，不再直接写入数据库
	go func(extID string, targets []string) {
		agentConnsMu.RLock()
		defer agentConnsMu.RUnlock()

		msg := RPCRequest{
			JSONRPC: "2.0",
			Method:  "extension.uninstall",
			Params:  mustMarshal(map[string]interface{}{"id": extID}),
			ID:      time.Now().UnixNano(),
		}

		for _, nodeID := range targets {
			if ac, ok := agentConns[nodeID]; ok {
				go func(conn *AgentConn) {
					conn.mu.Lock()
					defer conn.mu.Unlock()
					_ = conn.conn.WriteJSON(msg)
				}(ac)
			}
		}
	}(req.ID, req.TargetNodes)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}