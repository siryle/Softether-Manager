package manager

import (
	"context"
	"net"
	"strings"
	"sync"
)

func isIP(s string) bool {
	return net.ParseIP(strings.TrimSpace(s)) != nil
}

// isIPv4 returns true if s is a valid IPv4 address string.
func isIPv4(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	return ip != nil && ip.To4() != nil
}

// isClientSessionRPC returns true if the session item from EnumSession represents
// an actual end-user VPN client session, rather than an internal/system session
// such as SecureNAT, Local Bridge, Cascade connection, or Layer-3 Virtual Router.
func isClientSessionRPC(s M) bool {
	if s == nil {
		return false
	}
	if boolean(s["LinkMode_bool"]) || boolean(s["SecureNATMode_bool"]) || boolean(s["BridgeMode_bool"]) || boolean(s["Layer3Mode_bool"]) {
		return false
	}
	u := strings.ToLower(strings.TrimSpace(str(s["Username_str"])))
	if u == "securenat" || u == "local bridge" || strings.HasPrefix(u, "sid-local-bridge") || strings.HasPrefix(u, "link_") || strings.HasPrefix(u, "l3_") {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(str(s["Name_str"])))
	if strings.HasPrefix(name, "sid-securenat") || strings.HasPrefix(name, "sid-local-bridge") || strings.HasPrefix(name, "sid-link-") || strings.HasPrefix(name, "sid-l3-") {
		return false
	}
	return true
}

// decodeNodePort normalizes SoftEther port fields. NODE_INFO stores ClientPort
// and ServerPort2 in network byte order passed through LittleEndian32 in
// OutRpcNodeInfo, so on little-endian servers ports 1..65535 arrive byte-swapped.
func decodeNodePort(v any) int64 {
	n := integer(v)
	if n <= 0 {
		return 0
	}
	if n <= 65535 {
		return n
	}
	u := uint32(n)
	swapped := int64(((u & 0xff) << 24) | ((u & 0xff00) << 8) | ((u >> 8) & 0xff00) | ((u >> 24) & 0xff))
	if swapped > 0 && swapped <= 65535 {
		return swapped
	}
	return 0
}

func extractSessionPorts(st M) (int64, int64) {
	serverPort := decodeNodePort(st["ServerPort2_u32"])
	if serverPort == 0 {
		serverPort = decodeNodePort(st["ServerPort_u32"])
	}
	clientPort := decodeNodePort(st["ClientPort_u32"])
	return serverPort, clientPort
}

// collectSessionNetMaps resolves per-session IPv4 virtual IPs and DHCP hostnames for a hub.
func (a *App) collectSessionNetMaps(ctx context.Context, hub string) (sessionIPs map[string][]string, ipToDhcpHost map[string]string) {
	var ipTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumIpTable", M{"HubName_str": hub})
		ipTable = list(out["IpTable"])
	}()

	var dhcpTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumDHCP", M{"HubName_str": hub})
		dhcpTable = list(out["DhcpTable"])
	}()

	sessionIPs = make(map[string][]string)
	for _, entry := range ipTable {
		sessName := str(entry["SessionName_str"])
		ip := strings.TrimSpace(str(entry["IpAddress_ip"]))
		// Exclude IPv6 addresses; only keep IPv4
		if sessName != "" && ip != "" && isIPv4(ip) {
			found := false
			for _, existing := range sessionIPs[sessName] {
				if existing == ip {
					found = true
					break
				}
			}
			if !found {
				sessionIPs[sessName] = append(sessionIPs[sessName], ip)
			}
		}
	}

	ipToDhcpHost = make(map[string]string)
	for _, entry := range dhcpTable {
		ip := strings.TrimSpace(str(entry["IpAddress_ip"]))
		h := strings.TrimSpace(str(entry["Hostname_str"]))
		if ip != "" && h != "" && !isIP(h) {
			ipToDhcpHost[ip] = h
		}
	}
	return sessionIPs, ipToDhcpHost
}

func firstDHCPHost(ips []string, ipToDhcpHost map[string]string, remoteHost string) string {
	for _, ip := range ips {
		if dhcpH := ipToDhcpHost[ip]; dhcpH != "" && !strings.EqualFold(dhcpH, remoteHost) {
			return dhcpH
		}
	}
	return ""
}

