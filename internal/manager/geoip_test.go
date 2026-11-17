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
