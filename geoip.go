package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
)

func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Real-IP")
	if ip == "" {
		ip = r.Header.Get("X-Forwarded-For")
	}
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if strings.Contains(ip, ",") {
		ip = strings.TrimSpace(strings.Split(ip, ",")[0])
	}
	return ip
}

func fetchAndSaveGeoIP(nodeID, ip string) {
	if ip == "" || ip == "127.0.0.1" || ip == "::1" || strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") {
		return
	}

	var currentRegion string
	err := db.QueryRow("SELECT region FROM servers WHERE node_id = ?", nodeID).Scan(&currentRegion)
	if err == nil && currentRegion != "" && currentRegion != "UN" {
		return
	}

	resp, err := http.Get("http://ip-api.com/json/" + ip)
	if err != nil {
		log.Println("[GeoIP] 请求失败:", err)
		return
	}
	defer resp.Body.Close()

	var result struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.Status == "success" {
		if result.CountryCode != "" {
			db.Exec("UPDATE servers SET region = ? WHERE node_id = ?", result.CountryCode, nodeID)
			log.Printf("[GeoIP] 节点 %s 解析成功: %s (%s)\n", nodeID, ip, result.CountryCode)
		}
	}
}