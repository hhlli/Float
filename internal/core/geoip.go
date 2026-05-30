package core

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"Float/internal/database"
	"Float/internal/logger"
	"go.uber.org/zap"

	"github.com/oschwald/geoip2-golang"
)

const LocalDBPath = "./data/GeoLite2-Country.mmdb"

func GetClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Real-IP")
	if ip == "" || isPrivateIP(ip) { // 检查 X-Real-IP 是否为内网
		xff := r.Header.Get("X-Forwarded-For")
		if xff != "" {
			ips := strings.Split(xff, ",")
			for _, p := range ips {
				p = strings.TrimSpace(p)
				if !isPrivateIP(p) {
					return p // 返回第一个公网 IP
				}
			}
		}
	}
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if strings.Contains(ip, ",") {
		ip = strings.TrimSpace(strings.Split(ip, ",")[0])
	}
	return ip
}

// FetchAndSaveGeoIP 核心解析入口
func FetchAndSaveGeoIP(nodeID, ip string) {
	if isPrivateIP(ip) {
		return
	}

	// 检查全局开关
	settings := getSettings("geoip_enabled", "geoip_provider")
	if settings["geoip_enabled"] != "true" {
		return
	}

	var currentRegion string
	err := database.DB.QueryRow("SELECT region FROM servers WHERE node_id = ?", nodeID).Scan(&currentRegion)
	if err == nil && currentRegion != "" && currentRegion != "UN" && len(currentRegion) == 2 {
		return
	}

	result, err := ParseIPLocation(ip, settings["geoip_provider"])
	if err != nil {
		logger.Log.Error("GeoIP 解析失败",
			zap.String("module", "GeoIP"),
			zap.String("ip", ip),
			zap.Error(err),
		)
		return
	}

if result.CountryCode != "" {
    database.DB.Exec(
        "UPDATE servers SET region = ?, latitude = ?, longitude = ? WHERE node_id = ?",
        result.CountryCode, result.Lat, result.Lon, nodeID,
    )
    logger.Log.Info("GeoIP 解析成功",
    zap.String("module", "GeoIP"),
    zap.String("node_id", nodeID),
    zap.String("ip", ip),
    zap.String("country_code", result.CountryCode),
    zap.Float64("lat", result.Lat),
    zap.Float64("lon", result.Lon),
)
}
}

// ParseIPLocation 根据指定服务商解析 IP、经纬度
type GeoResult struct {
    CountryCode string
    Lat         float64
    Lon         float64
}

func ParseIPLocation(ip, provider string) (*GeoResult, error) {
    if provider == "maxmind" {
        code, err := parseMaxMind(ip)
        if err != nil {
            return nil, err
        }
        return &GeoResult{CountryCode: code}, nil  // maxmind 只有国家代码，经纬度为0
    }
    return parseIPAPI(ip)
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

func parseIPAPI(ip string) (*GeoResult, error) {
    resp, err := http.Get("http://ip-api.com/json/" + ip)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var result struct {
        Status      string  `json:"status"`
        CountryCode string  `json:"countryCode"`
        Lat         float64 `json:"lat"`
        Lon         float64 `json:"lon"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }
    if result.Status != "success" {
        return nil, fmt.Errorf("ip-api status: %s", result.Status)
    }
    return &GeoResult{
        CountryCode: result.CountryCode,
        Lat:         result.Lat,
        Lon:         result.Lon,
    }, nil
}

func parseMaxMind(ip string) (string, error) {
	if _, err := os.Stat(LocalDBPath); os.IsNotExist(err) {
		return "", errors.New("本地 GeoIP 数据库文件不存在，请先更新数据库")
	}

	db, err := geoip2.Open(LocalDBPath)
	if err != nil {
		return "", err
	}
	defer db.Close()

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return "", errors.New("无效的 IP 格式")
	}

	record, err := db.Country(parsedIP)
	if err != nil {
		return "", err
	}
	return record.Country.IsoCode, nil
}

// UpdateGeoIPDB 从 MaxMind 官方下载并解压最新的数据库文件
func UpdateGeoIPDB(licenseKey string) error {
	if licenseKey == "" {
		return errors.New("License Key 不能为空")
	}

	url := fmt.Sprintf("https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=%s&suffix=tar.gz", licenseKey)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，状态码: %d", resp.StatusCode)
	}

	gzipReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if strings.HasSuffix(header.Name, ".mmdb") {
			if err := os.MkdirAll(filepath.Dir(LocalDBPath), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(LocalDBPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return err
			}
			defer outFile.Close()

			if _, err := io.Copy(outFile, tarReader); err != nil {
				return err
			}
			return nil
		}
	}

	return errors.New("未在压缩包中找到 .mmdb 文件")
}