package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type IPGeoInfo struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`
	Region   string `json:"region"`
	City     string `json:"city"`
	ISP      string `json:"isp"`
	Location string `json:"location"`
	Full     string `json:"full"`
}

func isPrivateIP(ipStr string) bool {
	clean := strings.TrimSpace(ipStr)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}
	ip := net.ParseIP(clean)
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}
	return false
}

func cleanIP(ipStr string) string {
	clean := strings.TrimSpace(ipStr)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}
	return clean
}

func (a *App) initGeoIP() {
	execute(a.db, `CREATE TABLE IF NOT EXISTS "IPGeoCache" (
		"IP"          TEXT PRIMARY KEY,
		"Country"     TEXT NOT NULL DEFAULT '',
		"Region"      TEXT NOT NULL DEFAULT '',
		"City"        TEXT NOT NULL DEFAULT '',
		"ISP"         TEXT NOT NULL DEFAULT '',
		"Location"    TEXT NOT NULL DEFAULT '',
		"Full"        TEXT NOT NULL DEFAULT '',
		"UpdatedDate" TEXT NOT NULL
	)`)
}

func (a *App) lookupIP(ctx context.Context, ipStr string) IPGeoInfo {
	ip := cleanIP(ipStr)
	if ip == "" {
		return IPGeoInfo{}
	}
	if isPrivateIP(ip) {
		return IPGeoInfo{
			IP:       ip,
			Location: "局域网/内网",
			ISP:      "—",
			Full:     "局域网/内网",
		}
	}

	row := one(a.db, `SELECT Country, Region, City, ISP, Location, Full, UpdatedDate FROM IPGeoCache WHERE IP=?`, ip)
	if row != nil && time.Since(parseTime(str(row["UpdatedDate"]))) < 30*24*time.Hour {
		return IPGeoInfo{IP: ip, Country: str(row["Country"]), Region: str(row["Region"]), City: str(row["City"]), ISP: str(row["ISP"]), Location: str(row["Location"]), Full: str(row["Full"])}
	}
	info := fetchIPGeo(ctx, ip)
	if info.Location != "" || info.ISP != "" {
		execute(a.db, `INSERT OR REPLACE INTO IPGeoCache(IP, Country, Region, City, ISP, Location, Full, UpdatedDate) VALUES(?,?,?,?,?,?,?,?)`,
			info.IP, info.Country, info.Region, info.City, info.ISP, info.Location, info.Full, now())
	}

	return info
}

type ipwhoisResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	Country    string `json:"country"`
	Region     string `json:"region"`
	City       string `json:"city"`
	Connection struct {
		ISP string `json:"isp"`
		Org string `json:"org"`
	} `json:"connection"`
}

type ipapiResponse struct {
	Status     string `json:"status"`
	Country    string `json:"country"`
	RegionName string `json:"regionName"`
	City       string `json:"city"`
	ISP        string `json:"isp"`
	Org        string `json:"org"`
}

func fetchIPGeo(ctx context.Context, ip string) IPGeoInfo {
	client := &http.Client{Timeout: 3 * time.Second}

	// 1. Try ipwho.is
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://ipwho.is/%s?lang=zh-CN", ip), nil)
	if err == nil {
		req.Header.Set("User-Agent", "SoftEther-Manager")
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
		}
		if err == nil && resp.StatusCode == 200 {
			var res ipwhoisResponse
			if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && res.Success {
				locParts := []string{}
				if res.Country != "" && res.Country != "中国" {
					locParts = append(locParts, res.Country)
				}
				if res.Region != "" {
					locParts = append(locParts, res.Region)
				}
				if res.City != "" && res.City != res.Region {
					locParts = append(locParts, res.City)
				}
				loc := strings.Join(locParts, " ")
				if loc == "" {
					loc = res.Country
				}
				isp := res.Connection.ISP
				if isp == "" {
					isp = res.Connection.Org
				}
				full := loc
				if isp != "" {
					if full != "" {
						full += " " + isp
					} else {
						full = isp
					}
				}
				return IPGeoInfo{
					IP:       ip,
					Country:  res.Country,
					Region:   res.Region,
					City:     res.City,
					ISP:      isp,
					Location: loc,
					Full:     full,
				}
			}
		}
	}

	// 2. Fallback to ip-api.com
	req2, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://ip-api.com/json/%s?lang=zh-CN", ip), nil)
	if err == nil {
		req2.Header.Set("User-Agent", "SoftEther-Manager")
		resp, err := client.Do(req2)
		if err == nil {
			defer resp.Body.Close()
		}
		if err == nil && resp.StatusCode == 200 {
			var res ipapiResponse
			if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && res.Status == "success" {
				locParts := []string{}
				if res.Country != "" && res.Country != "中国" {
					locParts = append(locParts, res.Country)
				}
				if res.RegionName != "" {
					locParts = append(locParts, res.RegionName)
				}
				if res.City != "" && res.City != res.RegionName {
					locParts = append(locParts, res.City)
				}
				loc := strings.Join(locParts, " ")
				if loc == "" {
					loc = res.Country
				}
				isp := res.ISP
				if isp == "" {
					isp = res.Org
				}
				full := loc
				if isp != "" {
					if full != "" {
						full += " " + isp
					} else {
						full = isp
					}
				}
				return IPGeoInfo{
					IP:       ip,
					Country:  res.Country,
					Region:   res.RegionName,
					City:     res.City,
					ISP:      isp,
					Location: loc,
					Full:     full,
				}
			}
		}
	}

	return IPGeoInfo{IP: ip}
}

func (a *App) batchLookupIPs(ctx context.Context, ips []string) map[string]IPGeoInfo {
	out := make(map[string]IPGeoInfo)
	if len(ips) == 0 {
		return out
	}
	var needed []string
	seen := make(map[string]bool)
	for _, raw := range ips {
		ip := cleanIP(raw)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		if isPrivateIP(ip) {
			out[ip] = IPGeoInfo{IP: ip, Location: "局域网/内网", ISP: "—", Full: "局域网/内网"}
			continue
		}
		needed = append(needed, ip)
	}

	if len(needed) == 0 {
		return out
	}

	// Concurrently fetch needed IPs with max concurrency 5
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, ip := range needed {
		wg.Add(1)
		go func(targetIP string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info := a.lookupIP(ctx, targetIP)
			mu.Lock()
			out[targetIP] = info
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	return out
}
