package manager

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
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

	// Purge historical dirty entries that were contaminated by overseas WHOIS street addresses
	execute(a.db, `DELETE FROM "IPGeoCache" WHERE "ISP" LIKE '%Jin-rong Street%' OR "ISP" LIKE '%Financial Street%' OR "Location" LIKE '%Jin-rong Street%' OR "ISP" LIKE '%No.31%'`)

	// Initialize offline ip2region database
	a.initIP2Region()
}

func (a *App) clearIPGeoCache() int64 {
	return execute(a.db, `DELETE FROM "IPGeoCache"`)
}

func (a *App) initIP2Region() {
	candidates := []string{}
	if p := os.Getenv("SEM_IP2REGION_DB"); p != "" {
		candidates = append(candidates, p)
	}
	if a.cfg.DataDir != "" {
		candidates = append(candidates,
			filepath.Join(a.cfg.DataDir, "ip2region.xdb"),
			filepath.Join(a.cfg.DataDir, "ip2region_v4.xdb"),
		)
	}
	if a.cfg.Root != "" {
		candidates = append(candidates,
			filepath.Join(a.cfg.Root, "data", "ip2region.xdb"),
			filepath.Join(a.cfg.Root, "data", "ip2region_v4.xdb"),
			filepath.Join(a.cfg.Root, "ip2region.xdb"),
			filepath.Join(a.cfg.Root, "ip2region_v4.xdb"),
		)
	}
	candidates = append(candidates,
		"/usr/local/share/softether-manager/ip2region.xdb",
		"/var/lib/softether-manager/ip2region.xdb",
		"data/ip2region.xdb",
		"data/ip2region_v4.xdb",
		"ip2region.xdb",
		"ip2region_v4.xdb",
	)

	for _, path := range candidates {
		if path == "" {
			continue
		}
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() && fi.Size() > 1024*1024 {
			if a.loadXDB(path) {
				return
			}
		}
	}
}

func (a *App) startGeoWorker(ctx context.Context) {
	if flag.Lookup("test.v") != nil {
		return
	}
	a.geoMu.RLock()
	loaded := a.xdb != nil
	a.geoMu.RUnlock()
	if loaded || a.cfg.DataDir == "" {
		return
	}

	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		a.ensureIP2Region(ctx)
	}()
}

func (a *App) loadXDB(path string) bool {
	cBuff, err := xdb.LoadContentFromFile(path)
	if err != nil {
		log.Printf("ip2region: failed to read %s: %v", path, err)
		return false
	}
	header, err := xdb.LoadHeaderFromBuff(cBuff)
	if err != nil {
		log.Printf("ip2region: invalid xdb header in %s: %v", path, err)
		return false
	}
	version, err := xdb.VersionFromHeader(header)
	if err != nil {
		log.Printf("ip2region: unsupported version in %s: %v", path, err)
		return false
	}
	searcher, err := xdb.NewWithBuffer(version, cBuff)
	if err != nil {
		log.Printf("ip2region: failed to initialize searcher from %s: %v", path, err)
		return false
	}

	a.geoMu.Lock()
	old := a.xdb
	a.xdb = searcher
	a.xdbPath = path
	a.geoMu.Unlock()

	if old != nil {
		old.Close()
	}
	log.Printf("ip2region: loaded offline database from %s", path)
	return true
}

func (a *App) ensureIP2Region(ctx context.Context) {
	destPath := filepath.Join(a.cfg.DataDir, "ip2region.xdb")
	if fi, err := os.Stat(destPath); err == nil && fi.Size() > 1024*1024 {
		a.loadXDB(destPath)
		return
	}

	urls := []string{
		"https://cdn.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v4.xdb",
		"https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb",
	}

	client := &http.Client{Timeout: 60 * time.Second}
	tmpPath := destPath + ".tmp"
	for _, u := range urls {
		select {
		case <-ctx.Done():
			return
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "SoftEther-Manager")
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			continue
		}

		func() {
			out, err := os.Create(tmpPath)
			if err != nil {
				resp.Body.Close()
				return
			}
			defer out.Close()
			defer resp.Body.Close()

			_, err = io.Copy(out, resp.Body)
			if err != nil || ctx.Err() != nil {
				return
			}
			out.Close()

			if fi, err := os.Stat(tmpPath); err == nil && fi.Size() > 1024*1024 {
				if err := os.Rename(tmpPath, destPath); err == nil {
					a.loadXDB(destPath)
				}
			}
		}()

		a.geoMu.RLock()
		loaded := a.xdb != nil
		a.geoMu.RUnlock()
		if loaded {
			_ = os.Remove(tmpPath)
			return
		}
		_ = os.Remove(tmpPath)
	}
}

func cleanXDBVal(s string) string {
	s = strings.TrimSpace(s)
	if s == "0" || s == "Reserved" {
		return ""
	}
	return s
}