// enrichSessions supplements session entries with Virtual IP (IPv4 only) and clean client hostname.
func (a *App) enrichSessions(ctx context.Context, hub string, sessions []M) []M {
	if len(sessions) == 0 {
		return sessions
	}
	sessionIPs, ipToDhcpHost := a.collectSessionNetMaps(ctx, hub)

	natTcpByIP := make(map[string]int)
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumNAT", M{"HubName_str": hub})
		for _, entry := range list(out["NatTable"]) {
			if integer(entry["Protocol_u32"]) == 0 {
				if srcIP := strings.TrimSpace(str(entry["SrcIp_ip"])); srcIP != "" {
					natTcpByIP[srcIP]++
				}
			}
		}
	}()

	type sessStatusRes struct {
		name       string
		clientHost string
		serverPort int64
		clientPort int64
	}
	sessStatusMap := make(map[string]sessStatusRes)
	if len(sessions) > 0 && len(sessions) <= 50 {
		ch := make(chan sessStatusRes, len(sessions))
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, s := range sessions {
			sessName := str(s["Name_str"])
			if sessName == "" {
				continue
			}
			wg.Add(1)
			go func(sn string) {
				defer wg.Done()
				defer func() { _ = recover() }()
				sem <- struct{}{}
				defer func() { <-sem }()
				st := a.rpc(ctx, "GetSessionStatus", M{"HubName_str": hub, "Name_str": sn})
				cH := strings.TrimSpace(str(st["ClientHostname_str"]))
				sN := strings.TrimSpace(str(st["ServerName_str"]))
				cIP := strings.TrimSpace(str(st["Client_Ip_Address_ip"]))
				host := ""
				if cH != "" && !isIP(cH) && cH != cIP && !strings.EqualFold(cH, sN) {
					host = cH
				}
				sPort, cPort := extractSessionPorts(st)
				if host != "" || sPort > 0 || cPort > 0 {
					ch <- sessStatusRes{name: sn, clientHost: host, serverPort: sPort, clientPort: cPort}
				}
			}(sessName)
		}
		wg.Wait()
		close(ch)
		for res := range ch {
			sessStatusMap[res.name] = res
		}
	}

	userMap := make(map[string]M)
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumUser", M{"HubName_str": hub})
		for _, u := range list(out["UserList"]) {
			uName := strings.ToLower(strings.TrimSpace(str(u["Name_str"])))
			if uName != "" {
				userMap[uName] = u
			}
		}
	}()

	ips := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if cIP := str(s["ClientIP_ip"]); cIP != "" {
			ips = append(ips, cIP)
		}
	}
	geoMap := a.batchLookupIPs(ctx, ips)

	for _, s := range sessions {
		sessName := str(s["Name_str"])
		clientIP := strings.TrimSpace(str(s["ClientIP_ip"]))
		host := strings.TrimSpace(str(s["Hostname_str"]))
		remoteHost := strings.TrimSpace(str(s["RemoteHostname_str"])) // SoftEther server's hostname

		if ips, ok := sessionIPs[sessName]; ok && len(ips) > 0 {
			s["VirtualIP_ip"] = strings.Join(ips, ", ")
		} else {
			s["VirtualIP_ip"] = ""
		}

		activeNatTcp := 0
		for _, ip := range sessionIPs[sessName] {
			activeNatTcp += natTcpByIP[ip]
		}
		s["ActiveNatTcp_u32"] = activeNatTcp

		var realHost string
		if res, ok := sessStatusMap[sessName]; ok {
			if res.serverPort > 0 {
				s["ServerPort_u32"] = res.serverPort
			}
			if res.clientPort > 0 {
				s["ClientPort_u32"] = res.clientPort
			}
			if res.clientHost != "" {
				realHost = res.clientHost
				s["ClientHostname_str"] = res.clientHost
			} else {
				realHost = firstDHCPHost(sessionIPs[sessName], ipToDhcpHost, remoteHost)
			}
		} else {
			realHost = firstDHCPHost(sessionIPs[sessName], ipToDhcpHost, remoteHost)
		}

		if realHost == "" && host != "" && host != clientIP && !isIP(host) && !strings.EqualFold(host, remoteHost) {
			realHost = host
		}

		s["RealHostname_str"] = realHost
		if isIP(host) || host == clientIP || strings.EqualFold(host, remoteHost) {
			s["Hostname_str"] = realHost
		}

		if geo, ok := geoMap[cleanIP(clientIP)]; ok {
			s["IpLocation_str"] = geo.Location
			s["IpIsp_str"] = geo.ISP
			s["IpFull_str"] = geo.Full
		}
		uName := strings.ToLower(strings.TrimSpace(str(s["Username_str"])))
		if u, ok := userMap[uName]; ok {
			s["Realname_utf"] = str(u["Realname_utf"])
			s["Note_utf"] = str(u["Note_utf"])
		}
	}
	return sessions
}

