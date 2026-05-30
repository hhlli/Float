package core

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type loginAttempt struct {
	count     int
	lockUntil time.Time
}

var (
	attemptsMap  sync.Map
	maxAttempts  = 5
	lockDuration = 15 * time.Minute
)

func init() {
	// 后台 GC 协程，每 5 分钟清理一次已过期的 IP 记录，防止内存泄漏
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			now := time.Now()
			attemptsMap.Range(func(key, value interface{}) bool {
				att := value.(*loginAttempt)
				if att.count >= maxAttempts && now.After(att.lockUntil) {
					attemptsMap.Delete(key)
				} else if att.count < maxAttempts && now.After(att.lockUntil) {
                    // 对于未锁定但长期未活动的记录也进行清理 (设 30 分钟过期)
					attemptsMap.Delete(key)
                }
				return true
			})
		}
	}()
}

// 🌟 修复：安全的 IP 提取逻辑
func getRealIP(r *http.Request) string {
	// 1. 获取物理直连 IP (无法被伪造)
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return remoteIP
	}

	// 2. 仅当直连 IP 为内网或本地回环时 (即经过受信任的 Nginx/Docker 网关)，才解析 Header
	if ip.IsLoopback() || ip.IsPrivate() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ips := strings.Split(xff, ",")
			if len(ips) > 0 {
				return strings.TrimSpace(ips[0])
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	// 3. 否则 (如公网直连)，强制使用物理连接 IP
	if remoteIP == "::1" {
		return "127.0.0.1"
	}
	return remoteIP
}

func IsIPLocked(r *http.Request) (bool, time.Duration) {
	ip := getRealIP(r)
	v, ok := attemptsMap.Load(ip)
	if !ok {
		return false, 0
	}

	att := v.(*loginAttempt)
	if att.count >= maxAttempts {
		if time.Now().Before(att.lockUntil) {
			return true, time.Until(att.lockUntil)
		}
		// 封禁时间已过，解除封禁
		attemptsMap.Delete(ip)
		return false, 0
	}
	return false, 0
}

func RecordFailedLogin(r *http.Request) {
	ip := getRealIP(r)
	now := time.Now()
	
	v, _ := attemptsMap.LoadOrStore(ip, &loginAttempt{lockUntil: now.Add(30 * time.Minute)})
	att := v.(*loginAttempt)
	
	att.count++
	if att.count >= maxAttempts {
		att.lockUntil = now.Add(lockDuration)
	}
}

func ClearLoginAttempts(r *http.Request) {
	ip := getRealIP(r)
	attemptsMap.Delete(ip)
}