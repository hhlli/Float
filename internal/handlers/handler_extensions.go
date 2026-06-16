package handlers

import (
	"encoding/json"
	"net/http"

	"Float/internal/database"
)

type ExtensionInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Version        string   `json:"version"`
	InstalledNodes []string `json:"installed_nodes"` // 变更为数组
}

var availableExtensions = []ExtensionInfo{
	{
		ID:             "mtr-plugin",
		Name:           "MTR 路由追踪",
		Description:    "提供从探针节点到目标 IP 的全链路 MTR 路由追踪诊断能力。",
		Version:        "v1.0.0",
		InstalledNodes: []string{},
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
						Params:  mustMarshal(map[string]string{"id": ext.ID}),
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

// ApiInstallExtensionHandler 按节点列表安装插件
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

	// 计算并集
	existingNodes := getInstalledNodes(req.ID)
	nodeMap := make(map[string]bool)
	for _, n := range existingNodes {
		nodeMap[n] = true
	}
	for _, n := range req.TargetNodes {
		nodeMap[n] = true
	}
	var mergedNodes []string
	for n := range nodeMap {
		mergedNodes = append(mergedNodes, n)
	}

	if err := saveInstalledNodes(req.ID, mergedNodes); err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// 仅向本次选中的在线节点下发安装指令
	go func(extID string, targets []string) {
		agentConnsMu.RLock()
		defer agentConnsMu.RUnlock()

		msg := RPCRequest{
			JSONRPC: "2.0",
			Method:  "extension.install",
			Params:  mustMarshal(map[string]string{"id": extID}),
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

// ApiUninstallExtensionHandler 按节点列表卸载插件
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

	// 计算差集
	existingNodes := getInstalledNodes(req.ID)
	removeMap := make(map[string]bool)
	for _, n := range req.TargetNodes {
		removeMap[n] = true
	}
	var keptNodes []string
	for _, n := range existingNodes {
		if !removeMap[n] {
			keptNodes = append(keptNodes, n)
		}
	}

	if err := saveInstalledNodes(req.ID, keptNodes); err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// 仅向本次选中的在线节点下发卸载指令
	go func(extID string, targets []string) {
		agentConnsMu.RLock()
		defer agentConnsMu.RUnlock()

		msg := RPCRequest{
			JSONRPC: "2.0",
			Method:  "extension.uninstall",
			Params:  mustMarshal(map[string]string{"id": extID}),
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