// enrichSessionStatus supplements single session status with Virtual IP (IPv4 only) and clean client hostname.
func (a *App) enrichSessionStatus(ctx context.Context, hub, name string, status M) M {
	if status == nil {
		return status
	}
	clientIP := strings.TrimSpace(str(status["Client_Ip_Address_ip"]))
	serverName := strings.TrimSpace(str(status["ServerName_str"]))
	clientReportedHost := strings.TrimSpace(str(status["ClientHostname_str"]))
	serverResolvedHost := strings.TrimSpace(str(status["SessionStatus_ClientHostName_str"]))

	var ipTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumIpTable", M{"HubName_str": hub})
		ipTable = list(out["IpTable"])
	}()

	var dhcpTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumDHCP", M{"HubName_str": hub})
		dhcpTable = list(out["DhcpTable"])
	}()

	natTcpByIP := make(map[string]int)
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumNAT", M{"HubName_str": hub})
		for _, entry := range list(out["NatTable"]) {
			if integer(entry["Protocol_u32"]) == 0 {
				if srcIP := strings.TrimSpace(str(entry["SrcIp_ip"])); srcIP != "" {
					natTcpByIP[srcIP]++
				}
			}
		}
	}()

	var virtualIPs []string
	for _, entry := range ipTable {
		if str(entry["SessionName_str"]) == name {
			ip := strings.TrimSpace(str(entry["IpAddress_ip"]))
			// Exclude IPv6 addresses; only keep IPv4
			if ip != "" && isIPv4(ip) {
				found := false
				for _, ex := range virtualIPs {
					if ex == ip {
						found = true
						break
					}
				}
				if !found {
					virtualIPs = append(virtualIPs, ip)
				}
			}
		}
	}

	var dhcpHost string
	for _, ip := range virtualIPs {
		for _, entry := range dhcpTable {
			if strings.TrimSpace(str(entry["IpAddress_ip"])) == ip {
				h := strings.TrimSpace(str(entry["Hostname_str"]))
				if h != "" && !isIP(h) && !strings.EqualFold(h, serverName) {
					dhcpHost = h
					break
				}
			}
		}
		if dhcpHost != "" {
			break
		}
	}

	if len(virtualIPs) > 0 {
		status["VirtualIP_ip"] = strings.Join(virtualIPs, ", ")
	} else {
		status["VirtualIP_ip"] = ""
	}

	var realHost string
	// Priority 1: ClientHostname_str ("客户端主机名 (报告的)") reported by client
	if clientReportedHost != "" && !isIP(clientReportedHost) && clientReportedHost != clientIP && !strings.EqualFold(clientReportedHost, serverName) {
		realHost = clientReportedHost
	} else if dhcpHost != "" {
		// Priority 2: DHCP assigned machine hostname
		realHost = dhcpHost
	} else if serverResolvedHost != "" && serverResolvedHost != clientIP && !isIP(serverResolvedHost) && !strings.EqualFold(serverResolvedHost, serverName) {
		// Priority 3: DNS PTR resolved hostname (if not matching server name or client IP)
		realHost = serverResolvedHost
	}

	status["RealHostname_str"] = realHost
	status["ClientHostname_str"] = realHost
	status["SessionStatus_ClientHostName_str"] = realHost

	var activeNatTcp int
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumNAT", M{"HubName_str": hub})
		natTable := list(out["NatTable"])
		for _, ex := range virtualIPs {
			for _, entry := range natTable {
				if str(entry["SrcIp_ip"]) == ex && integer(entry["Protocol_u32"]) == 0 {
					activeNatTcp++
				}
			}
		}
	}()
	status["ActiveNatTcp_u32"] = activeNatTcp

	serverPort, clientPort := extractSessionPorts(status)
	status["ServerPort_u32"] = serverPort
	status["ClientPort_u32"] = clientPort

	geo := a.lookupIP(ctx, clientIP)
	status["IpLocation_str"] = geo.Location
	status["IpIsp_str"] = geo.ISP
	status["IpFull_str"] = geo.Full

	func() {
		defer func() { _ = recover() }()
		uName := str(status["Username_str"])
		if uName != "" {
			u := a.rpc(ctx, "GetUser", M{"HubName_str": hub, "Name_str": uName})
			status["Realname_utf"] = str(u["Realname_utf"])
			status["Note_utf"] = str(u["Note_utf"])
		}
	}()

	return status
}
