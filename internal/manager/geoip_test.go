package manager

import (
	"context"
	"testing"
)

func TestGeoIPPrivateIP(t *testing.T) {
	a := testApp(t)
	cases := []string{
		"127.0.0.1",
		"192.168.1.1",
		"10.0.0.5",
		"172.16.0.10",
		"192.168.6.213:5555",
		"::1",
	}
	for _, c := range cases {
		info := a.lookupIP(context.Background(), c)
		if info.Location != "局域网/内网" {
			t.Errorf("expected 局域网/内网 for %s, got %s", c, info.Location)
		}
	}
}

func TestGeoIPCache(t *testing.T) {
	a := testApp(t)
	// Seed manual cache entry in SQLite
	execute(a.db, `INSERT OR REPLACE INTO IPGeoCache(IP, Country, Region, City, ISP, Location, Full, UpdatedDate) VALUES(?,?,?,?,?,?,?,?)`,
		"114.114.114.114", "中国", "江苏省", "南京市", "中国电信", "江苏省 南京市", "江苏省 南京市 中国电信", now())

	info := a.lookupIP(context.Background(), "114.114.114.114")
	if info.Location != "江苏省 南京市" {
		t.Errorf("expected 江苏省 南京市, got %s", info.Location)
	}
	if info.ISP != "中国电信" {
		t.Errorf("expected 中国电信, got %s", info.ISP)
	}

	batch := a.batchLookupIPs(context.Background(), []string{"114.114.114.114", "192.168.1.5"})
	if batch["114.114.114.114"].Location != "江苏省 南京市" {
		t.Errorf("batch lookup failed for 114.114.114.114")
	}
	if batch["192.168.1.5"].Location != "局域网/内网" {
		t.Errorf("batch lookup failed for 192.168.1.5")
	}
}

func TestEnrichConnections(t *testing.T) {
	a := testApp(t)
	execute(a.db, `INSERT OR REPLACE INTO IPGeoCache(IP, Country, Region, City, ISP, Location, Full, UpdatedDate) VALUES(?,?,?,?,?,?,?,?)`,
		"222.92.51.10", "中国", "湖南省", "株洲市", "中国电信", "湖南省 株洲市", "湖南省 株洲市 中国电信", now())

	conns := []M{
		{
			"Name_str":     "CID-101",
			"Ip_ip":        "222.92.51.10",
			"Port_u32":     12345,
			"ConnectedTime": now(),
			"Type_u32":     0,
		},
		{
			"Name_str":     "CID-102",
			"Ip_ip":        "192.168.1.50",
			"Port_u32":     54321,
			"ConnectedTime": now(),
			"Type_u32":     5,
		},
	}

	res := a.enrichConnections(context.Background(), conns)
	list := list(res["ConnectionList"])
	if len(list) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(list))
	}
	if list[0]["IpLocation_str"] != "湖南省 株洲市" {
		t.Errorf("expected 湖南省 株洲市, got %v", list[0]["IpLocation_str"])
	}
	if list[0]["IpIsp_str"] != "中国电信" {
		t.Errorf("expected 中国电信, got %v", list[0]["IpIsp_str"])
	}
	if list[1]["IpLocation_str"] != "局域网/内网" {
		t.Errorf("expected 局域网/内网, got %v", list[1]["IpLocation_str"])
	}
}

func TestSanitizeISP(t *testing.T) {
	for raw, expected := range map[string]string{
		"No.31,Jin-rong Street": "中国电信",
		"Financial Street":      "中国电信",
		"CHINANET-JS":           "中国电信",
		"China Unicom Henan":    "中国联通",
		"China Mobile 5G":       "中国移动",
		"Tietong Telecom":       "中国铁通",
		"CERNET Backbone":       "中国教育网",
		"Normal ISP":            "Normal ISP",
	} {
		if got := sanitizeISP(raw, ""); got != expected {
			t.Errorf("expected %s for %s, got %s", expected, raw, got)
		}
	}
}

func TestClearIPGeoCache(t *testing.T) {
	a := testApp(t)
	execute(a.db, `INSERT INTO IPGeoCache(IP, Location, UpdatedDate) VALUES(?, ?, ?)`, "1.2.3.4", "Test Loc", now())
	if n := a.clearIPGeoCache(); n != 1 {
		t.Errorf("expected 1 row deleted, got %d", n)
	}
	if row := one(a.db, `SELECT COUNT(*) n FROM IPGeoCache`); integer(row["n"]) != 0 {
		t.Errorf("expected 0 rows in cache, got %v", row["n"])
	}
}

func TestIP2RegionOfflineLookup(t *testing.T) {
	a := testApp(t)
	// Try loading root or local ip2region_v4.xdb if available
	candidates := []string{"../../ip2region_v4.xdb", "../../../ip2region_v4.xdb", "ip2region_v4.xdb"}
	for _, p := range candidates {
		if a.loadXDB(p) {
			break
		}
	}
	if a.xdb == nil {
		t.Skip("ip2region_v4.xdb not found on disk, skipping offline test")
	}

	info := a.lookupIP(context.Background(), "58.222.254.242")
	if info.City != "泰州市" {
		t.Errorf("expected 泰州市, got %s", info.City)
	}
	if info.Region != "江苏省" {
		t.Errorf("expected 江苏省, got %s", info.Region)
	}
	if info.ISP != "中国电信" {
		t.Errorf("expected 中国电信, got %s", info.ISP)
	}
	if info.Location != "江苏省 泰州市" {
		t.Errorf("expected 江苏省 泰州市, got %s", info.Location)
	}
}