func normalizeISP(isp string) string {
	s := strings.TrimSpace(isp)
	switch {
	case strings.Contains(s, "电信"):
		return "中国电信"
	case strings.Contains(s, "联通"):
		return "中国联通"
	case strings.Contains(s, "移动"):
		return "中国移动"
	case strings.Contains(s, "铁通"):
		return "中国铁通"
	case strings.Contains(s, "广电"):
		return "中国广电"
	case strings.Contains(s, "教育网"):
		return "中国教育网"
	case strings.Contains(s, "阿里"):
		return "阿里云"
	case strings.Contains(s, "腾讯"):
		return "腾讯云"
	case strings.Contains(s, "百度"):
		return "百度云"
	case strings.Contains(s, "华为"):
		return "华为云"
	case strings.Contains(s, "金山"):
		return "金山云"
	case strings.Contains(s, "京东"):
		return "京东云"
	case strings.Contains(s, "微软"):
		return "微软云"
	case strings.Contains(s, "亚马逊"):
		return "亚马逊云"
	case strings.Contains(s, "谷歌"):
		return "谷歌云"
	default:
		return s
	}
}

func sanitizeISP(isp, org string) string {
	raw := strings.TrimSpace(isp)
	if raw == "" {
		raw = strings.TrimSpace(org)
	}
	switch {
	case strings.Contains(raw, "Jin-rong Street") || strings.Contains(raw, "Financial Street") || strings.Contains(raw, "CHINANET") || strings.Contains(raw, "Chinanet") || strings.Contains(raw, "No.31") || strings.Contains(raw, "China Telecom"):
		return "中国电信"
	case strings.Contains(raw, "China Unicom") || strings.Contains(raw, "UNICOM") || strings.Contains(raw, "CNC Group") || strings.Contains(raw, "China169"):
		return "中国联通"
	case strings.Contains(raw, "China Mobile") || strings.Contains(raw, "CMNET"):
		return "中国移动"
	case strings.Contains(raw, "Tietong") || strings.Contains(raw, "铁通"):
		return "中国铁通"
	case strings.Contains(raw, "CERNET") || strings.Contains(raw, "教育网"):
		return "中国教育网"
	default:
		return raw
	}
}

func parseXDBRegion(res string) (country, region, city, isp string) {
	parts := strings.Split(res, "|")
	if len(parts) < 5 {
		return "", "", "", ""
	}
	country = cleanXDBVal(parts[0])

	// Distinguish [Country, Area, Province, City, ISP] vs [Country, Province, City, ISP, Code]
	if len(parts[4]) == 2 || (parts[1] != "" && parts[1] != "0" && !strings.HasSuffix(parts[1], "区") && parts[1] != "华东" && parts[1] != "华北" && parts[1] != "华南" && parts[1] != "华中" && parts[1] != "西北" && parts[1] != "西南" && parts[1] != "东北") {
		region = cleanXDBVal(parts[1])
		city = cleanXDBVal(parts[2])
		isp = normalizeISP(cleanXDBVal(parts[3]))
	} else {
		region = cleanXDBVal(parts[2])
		city = cleanXDBVal(parts[3])
		isp = normalizeISP(cleanXDBVal(parts[4]))
	}

	return country, region, city, isp
}

func buildIPGeoInfo(ip, country, region, city, isp string) IPGeoInfo {
	locParts := []string{}
	if country != "" && country != "中国" {
		locParts = append(locParts, country)
	}
	if region != "" {
		locParts = append(locParts, region)
	}
	if city != "" && city != region {
		locParts = append(locParts, city)
	}
	loc := strings.Join(locParts, " ")
	if loc == "" {
		loc = country
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
		Country:  country,
		Region:   region,
		City:     city,
		ISP:      isp,
		Location: loc,
		Full:     full,
	}
}

func (a *App) lookupXDB(ip string) (IPGeoInfo, bool) {
	a.geoMu.RLock()
	searcher := a.xdb
	a.geoMu.RUnlock()
	if searcher == nil {
		return IPGeoInfo{}, false
	}

	res, err := searcher.Search(ip)
	if err != nil || res == "" {
		return IPGeoInfo{}, false
	}

	country, region, city, isp := parseXDBRegion(res)
	if country == "" && region == "" && city == "" && isp == "" {
		return IPGeoInfo{}, false
	}
	return buildIPGeoInfo(ip, country, region, city, isp), true
}

func (a *App) isGeoNegCached(ip string) bool {
	a.geoNegMu.Lock()
	defer a.geoNegMu.Unlock()
	if a.geoNegCache == nil {
		return false
	}
	until, ok := a.geoNegCache[ip]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(a.geoNegCache, ip)
		return false
	}
	return true
}

func (a *App) markGeoNegCached(ip string) {
	a.geoNegMu.Lock()
	defer a.geoNegMu.Unlock()
	if a.geoNegCache == nil {
		a.geoNegCache = make(map[string]time.Time)
	}
	if len(a.geoNegCache) >= 2048 {
		nowTime := time.Now()
		for k, v := range a.geoNegCache {
			if nowTime.After(v) {
				delete(a.geoNegCache, k)
			}
		}
	}
	a.geoNegCache[ip] = time.Now().Add(10 * time.Minute)
}

