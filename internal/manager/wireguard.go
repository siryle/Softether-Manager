package manager

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

const (
	defaultWireGuardPort       = 51820
	defaultWireGuardDNS        = "1.1.1.1"
	defaultWireGuardAllowedIPs = "0.0.0.0/0, ::/0"
	defaultWireGuardKeepalive  = 21
)

type seCfgItem struct {
	Type      string
	Key       string
	RawVal    string
	Val       string
	LineStart int
	LineEnd   int
}

type seCfgNode struct {
	Name            string
	RawName         string
	Depth           int
	DeclStart       int
	OpenBraceEnd    int
	CloseBraceStart int
	End             int
	Indent          string
	Items           []seCfgItem
	Children        []*seCfgNode
}

type seCfgDoc struct {
	EOL  string
	Root *seCfgNode
}

type seEdit struct {
	start int
	end   int
	text  string
}

type wgPeerConfig struct {
	PublicKey string `json:"public_key"`
	Hub       string `json:"hub"`
	User      string `json:"user"`
}

type wgHubNetwork struct {
	Hub     string `json:"hub"`
	Gateway string `json:"gateway"`
	Subnet  string `json:"subnet"`
}

type wgConfigState struct {
	Supported    bool
	Enabled      bool
	PrivateKey   string
	PublicKey    string
	PresharedKey string
	UDPPorts     []int
	Peers        []wgPeerConfig
	HubNetworks  []wgHubNetwork
}

func seCfgEscape(s string) string {
	if s == "" {
		return "$"
	}
	var out strings.Builder
	for _, b := range []byte(s) {
		if b <= 31 || b == ' ' || b == '\t' || b == '$' {
			fmt.Fprintf(&out, "$%02X", b)
		} else {
			out.WriteByte(b)
		}
	}
	return out.String()
}

func seCfgUnescape(s string) string {
	if s == "$" {
		return ""
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				out = append(out, byte(v))
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

func leadingIndent(line string) string {
	stripped := strings.TrimPrefix(line, "\ufeff")
	i := 0
	for i < len(stripped) && (stripped[i] == '\t' || stripped[i] == ' ') {
		i++
	}
	return stripped[:i]
}

func parseSEConfig(config string) (*seCfgDoc, error) {
	eol := "\n"
	if strings.Contains(config, "\r\n") {
		eol = "\r\n"
	}
	var stack []*seCfgNode
	var roots []*seCfgNode
	pendingName := ""
	pendingStart := 0
	pendingIndent := ""
	offset := 0

	for _, line := range strings.SplitAfter(config, "\n") {
		lineStart := offset
		lineEnd := offset + len(line)
		offset = lineEnd

		text := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		switch {
		case strings.HasPrefix(text, "declare "):
			if pendingName != "" {
				return nil, fmt.Errorf("invalid server configuration")
			}
			pendingName = strings.TrimSpace(strings.TrimPrefix(text, "declare "))
			if pendingName == "" {
				return nil, fmt.Errorf("invalid server configuration")
			}
			pendingStart = lineStart
			pendingIndent = leadingIndent(line)

		case text == "{":
			if pendingName == "" || (len(stack) == 0 && pendingName != "root") {
				return nil, fmt.Errorf("invalid server configuration")
			}
			node := &seCfgNode{
				Name:         seCfgUnescape(pendingName),
				RawName:      pendingName,
				Depth:        len(stack),
				DeclStart:    pendingStart,
				OpenBraceEnd: lineEnd,
				Indent:       pendingIndent,
			}
			if len(stack) == 0 {
				roots = append(roots, node)
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
			}
			stack = append(stack, node)
			pendingName = ""

		case text == "}":
			if len(stack) == 0 || pendingName != "" {
				return nil, fmt.Errorf("invalid server configuration")
			}
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			node.CloseBraceStart = lineStart
			node.End = lineEnd

		default:
			if pendingName != "" || len(stack) == 0 {
				return nil, fmt.Errorf("invalid server configuration")
			}
			fields := strings.Fields(text)
			if len(fields) < 2 {
				return nil, fmt.Errorf("invalid server configuration")
			}
			rawVal := ""
			if len(fields) >= 3 {
				rawVal = strings.Join(fields[2:], " ")
			}
			node := stack[len(stack)-1]
			node.Items = append(node.Items, seCfgItem{
				Type:      fields[0],
				Key:       seCfgUnescape(fields[1]),
				RawVal:    rawVal,
				Val:       seCfgUnescape(rawVal),
				LineStart: lineStart,
				LineEnd:   lineEnd,
			})
		}
	}

	if len(stack) != 0 || pendingName != "" || len(roots) != 1 {
		return nil, fmt.Errorf("invalid server configuration")
	}
	return &seCfgDoc{EOL: eol, Root: roots[0]}, nil
}

func (n *seCfgNode) child(name string) *seCfgNode {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (n *seCfgNode) item(key string) *seCfgItem {
	if n == nil {
		return nil
	}
	for i := range n.Items {
		if n.Items[i].Key == key {
			return &n.Items[i]
		}
	}
	return nil
}

func (n *seCfgNode) itemInsertOffset() int {
	if len(n.Items) > 0 {
		return n.Items[len(n.Items)-1].LineEnd
	}
	return n.OpenBraceEnd
}

func applySEEdits(raw string, edits []seEdit) (string, error) {
	if len(edits) == 0 {
		return raw, nil
	}
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start < edits[j].start
		}
		return edits[i].end < edits[j].end
	})
	merged := make([]seEdit, 0, len(edits))
	for _, e := range edits {
		if e.start < 0 || e.end < e.start || e.end > len(raw) {
			return "", fmt.Errorf("invalid configuration edit span")
		}
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			if e.start == last.start && e.end == last.end && e.start == e.end {
				last.text += e.text
				continue
			}
			if e.start < last.end {
				return "", fmt.Errorf("overlapping configuration edit span")
			}
		}
		merged = append(merged, e)
	}
	var out strings.Builder
	cursor := 0
	for _, e := range merged {
		out.WriteString(raw[cursor:e.start])
		out.WriteString(e.text)
		cursor = e.end
	}
	out.WriteString(raw[cursor:])
	updated := out.String()
	if _, err := parseSEConfig(updated); err != nil {
		return "", err
	}
	return updated, nil
}

func parseUDPPorts(raw string) []int {
	if strings.TrimSpace(raw) == "" {
		return []int{}
	}
	seen := map[int]bool{}
	ports := []int{}
	for _, part := range strings.Split(raw, ",") {
		p, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	return ports
}

func normalizeUDPPorts(raw any) ([]int, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("udp_ports must be an array of port numbers")
	}
	seen := map[int]bool{}
	ports := make([]int, 0, len(items))
	for _, item := range items {
		p, err := strconv.Atoi(strings.TrimSpace(str(item)))
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid UDP port: %v", item)
		}
		if !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports, nil
}

func formatUDPPorts(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}

func wgValidateKey(keyB64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil || len(raw) != curve25519.ScalarSize {
		return nil, errors.New("WireGuard key must be a 32-byte standard base64 string")
	}
	allZero := true
	for _, b := range raw {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, errors.New("WireGuard key cannot be all zeroes")
	}
	return raw, nil
}

func wgPublicKey(privB64 string) (string, error) {
	priv, err := wgValidateKey(privB64)
	if err != nil {
		return "", err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

func wgGenerateKeypair() (privB64, pubB64 string, err error) {
	priv := make([]byte, curve25519.ScalarSize)
	if _, err = rand.Read(priv); err != nil {
		return "", "", err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(pub), nil
}

func wgGenerateRandomKey() (string, error) {
	buf := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func parseWireGuardState(raw string) (*wgConfigState, error) {
	doc, err := parseSEConfig(raw)
	if err != nil {
		return nil, err
	}
	sc := doc.Root.child("ServerConfiguration")
	wg := sc.child("Proto").child("WireGuard")
	if wg == nil {
		return &wgConfigState{
			Supported:   false,
			UDPPorts:    []int{},
			Peers:       []wgPeerConfig{},
			HubNetworks: []wgHubNetwork{},
		}, nil
	}

	state := &wgConfigState{
		Supported:   true,
		UDPPorts:    []int{},
		Peers:       []wgPeerConfig{},
		HubNetworks: []wgHubNetwork{},
	}
	if item := wg.item("Enabled"); item != nil {
		state.Enabled = item.Val == "true"
	}
	if item := wg.item("PrivateKey"); item != nil {
		state.PrivateKey = item.Val
		if pub, pubErr := wgPublicKey(item.Val); pubErr == nil {
			state.PublicKey = pub
		}
	}
	if item := wg.item("PresharedKey"); item != nil {
		state.PresharedKey = item.Val
	}
	if item := sc.item("PortsUDP"); item != nil {
		state.UDPPorts = parseUDPPorts(item.Val)
	}
	if list := doc.Root.child("WireGuardKeyList"); list != nil {
		for _, peerNode := range list.Children {
			hub := ""
			user := ""
			if h := peerNode.item("Hub"); h != nil {
				hub = h.Val
			}
			if u := peerNode.item("User"); u != nil {
				user = u.Val
			}
			state.Peers = append(state.Peers, wgPeerConfig{
				PublicKey: peerNode.Name,
				Hub:       hub,
				User:      user,
			})
		}
	}
	if hubs := doc.Root.child("VirtualHUB"); hubs != nil {
		for _, hubNode := range hubs.Children {
			opt := hubNode.child("Option")
			if opt == nil {
				continue
			}
			gw, sub := "", ""
			if g := opt.item("DefaultGateway"); g != nil {
				gw = g.Val
			}
			if s := opt.item("DefaultSubnet"); s != nil {
				sub = s.Val
			}
			state.HubNetworks = append(state.HubNetworks, wgHubNetwork{
				Hub:     hubNode.Name,
				Gateway: gw,
				Subnet:  sub,
			})
		}
	}
	return state, nil
}

func setItemEdit(doc *seCfgDoc, node *seCfgNode, typ, key, val string) seEdit {
	line := strings.Repeat("\t", node.Depth+1) + typ + " " + seCfgEscape(key) + " " + seCfgEscape(val) + doc.EOL
	if existing := node.item(key); existing != nil {
		return seEdit{start: existing.LineStart, end: existing.LineEnd, text: line}
	}
	pos := node.itemInsertOffset()
	return seEdit{start: pos, end: pos, text: line}
}

type wgServerPatch struct {
	Enabled      *bool
	UDPPorts     []int
	HasUDPPorts  bool
	PresharedKey *string
	PrivateKey   *string
}

func patchWireGuardServer(raw string, patch wgServerPatch) (string, error) {
	doc, err := parseSEConfig(raw)
	if err != nil {
		return "", err
	}
	sc := doc.Root.child("ServerConfiguration")
	wg := sc.child("Proto").child("WireGuard")
	if sc == nil || wg == nil {
		return "", httpError{400, M{"message": "This SoftEther server does not expose WireGuard protocol settings.", "se_error": "UnsupportedError"}}
	}

	var edits []seEdit
	if patch.HasUDPPorts {
		edits = append(edits, setItemEdit(doc, sc, "string", "PortsUDP", formatUDPPorts(patch.UDPPorts)))
	}
	if patch.Enabled != nil {
		val := "false"
		if *patch.Enabled {
			val = "true"
		}
		edits = append(edits, setItemEdit(doc, wg, "bool", "Enabled", val))
	}
	if patch.PresharedKey != nil {
		edits = append(edits, setItemEdit(doc, wg, "string", "PresharedKey", *patch.PresharedKey))
	}
	if patch.PrivateKey != nil {
		edits = append(edits, setItemEdit(doc, wg, "string", "PrivateKey", *patch.PrivateKey))
	}
	return applySEEdits(raw, edits)
}

func patchWireGuardHubNetwork(raw, hub, gateway, subnet string) (string, error) {
	doc, err := parseSEConfig(raw)
	if err != nil {
		return "", err
	}
	hubs := doc.Root.child("VirtualHUB")
	if hubs == nil {
		return "", httpError{404, "Virtual Hub not found in server configuration."}
	}
	hubNode := hubs.child(hub)
	if hubNode == nil {
		return "", httpError{404, "Virtual Hub not found in server configuration."}
	}
	opt := hubNode.child("Option")
	if opt == nil {
		return "", httpError{502, "Hub options section missing in server configuration."}
	}
	edits := []seEdit{
		setItemEdit(doc, opt, "string", "DefaultGateway", gateway),
		setItemEdit(doc, opt, "string", "DefaultSubnet", subnet),
	}
	return applySEEdits(raw, edits)
}

func formatWGPeerBlock(eol string, depth int, publicKey, hub, user string) string {
	d0 := strings.Repeat("\t", depth)
	d1 := strings.Repeat("\t", depth+1)
	return d0 + "declare " + seCfgEscape(publicKey) + eol +
		d0 + "{" + eol +
		d1 + "string Hub " + seCfgEscape(hub) + eol +
		d1 + "string User " + seCfgEscape(user) + eol +
		d0 + "}" + eol
}

func patchWireGuardAddPeer(raw, publicKey, hub, user string) (string, error) {
	doc, err := parseSEConfig(raw)
	if err != nil {
		return "", err
	}
	if doc.Root.child("ServerConfiguration").child("Proto").child("WireGuard") == nil {
		return "", httpError{400, M{"message": "This SoftEther server does not expose WireGuard protocol settings.", "se_error": "UnsupportedError"}}
	}
	list := doc.Root.child("WireGuardKeyList")
	if list != nil {
		if existing := list.child(publicKey); existing != nil {
			return "", httpError{409, "This WireGuard public key is already registered."}
		}
		block := formatWGPeerBlock(doc.EOL, list.Depth+1, publicKey, hub, user)
		return applySEEdits(raw, []seEdit{{start: list.CloseBraceStart, end: list.CloseBraceStart, text: block}})
	}

	insertAt := doc.Root.CloseBraceStart
	if vh := doc.Root.child("VirtualHUB"); vh != nil {
		insertAt = vh.DeclStart
	}
	d1 := strings.Repeat("\t", doc.Root.Depth+1)
	block := d1 + "declare WireGuardKeyList" + doc.EOL +
		d1 + "{" + doc.EOL +
		formatWGPeerBlock(doc.EOL, doc.Root.Depth+2, publicKey, hub, user) +
		d1 + "}" + doc.EOL
	return applySEEdits(raw, []seEdit{{start: insertAt, end: insertAt, text: block}})
}

func patchWireGuardDeletePeer(raw, publicKey string) (string, error) {
	doc, err := parseSEConfig(raw)
	if err != nil {
		return "", err
	}
	list := doc.Root.child("WireGuardKeyList")
	if list == nil {
		return "", httpError{404, "WireGuard peer key not found."}
	}
	target := list.child(publicKey)
	if target == nil {
		return "", httpError{404, "WireGuard peer key not found."}
	}
	return applySEEdits(raw, []seEdit{{start: target.DeclStart, end: target.End, text: ""}})
}

func patchWireGuardReplacePeerKey(raw, oldPublicKey, newPublicKey, hub, user string) (string, error) {
	doc, err := parseSEConfig(raw)
	if err != nil {
		return "", err
	}
	list := doc.Root.child("WireGuardKeyList")
	if list == nil {
		return "", httpError{404, "WireGuard peer key not found."}
	}
	target := list.child(oldPublicKey)
	if target == nil {
		return "", httpError{404, "WireGuard peer key not found."}
	}
	if list.child(newPublicKey) != nil {
		return "", httpError{409, "This WireGuard public key is already registered."}
	}
	block := formatWGPeerBlock(doc.EOL, list.Depth+1, newPublicKey, hub, user)
	return applySEEdits(raw, []seEdit{{start: target.DeclStart, end: target.End, text: block}})
}

func parseIPv4(s string) (net.IP, error) {
	trimmed := strings.TrimSpace(s)
	if idx := strings.IndexByte(trimmed, '/'); idx != -1 {
		trimmed = trimmed[:idx]
	}
	ip := net.ParseIP(trimmed).To4()
	if ip == nil {
		return nil, fmt.Errorf("invalid IPv4 address: %s", s)
	}
	return ip, nil
}

func validateIPv4Mask(s string) (net.IP, int, error) {
	ip, err := parseIPv4(s)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid subnet mask: %s", s)
	}
	mask := net.IPv4Mask(ip[0], ip[1], ip[2], ip[3])
	ones, bits := mask.Size()
	if bits != 32 || ones < 8 || ones > 30 {
		return nil, 0, fmt.Errorf("subnet mask must be a contiguous IPv4 mask between /8 and /30")
	}
	return ip, ones, nil
}

func ipToUint32(ip net.IP) uint32 {
	v4 := ip.To4()
	return binary.BigEndian.Uint32(v4)
}

func uint32ToIP(v uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return net.IP(b[:]).String()
}

func suggestPeerIP(gateway, subnet string, used []string) string {
	gwIP, err := parseIPv4(gateway)
	if err != nil || gwIP.Equal(net.IPv4zero) {
		return ""
	}
	maskIP, ones, err := validateIPv4Mask(subnet)
	if err != nil {
		return ""
	}
	gw := ipToUint32(gwIP)
	mask := ipToUint32(maskIP)
	network := gw & mask
	broadcast := network | ^mask
	if broadcast <= network+2 {
		return ""
	}
	taken := map[uint32]bool{network: true, broadcast: true, gw: true}
	for _, item := range used {
		if ip, parseErr := parseIPv4(item); parseErr == nil {
			taken[ipToUint32(ip)] = true
		}
	}
	// Cap search to avoid huge scans on /8 networks; first free IP will be near the front.
	limit := min(broadcast-1, network+uint32(1<<(32-ones))-2)
	for candidate := network + 2; candidate <= limit; candidate++ {
		if !taken[candidate] {
			return uint32ToIP(candidate)
		}
	}
	if !taken[network+1] && network+1 < broadcast {
		return uint32ToIP(network + 1)
	}
	return ""
}

func pickWireGuardPort(ports []int) int {
	for _, p := range ports {
		if p == defaultWireGuardPort {
			return p
		}
	}
	reserved := map[int]bool{443: true, 992: true, 1194: true, 5555: true}
	for _, p := range ports {
		if !reserved[p] {
			return p
		}
	}
	return defaultWireGuardPort
}

func containsPort(ports []int, target int) bool {
	for _, p := range ports {
		if p == target {
			return true
		}
	}
	return false
}

// mergeWireGuardUDPPort ensures the selected WireGuard port is present in
// SoftEther's PortsUDP listener list, replacing a prior non-reserved WireGuard
// port while keeping standard SoftEther/OpenVPN listeners (443, 992, 1194, 5555).
func mergeWireGuardUDPPort(existing []int, prevWGPort, nextWGPort int) []int {
	reserved := map[int]bool{443: true, 992: true, 1194: true, 5555: true}
	seen := map[int]bool{}
	out := make([]int, 0, len(existing)+1)
	for _, p := range existing {
		if p < 1 || p > 65535 || seen[p] {
			continue
		}
		if prevWGPort > 0 && prevWGPort != nextWGPort && p == prevWGPort && !reserved[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if nextWGPort >= 1 && nextWGPort <= 65535 && !seen[nextWGPort] {
		out = append(out, nextWGPort)
	}
	sort.Ints(out)
	return out
}

func (a *App) savedWireguardPort(serverID int64) int {
	raw := a.setting("wireguard_ports")
	m, ok := raw.(map[string]any)
	if !ok {
		return 0
	}
	p := int(integer(m[strconv.FormatInt(serverID, 10)]))
	if p >= 1 && p <= 65535 {
		return p
	}
	return 0
}

func (a *App) setWireguardPort(serverID int64, port int) {
	next := M{}
	if existing, ok := a.setting("wireguard_ports").(map[string]any); ok {
		for k, v := range existing {
			next[k] = v
		}
	}
	next[strconv.FormatInt(serverID, 10)] = port
	a.set("wireguard_ports", next)
}

func (a *App) wireguardPort(serverID int64, state *wgConfigState) int {
	if saved := a.savedWireguardPort(serverID); saved > 0 {
		return saved
	}
	return pickWireGuardPort(state.UDPPorts)
}

func cleanConfigLineField(s string) string {
	s = strings.NewReplacer("\r", "", "\n", "").Replace(strings.TrimSpace(s))
	return s
}

func buildWireGuardClientConfig(privateKey, address, dns, serverPub, presharedKey, endpointHost string, endpointPort int, allowedIPs string, keepalive int) string {
	privateKey = cleanConfigLineField(privateKey)
	if privateKey == "" {
		privateKey = "<CLIENT_PRIVATE_KEY>"
	}
	addr := cleanConfigLineField(address)
	if addr == "" {
		addr = "192.168.30.2/32"
	} else if !strings.Contains(addr, "/") {
		addr += "/32"
	}
	endpointHost = cleanConfigLineField(endpointHost)
	if strings.HasPrefix(endpointHost, "[") && strings.HasSuffix(endpointHost, "]") {
		if inner := endpointHost[1 : len(endpointHost)-1]; net.ParseIP(inner) != nil {
			endpointHost = inner
		}
	}
	if endpointHost == "" {
		endpointHost = "YOUR_SERVER_IP"
	}
	if endpointPort <= 0 || endpointPort > 65535 {
		endpointPort = defaultWireGuardPort
	}
	allowedIPs = cleanConfigLineField(allowedIPs)
	if allowedIPs == "" {
		allowedIPs = defaultWireGuardAllowedIPs
	}
	dns = cleanConfigLineField(dns)
	serverPub = cleanConfigLineField(serverPub)
	presharedKey = cleanConfigLineField(presharedKey)
	if keepalive < 0 {
		keepalive = 0
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", privateKey)
	fmt.Fprintf(&b, "Address = %s\n", addr)
	if dns != "" {
		fmt.Fprintf(&b, "DNS = %s\n", dns)
	}
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", serverPub)
	if presharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", presharedKey)
	}
	fmt.Fprintf(&b, "Endpoint = %s\n", net.JoinHostPort(endpointHost, strconv.Itoa(endpointPort)))
	fmt.Fprintf(&b, "AllowedIPs = %s\n", allowedIPs)
	if keepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", keepalive)
	}
	return b.String()
}

func (a *App) loadWireGuardMeta(serverID int64) map[string]M {
	out := map[string]M{}
	for _, row := range query(a.db, `SELECT PublicKey,HubName,UserName,Label,AssignedIP,EncryptedPrivateKey,CreatedDate,UpdatedDate FROM WireGuardPeer WHERE ServerID=?`, serverID) {
		out[str(row["PublicKey"])] = row
	}
	return out
}

func (a *App) upsertWireGuardMeta(serverID int64, pubKey, hub, user, label, assignedIP, encPriv string) {
	stamp := now()
	execute(a.db, `INSERT INTO WireGuardPeer(ServerID,PublicKey,HubName,UserName,Label,AssignedIP,EncryptedPrivateKey,CreatedDate,UpdatedDate) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(ServerID,PublicKey) DO UPDATE SET HubName=excluded.HubName,UserName=excluded.UserName,Label=excluded.Label,AssignedIP=excluded.AssignedIP,EncryptedPrivateKey=CASE WHEN excluded.EncryptedPrivateKey!='' THEN excluded.EncryptedPrivateKey ELSE WireGuardPeer.EncryptedPrivateKey END,UpdatedDate=excluded.UpdatedDate`, serverID, pubKey, hub, user, label, assignedIP, encPriv, stamp, stamp)
}

func (a *App) decryptWireGuardPrivateKey(enc string) string {
	if enc == "" {
		return ""
	}
	plain, err := a.decrypt(enc)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(plain)
}

func (a *App) fetchRawConfig(ctx context.Context) string {
	cfg := a.rpc(ctx, "GetConfig", nil)
	raw, err := base64.StdEncoding.DecodeString(str(cfg["FileData_bin"]))
	if err != nil {
		fail(502, "SoftEther returned an invalid configuration buffer.")
	}
	return string(raw)
}

// SetConfig restarts SoftEther; the shared RPC path also handles a dropped reply.
func (a *App) pushWireGuardConfig(ctx context.Context, updated string) bool {
	out := a.rpc(ctx, "SetConfig", M{"FileData_bin": base64.StdEncoding.EncodeToString([]byte(updated))})
	return boolean(out["restarting"])
}

func (a *App) enrichWireGuardPeers(serverID int64, state *wgConfigState) []M {
	meta := a.loadWireGuardMeta(serverID)
	peers := make([]M, 0, len(state.Peers))
	for _, p := range state.Peers {
		entry := M{
			"public_key":      p.PublicKey,
			"hub":             p.Hub,
			"user":            p.User,
			"label":           "",
			"assigned_ip":     "",
			"has_private_key": false,
		}
		if m, ok := meta[p.PublicKey]; ok {
			entry["label"] = str(m["Label"])
			entry["assigned_ip"] = str(m["AssignedIP"])
			entry["created_date"] = str(m["CreatedDate"])
			entry["has_private_key"] = a.decryptWireGuardPrivateKey(str(m["EncryptedPrivateKey"])) != ""
		}
		peers = append(peers, entry)
	}
	return peers
}

func (a *App) registerWireGuard() {
	a.endpoint("GET /server/wireguard", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		raw := a.fetchRawConfig(r.Context())
		state, err := parseWireGuardState(raw)
		if err != nil {
			fail(502, err.Error())
		}
		if !state.Supported {
			return M{"supported": false}
		}
		serverID := a.serverIDFromContext(r.Context())
		conn := a.connection(r.Context())
		defaultHost := strings.TrimSpace(str(conn["public_host"]))
		if defaultHost == "" {
			defaultHost = str(conn["host"])
		}
		wgPort := a.wireguardPort(serverID, state)
		return M{
			"supported":                true,
			"enabled":                  state.Enabled,
			"public_key":               state.PublicKey,
			"preshared_key":            state.PresharedKey,
			"has_preshared_key":        state.PresharedKey != "",
			"udp_ports":                state.UDPPorts,
			"default_udp_port":         wgPort,
			"wireguard_port":           wgPort,
			"wireguard_port_listening": containsPort(state.UDPPorts, wgPort),
			"default_endpoint_host":    defaultHost,
			"peers":                    a.enrichWireGuardPeers(serverID, state),
			"hub_networks":             state.HubNetworks,
		}
	})

	a.endpoint("PUT /server/wireguard", func(w http.ResponseWriter, r *http.Request, u M) any {
		body := readBody(w, r)
		if body["confirm_restart"] != true {
			fail(422, "confirm_restart must be true.")
		}
		var patch wgServerPatch
		if v, exists := body["enabled"]; exists {
			b, ok := v.(bool)
			if !ok {
				fail(422, "enabled must be a boolean.")
			}
			patch.Enabled = &b
		}
		if v, exists := body["udp_ports"]; exists {
			ports, err := normalizeUDPPorts(v)
			if err != nil {
				fail(422, err.Error())
			}
			patch.UDPPorts = ports
			patch.HasUDPPorts = true
		}
		var wgPort int
		hasWGPort := false
		if v, exists := body["wireguard_port"]; exists {
			p := int(integer(v))
			if p < 1 || p > 65535 {
				fail(422, "wireguard_port must be between 1 and 65535.")
			}
			wgPort = p
			hasWGPort = true
		}
		if boolean(body["regenerate_preshared_key"]) {
			k, err := wgGenerateRandomKey()
			check(err)
			patch.PresharedKey = &k
		} else if v, exists := body["preshared_key"]; exists {
			s, ok := v.(string)
			if !ok {
				fail(422, "preshared_key must be a string.")
			}
			s = strings.TrimSpace(s)
			if s != "" {
				if _, err := wgValidateKey(s); err != nil {
					fail(422, "Invalid preshared_key: "+err.Error())
				}
			}
			patch.PresharedKey = &s
		}
		if boolean(body["regenerate_private_key"]) {
			priv, _, err := wgGenerateKeypair()
			check(err)
			patch.PrivateKey = &priv
		}
		if patch.Enabled == nil && !patch.HasUDPPorts && !hasWGPort && patch.PresharedKey == nil && patch.PrivateKey == nil {
			fail(422, "No WireGuard changes were requested.")
		}

		a.wgMu.Lock()
		defer a.wgMu.Unlock()
		serverID := a.serverIDFromContext(r.Context())
		raw := a.fetchRawConfig(r.Context())
		if hasWGPort {
			state, err := parseWireGuardState(raw)
			if err != nil {
				fail(502, err.Error())
			}
			if !state.Supported {
				fail(400, M{"message": "This SoftEther server does not expose WireGuard protocol settings.", "se_error": "UnsupportedError"})
			}
			prevWGPort := a.savedWireguardPort(serverID)
			if !patch.HasUDPPorts {
				patch.UDPPorts = mergeWireGuardUDPPort(state.UDPPorts, prevWGPort, wgPort)
				patch.HasUDPPorts = true
			}
			if patch.HasUDPPorts && !containsPort(patch.UDPPorts, wgPort) {
				fail(422, "udp_ports must include wireguard_port.")
			}
		}
		updated, err := patchWireGuardServer(raw, patch)
		check(err)
		restarting := false
		if updated != raw {
			restarting = a.pushWireGuardConfig(r.Context(), updated)
		}
		if hasWGPort {
			a.setWireguardPort(serverID, wgPort)
		}
		a.audit(u, "wireguard.updated", "server", "", "restart requested")
		return M{"restarting": restarting}
	})

	a.endpoint("PUT /hubs/{hub}/wireguard/network", func(w http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		body := readBody(w, r)
		if body["confirm_restart"] != true {
			fail(422, "confirm_restart must be true.")
		}
		gwIP, err := parseIPv4(str(body["gateway"]))
		if err != nil || gwIP.Equal(net.IPv4zero) {
			fail(422, "A valid non-zero IPv4 gateway is required.")
		}
		maskIP, _, err := validateIPv4Mask(str(body["subnet"]))
		if err != nil {
			fail(422, err.Error())
		}

		a.wgMu.Lock()
		defer a.wgMu.Unlock()
		raw := a.fetchRawConfig(r.Context())
		updated, err := patchWireGuardHubNetwork(raw, hub, gwIP.String(), maskIP.String())
		check(err)
		if updated == raw {
			return M{"restarting": false}
		}
		restarting := a.pushWireGuardConfig(r.Context(), updated)
		a.audit(u, "wireguard.network_updated", "hub", hub, gwIP.String()+" / "+maskIP.String())
		return M{"restarting": restarting}
	})

	a.endpoint("POST /hubs/{hub}/wireguard/peers", func(w http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		body := readBody(w, r)
		if body["confirm_restart"] != true {
			fail(422, "confirm_restart must be true.")
		}
		username := strings.TrimSpace(str(body["user"]))
		if username == "" {
			fail(422, "A user name is required.")
		}
		// Verify the user exists on the target hub before modifying server config.
		a.rpc(r.Context(), "GetUser", M{"HubName_str": hub, "Name_str": username})

		label := strings.TrimSpace(str(body["label"]))
		generate := boolean(body["generate"])
		var clientPriv, pubKey string
		if generate {
			var err error
			clientPriv, pubKey, err = wgGenerateKeypair()
			check(err)
		} else {
			pubKey = strings.TrimSpace(str(body["public_key"]))
			if _, err := wgValidateKey(pubKey); err != nil {
				fail(422, err.Error())
			}
		}

		a.wgMu.Lock()
		defer a.wgMu.Unlock()
		raw := a.fetchRawConfig(r.Context())
		state, err := parseWireGuardState(raw)
		if err != nil {
			fail(502, err.Error())
		}
		if !state.Supported {
			fail(400, M{"message": "This SoftEther server does not expose WireGuard protocol settings.", "se_error": "UnsupportedError"})
		}

		serverID := a.serverIDFromContext(r.Context())
		assignedIP := strings.TrimSpace(str(body["assigned_ip"]))
		if assignedIP != "" {
			ip, ipErr := parseIPv4(assignedIP)
			if ipErr != nil || ip.Equal(net.IPv4zero) {
				fail(422, "Invalid assigned_ip.")
			}
			assignedIP = ip.String()
		} else {
			var used []string
			for _, row := range query(a.db, `SELECT AssignedIP FROM WireGuardPeer WHERE ServerID=? AND HubName=? AND AssignedIP!=''`, serverID, hub) {
				used = append(used, str(row["AssignedIP"]))
			}
			for _, hn := range state.HubNetworks {
				if hn.Hub == hub {
					assignedIP = suggestPeerIP(hn.Gateway, hn.Subnet, used)
					break
				}
			}
		}

		updated, err := patchWireGuardAddPeer(raw, pubKey, hub, username)
		check(err)
		restarting := a.pushWireGuardConfig(r.Context(), updated)
		encPriv := ""
		if clientPriv != "" {
			encPriv = a.encrypt(clientPriv)
		}
		a.upsertWireGuardMeta(serverID, pubKey, hub, username, label, assignedIP, encPriv)
		a.audit(u, "wireguard.peer_added", "hub", hub, username)

		conn := a.connection(r.Context())
		configText := buildWireGuardClientConfig(
			clientPriv,
			assignedIP,
			wgBodyDNS(body),
			state.PublicKey,
			state.PresharedKey,
			wgBodyEndpointHost(body, conn),
			wgBodyEndpointPort(body, a.wireguardPort(serverID, state)),
			wgBodyAllowedIPs(body),
			wgBodyKeepalive(body),
		)
		resp := M{
			"restarting": restarting,
			"peer": M{
				"public_key":      pubKey,
				"hub":             hub,
				"user":            username,
				"label":           label,
				"assigned_ip":     assignedIP,
				"has_private_key": encPriv != "",
			},
			"client_config": configText,
		}
		if clientPriv != "" {
			resp["client_private_key"] = clientPriv
		}
		return resp
	})

	a.endpoint("POST /hubs/{hub}/wireguard/peers/delete", func(w http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		body := readBody(w, r)
		if body["confirm_restart"] != true {
			fail(422, "confirm_restart must be true.")
		}
		pubKey := strings.TrimSpace(str(body["public_key"]))
		if pubKey == "" {
			fail(422, "public_key is required.")
		}

		a.wgMu.Lock()
		defer a.wgMu.Unlock()
		raw := a.fetchRawConfig(r.Context())
		state, err := parseWireGuardState(raw)
		if err != nil {
			fail(502, err.Error())
		}
		for _, p := range state.Peers {
			if p.PublicKey == pubKey && hub != "" && p.Hub != hub {
				fail(404, "WireGuard peer key not found in this hub.")
			}
		}
		updated, err := patchWireGuardDeletePeer(raw, pubKey)
		check(err)
		restarting := a.pushWireGuardConfig(r.Context(), updated)
		serverID := a.serverIDFromContext(r.Context())
		execute(a.db, `DELETE FROM WireGuardPeer WHERE ServerID=? AND PublicKey=?`, serverID, pubKey)
		a.audit(u, "wireguard.peer_deleted", "hub", hub, pubKey)
		return M{"restarting": restarting}
	})

	a.endpoint("POST /hubs/{hub}/wireguard/peers/rotate", func(w http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		body := readBody(w, r)
		if body["confirm_restart"] != true {
			fail(422, "confirm_restart must be true.")
		}
		oldPubKey := strings.TrimSpace(str(body["public_key"]))
		if oldPubKey == "" {
			fail(422, "public_key is required.")
		}

		newPriv, newPub, err := wgGenerateKeypair()
		check(err)

		a.wgMu.Lock()
		defer a.wgMu.Unlock()
		raw := a.fetchRawConfig(r.Context())
		state, err := parseWireGuardState(raw)
		if err != nil {
			fail(502, err.Error())
		}
		if !state.Supported {
			fail(400, M{"message": "This SoftEther server does not expose WireGuard protocol settings.", "se_error": "UnsupportedError"})
		}
		var matched *wgPeerConfig
		for i := range state.Peers {
			if state.Peers[i].PublicKey == oldPubKey && state.Peers[i].Hub == hub {
				matched = &state.Peers[i]
				break
			}
		}
		if matched == nil {
			fail(404, "WireGuard peer key not found in this hub.")
		}

		updated, err := patchWireGuardReplacePeerKey(raw, oldPubKey, newPub, matched.Hub, matched.User)
		check(err)
		restarting := a.pushWireGuardConfig(r.Context(), updated)

		serverID := a.serverIDFromContext(r.Context())
		meta := a.loadWireGuardMeta(serverID)[oldPubKey]
		label := str(meta["Label"])
		assignedIP := str(meta["AssignedIP"])
		createdDate := str(meta["CreatedDate"])
		if createdDate == "" {
			createdDate = now()
		}
		encPriv := a.encrypt(newPriv)
		stamp := now()
		execute(a.db, `DELETE FROM WireGuardPeer WHERE ServerID=? AND PublicKey=?`, serverID, oldPubKey)
		execute(a.db, `INSERT INTO WireGuardPeer(ServerID,PublicKey,HubName,UserName,Label,AssignedIP,EncryptedPrivateKey,CreatedDate,UpdatedDate) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(ServerID,PublicKey) DO UPDATE SET HubName=excluded.HubName,UserName=excluded.UserName,Label=excluded.Label,AssignedIP=excluded.AssignedIP,EncryptedPrivateKey=excluded.EncryptedPrivateKey,UpdatedDate=excluded.UpdatedDate`, serverID, newPub, matched.Hub, matched.User, label, assignedIP, encPriv, createdDate, stamp)
		a.audit(u, "wireguard.peer_key_rotated", "hub", hub, matched.User)

		conn := a.connection(r.Context())
		configText := buildWireGuardClientConfig(
			newPriv,
			assignedIP,
			wgBodyDNS(body),
			state.PublicKey,
			state.PresharedKey,
			wgBodyEndpointHost(body, conn),
			wgBodyEndpointPort(body, a.wireguardPort(serverID, state)),
			wgBodyAllowedIPs(body),
			wgBodyKeepalive(body),
		)
		return M{
			"restarting": restarting,
			"peer": M{
				"public_key":      newPub,
				"hub":             matched.Hub,
				"user":            matched.User,
				"label":           label,
				"assigned_ip":     assignedIP,
				"created_date":    createdDate,
				"has_private_key": true,
			},
			"client_private_key": newPriv,
			"client_config":      configText,
		}
	})

	a.endpoint("PUT /hubs/{hub}/wireguard/peers/meta", func(w http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		body := readBody(w, r)
		pubKey := strings.TrimSpace(str(body["public_key"]))
		if pubKey == "" {
			fail(422, "public_key is required.")
		}
		label := strings.TrimSpace(str(body["label"]))
		assignedIP := strings.TrimSpace(str(body["assigned_ip"]))
		if assignedIP != "" {
			ip, err := parseIPv4(assignedIP)
			if err != nil || ip.Equal(net.IPv4zero) {
				fail(422, "Invalid assigned_ip.")
			}
			assignedIP = ip.String()
		}
		clientPriv := strings.TrimSpace(str(body["client_private_key"]))
		encPriv := ""
		if clientPriv != "" {
			derivedPub, err := wgPublicKey(clientPriv)
			if err != nil {
				fail(422, err.Error())
			}
			if derivedPub != pubKey {
				fail(422, "Client private key does not match this peer's public key.")
			}
			encPriv = a.encrypt(clientPriv)
		}
		raw := a.fetchRawConfig(r.Context())
		state, err := parseWireGuardState(raw)
		if err != nil {
			fail(502, err.Error())
		}
		var matched *wgPeerConfig
		for i := range state.Peers {
			if state.Peers[i].PublicKey == pubKey && state.Peers[i].Hub == hub {
				matched = &state.Peers[i]
				break
			}
		}
		if matched == nil {
			fail(404, "WireGuard peer key not found in this hub.")
		}
		serverID := a.serverIDFromContext(r.Context())
		a.upsertWireGuardMeta(serverID, pubKey, matched.Hub, matched.User, label, assignedIP, encPriv)
		hasKey := encPriv != ""
		if !hasKey {
			if m, ok := a.loadWireGuardMeta(serverID)[pubKey]; ok {
				hasKey = a.decryptWireGuardPrivateKey(str(m["EncryptedPrivateKey"])) != ""
			}
		}
		a.audit(u, "wireguard.peer_meta_updated", "hub", hub, matched.User)
		return M{
			"public_key":      pubKey,
			"hub":             matched.Hub,
			"user":            matched.User,
			"label":           label,
			"assigned_ip":     assignedIP,
			"has_private_key": hasKey,
		}
	})

	a.endpoint("POST /hubs/{hub}/wireguard/config", func(w http.ResponseWriter, r *http.Request, _ M) any {
		hub := r.PathValue("hub")
		body := readBody(w, r)
		raw := a.fetchRawConfig(r.Context())
		state, err := parseWireGuardState(raw)
		if err != nil {
			fail(502, err.Error())
		}
		if !state.Supported {
			fail(400, M{"message": "This SoftEther server does not expose WireGuard protocol settings.", "se_error": "UnsupportedError"})
		}

		pubKey := strings.TrimSpace(str(body["public_key"]))
		assignedIP := strings.TrimSpace(str(body["assigned_ip"]))
		serverID := a.serverIDFromContext(r.Context())
		var peerMeta M
		if pubKey != "" {
			peerMeta = a.loadWireGuardMeta(serverID)[pubKey]
			if assignedIP == "" && peerMeta != nil {
				assignedIP = str(peerMeta["AssignedIP"])
			}
		}
		if assignedIP == "" {
			var used []string
			for _, row := range query(a.db, `SELECT AssignedIP FROM WireGuardPeer WHERE ServerID=? AND HubName=? AND AssignedIP!=''`, serverID, hub) {
				used = append(used, str(row["AssignedIP"]))
			}
			for _, hn := range state.HubNetworks {
				if hn.Hub == hub {
					assignedIP = suggestPeerIP(hn.Gateway, hn.Subnet, used)
					break
				}
			}
		}

		clientPriv := strings.TrimSpace(str(body["client_private_key"]))
		if clientPriv != "" {
			derivedPub, privErr := wgPublicKey(clientPriv)
			if privErr != nil {
				fail(422, privErr.Error())
			}
			if pubKey != "" && derivedPub != pubKey {
				fail(422, "Client private key does not match this peer's public key.")
			}
		} else if peerMeta != nil {
			clientPriv = a.decryptWireGuardPrivateKey(str(peerMeta["EncryptedPrivateKey"]))
		}

		conn := a.connection(r.Context())
		configText := buildWireGuardClientConfig(
			clientPriv,
			assignedIP,
			wgBodyDNS(body),
			state.PublicKey,
			state.PresharedKey,
			wgBodyEndpointHost(body, conn),
			wgBodyEndpointPort(body, a.wireguardPort(serverID, state)),
			wgBodyAllowedIPs(body),
			wgBodyKeepalive(body),
		)
		filename := strings.TrimSpace(str(body["filename"]))
		if filename == "" {
			filename = "wg-" + hub + ".conf"
		}
		if !strings.HasSuffix(strings.ToLower(filename), ".conf") {
			filename += ".conf"
		}
		return M{"filename": filename, "config": configText, "assigned_ip": assignedIP, "has_private_key": clientPriv != ""}
	})
}

func wgBodyEndpointHost(body M, conn M) string {
	host := strings.TrimSpace(str(body["endpoint_host"]))
	if host == "" {
		host = strings.TrimSpace(str(conn["public_host"]))
	}
	if host == "" {
		host = str(conn["host"])
	}
	return host
}

func wgBodyEndpointPort(body M, fallback int) int {
	p := int(integer(body["endpoint_port"]))
	if p <= 0 || p > 65535 {
		return fallback
	}
	return p
}

func wgBodyDNS(body M) string {
	dns := strings.TrimSpace(str(body["dns"]))
	if _, hasDNS := body["dns"]; !hasDNS {
		return defaultWireGuardDNS
	}
	return dns
}

func wgBodyAllowedIPs(body M) string {
	allowed := strings.TrimSpace(str(body["allowed_ips"]))
	if allowed == "" {
		return defaultWireGuardAllowedIPs
	}
	return allowed
}

func wgBodyKeepalive(body M) int {
	if _, has := body["persistent_keepalive"]; has {
		return int(integer(body["persistent_keepalive"]))
	}
	return defaultWireGuardKeepalive
}