func (a *App) upsertIPGeoCache(entries []IPGeoInfo) {
	if len(entries) == 0 {
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		log.Printf("ipgeo cache begin tx: %v", err)
		return
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO IPGeoCache(IP, Country, Region, City, ISP, Location, Full, UpdatedDate) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		log.Printf("ipgeo cache prepare: %v", err)
		return
	}
	defer stmt.Close()
	stamp := now()
	for _, info := range entries {
		if _, err := stmt.Exec(info.IP, info.Country, info.Region, info.City, info.ISP, info.Location, info.Full, stamp); err != nil {
			log.Printf("ipgeo cache write %s: %v", info.IP, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("ipgeo cache commit: %v", err)
	}
}

func (a *App) lookupIPResult(ctx context.Context, ipStr string, useNegCache bool, offlineOnly ...bool) (IPGeoInfo, bool) {
	ip := cleanIP(ipStr)
	if ip == "" {
		return IPGeoInfo{}, false
	}
	if isPrivateIP(ip) {
		return IPGeoInfo{
			IP:       ip,
			Location: "局域网/内网",
			ISP:      "—",
			Full:     "局域网/内网",
		}, false
	}

	// 1. Check local SQLite cache (use read-only pool)
	row := one(a.readDB(), `SELECT Country, Region, City, ISP, Location, Full, UpdatedDate FROM IPGeoCache WHERE IP=?`, ip)
	if row != nil && time.Since(parseTime(str(row["UpdatedDate"]))) < 30*24*time.Hour {
		isp := str(row["ISP"])
		loc := str(row["Location"])
		// Discard polluted historical cache entries
		if !strings.Contains(isp, "Jin-rong Street") && !strings.Contains(loc, "Jin-rong Street") {
			return IPGeoInfo{
				IP:       ip,
				Country:  str(row["Country"]),
				Region:   str(row["Region"]),
				City:     str(row["City"]),
				ISP:      isp,
				Location: loc,
				Full:     str(row["Full"]),
			}, false
		}
	}

	skipOnline := len(offlineOnly) > 0 && offlineOnly[0]

	// 2. Query offline ip2region database (high precision, microsecond latency, zero network)
	if info, ok := a.lookupXDB(ip); ok {
		// If domestic, overseas with city known, or offline-only mode, return offline result immediately
		if info.Country == "中国" || info.City != "" || skipOnline {
			return info, true
		}

		// Overseas IP without city: attempt online enrichment unless negatively cached
		if !useNegCache || !a.isGeoNegCached(ip) {
			online := fetchIPGeo(ctx, ip)
			if online.City != "" {
				info.City = online.City
				if online.Region != "" && info.Region == "" {
					info.Region = online.Region
				}
				info.Location = online.Location
				if online.ISP != "" && info.ISP == "" {
					info.ISP = online.ISP
				}
				info.Full = strings.TrimSpace(info.Location + " " + info.ISP)
				return info, true
			}
			if useNegCache {
				a.markGeoNegCached(ip)
			}
		}
		return info, true
	}

	if skipOnline {
		return IPGeoInfo{IP: ip}, false
	}

	// 3. Fallback to HTTPS online API (with negative caching on batch polling)
	if useNegCache && a.isGeoNegCached(ip) {
		return IPGeoInfo{IP: ip}, false
	}
	info := fetchIPGeo(ctx, ip)
	if info.Location != "" || info.ISP != "" {
		return info, true
	}
	if useNegCache {
		a.markGeoNegCached(ip)
	}
	return info, false
}

func (a *App) lookupIP(ctx context.Context, ipStr string) IPGeoInfo {
	info, shouldCache := a.lookupIPResult(ctx, ipStr, false)
	if shouldCache {
		a.upsertIPGeoCache([]IPGeoInfo{info})
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

func fetchIPGeo(ctx context.Context, ip string) IPGeoInfo {
	client := &http.Client{Timeout: 3 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://ipwho.is/%s?lang=zh-CN", ip), nil)
	if err != nil {
		return IPGeoInfo{IP: ip}
	}
	req.Header.Set("User-Agent", "SoftEther-Manager")
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return IPGeoInfo{IP: ip}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		var res ipwhoisResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&res); err == nil && res.Success {
			isp := sanitizeISP(res.Connection.ISP, res.Connection.Org)
			return buildIPGeoInfo(ip, res.Country, res.Region, res.City, isp)
		}
	}

	return IPGeoInfo{IP: ip}
}

func (a *App) batchLookupIPs(ctx context.Context, ips []string, offlineOnly ...bool) map[string]IPGeoInfo {
	out := make(map[string]IPGeoInfo)
	if len(ips) == 0 {
		return out
	}
	skipOnline := len(offlineOnly) > 0 && offlineOnly[0]
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

	// Concurrently resolve needed IPs without writing SQLite inside workers; persist batch once done.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var toCache []IPGeoInfo

	for _, ip := range needed {
		wg.Add(1)
		go func(targetIP string) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					log.Printf("ipgeo lookup %s: %v", targetIP, p)
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, shouldCache := a.lookupIPResult(ctx, targetIP, true, skipOnline)
			mu.Lock()
			out[targetIP] = info
			if shouldCache {
				toCache = append(toCache, info)
			}
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	a.upsertIPGeoCache(toCache)
	return out
}
