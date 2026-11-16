package manager

import (
	"context"
	"crypto/rc4"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/bits"
	"net/http"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

//go:embed vpn-defaults.json
var vpnDefaultsJSON []byte

//go:embed vpn-fields.json
var vpnFieldsJSON []byte
var vpnDefaults = func() M { var out M; check(json.Unmarshal(vpnDefaultsJSON, &out)); return out }()

func sha0(data []byte) []byte {
	h := [5]uint32{0x67452301, 0xEFCDAB89, 0x98BADCFE, 0x10325476, 0xC3D2E1F0}
	message := append([]byte{}, data...)
	message = append(message, 0x80)
	for len(message)%64 != 56 {
		message = append(message, 0)
	}
	message = binary.BigEndian.AppendUint64(message, uint64(len(data))*8)
	for offset := 0; offset < len(message); offset += 64 {
		var w [80]uint32
		for i := 0; i < 16; i++ {
			w[i] = binary.BigEndian.Uint32(message[offset+i*4:])
		}
		for i := 16; i < 80; i++ {
			w[i] = w[i-3] ^ w[i-8] ^ w[i-14] ^ w[i-16]
		}
		a, b, c, d, e := h[0], h[1], h[2], h[3], h[4]
		for i := 0; i < 80; i++ {
			var f, k uint32
			switch {
			case i < 20:
				f, k = (b&c)|(^b&d), 0x5A827999
			case i < 40:
				f, k = b^c^d, 0x6ED9EBA1
			case i < 60:
				f, k = (b&c)|(b&d)|(c&d), 0x8F1BBCDC
			default:
				f, k = b^c^d, 0xCA62C1D6
			}
			a, b, c, d, e = bits.RotateLeft32(a, 5)+f+e+k+w[i], a, bits.RotateLeft32(b, 30), c, d
		}
		h[0] += a
		h[1] += b
		h[2] += c
		h[3] += d
		h[4] += e
	}
	out := []byte{}
	for _, v := range h {
		out = binary.BigEndian.AppendUint32(out, v)
	}
	return out
}
func hashedPassword(password, username string) string {
	return base64.StdEncoding.EncodeToString(sha0([]byte(password + cases.Upper(language.Und).String(username))))
}
func proxyPassword(password string) string {
	c, err := rc4.NewCipher([]byte("EncryptP"))
	check(err)
	out := make([]byte, len([]byte(password)))
	c.XORKeyStream(out, []byte(password))
	return base64.StdEncoding.EncodeToString(out)
}
func vpnOptions(raw any) M {
	out := clone(vpnDefaults)
	bounds := map[string][2]int64{"MaxConnection": {1, 32}, "PortUDP": {0, 65535}, "NumRetry": {0, 1<<32 - 1}, "RetryInterval": {5, 3600}, "AdditionalConnectionInterval": {1, 3600}, "ConnectionDisconnectSpan": {0, 1<<32 - 1}, "ProxyType": {0, 3}, "ProxyPort": {0, 65535}}
	for key, val := range object(raw) {
		def, ok := out[key]
		if !ok {
			continue
		}
		switch def.(type) {
		case bool:
			out[key] = boolean(val)
		case float64:
			b := bounds[key]
			out[key] = max(b[0], min(b[1], integer(val)))
		default:
			out[key] = str(val)
		}
	}
	return out
}
func cfgEscape(s string) string {
	if s == "" {
		return "$"
	}
	var out strings.Builder
	for _, c := range s {
		if c <= 31 || c == ' ' || c == '$' {
			fmt.Fprintf(&out, "$%02X", c)
		} else {
			out.WriteRune(c)
		}
	}
	return out.String()
}
func cfgValue(v any) string {
	switch n := v.(type) {
	case bool:
		if n {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%.0f", n)
	case int, int64, json.Number:
		return str(n)
	default:
		return cfgEscape(str(v))
	}
}
func vpnDocument(values, options M) string {
	var fields map[string][][]any
	check(json.Unmarshal(vpnFieldsJSON, &fields))
	var out strings.Builder
	out.WriteString("# VPN Client VPN Connection Setting File\n# \n# This file is exported from the SoftEther Manager panel.\n# The contents of this file can be edited using a text editor.\n# \n# When this file is imported to the Client Connection Manager it can be used immediately.\n\ndeclare root\n{\n")
	emit := func(section, indent string) {
		for _, field := range fields[section] {
			value := field[2]
			if s, ok := value.(string); ok {
				if strings.HasPrefix(s, "@") {
					value = options[s[1:]]
				}
				if strings.HasPrefix(s, "$") {
					value = values[s[1:]]
				}
			}
			fmt.Fprintf(&out, "%s%s %s %s\n", indent, field[0], field[1], cfgValue(value))
		}
	}
	emit("root", "\t")
	out.WriteString("\n\tdeclare ClientAuth\n\t{\n")
	emit("client_auth", "\t\t")
	out.WriteString("\t}\n\tdeclare ClientOption\n\t{\n")
	emit("client_option", "\t\t")
	out.WriteString("\t}\n}\n")
	return out.String()
}
func buildVPN(host string, port int64, hub, name string, auth int64, account, password, hash string, raw any) string {
	opts := vpnOptions(raw)
	types := map[int64]int64{0: 0, 1: 1, 2: 3, 3: 3, 4: 2, 5: 2}
	clientAuth, ok := types[auth]
	if !ok {
		clientAuth = 1
	}
	hashed, plain := "", ""
	if password != "" {
		if clientAuth == 2 {
			plain = password
		} else {
			clientAuth = 1
			hashed = hashedPassword(password, name)
		}
	} else if hash != "" && clientAuth != 2 {
		clientAuth = 1
		hashed = hash
	}
	if account == "" {
		account = hub + " - " + name
	}
	proxy := ""
	if str(opts["ProxyPassword"]) != "" {
		proxy = proxyPassword(str(opts["ProxyPassword"]))
	}
	doc := vpnDocument(M{"host": host, "port": port, "hub": hub, "username": name, "account": account, "auth_type": clientAuth, "hashed": hashed, "plain": plain, "proxy_password": proxy}, opts)
	return "\ufeff" + strings.ReplaceAll(doc, "\n", "\r\n")
}
func renderName(template string, values M) string {
	for key, val := range values {
		template = strings.ReplaceAll(template, "{"+key+"}", str(val))
	}
	return strings.TrimSpace(template)
}
func safeFilename(name string) string {
	var out strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("-_.", r) {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	s := strings.Trim(out.String(), "._")
	if s == "" {
		s = "connection"
	}
	if !strings.HasSuffix(strings.ToLower(s), ".vpn") {
		s += ".vpn"
	}
	return s
}
func (a *App) saveCredential(serverID int64, hub, name, hash string) {
	if serverID == 0 {
		serverID = 1
	}
	execute(a.db, `INSERT INTO VpnUserCredential(ServerID,HubName,UserName,PasswordHash,UpdatedDate) VALUES(?,?,?,?,?) ON CONFLICT(ServerID,HubName,UserName) DO UPDATE SET PasswordHash=excluded.PasswordHash,UpdatedDate=excluded.UpdatedDate`, serverID, hub, name, "enc:"+a.encrypt(hash), now())
}
func (a *App) storedCredential(serverID int64, hub, name string) string {
	if serverID == 0 {
		serverID = 1
	}
	r := one(a.db, `SELECT PasswordHash FROM VpnUserCredential WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE`, serverID, hub, name)
	if r == nil {
		return ""
	}
	hash, err := a.decrypt(strings.TrimPrefix(str(r["PasswordHash"]), "enc:"))
	check(err)
	return hash
}
func (a *App) migrateCredentials() {
	// Upgrade old rows once; never treat a damaged ciphertext as a usable hash.
	tx, err := a.db.Begin()
	check(err)
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT VpnUserCredentialID,PasswordHash FROM VpnUserCredential WHERE PasswordHash NOT LIKE 'enc:%'`)
	check(err)
	legacy := []M{}
	for rows.Next() {
		var id int64
		var hash string
		check(rows.Scan(&id, &hash))
		legacy = append(legacy, M{"id": id, "hash": hash})
	}
	check(rows.Err())
	check(rows.Close())
	for _, row := range legacy {
		_, err = tx.Exec(`UPDATE VpnUserCredential SET PasswordHash=? WHERE VpnUserCredentialID=?`, "enc:"+a.encrypt(str(row["hash"])), row["id"])
		check(err)
	}
	check(tx.Commit())
	if len(legacy) > 0 {
		_, err = a.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		check(err)
	}
}
func (a *App) obtainCredential(ctx context.Context, serverID int64, hub, name string) string {
	if hash := a.storedCredential(serverID, hub, name); hash != "" {
		return hash
	}
	out := a.rpc(ctx, "GetConfig", nil)
	raw, err := base64.StdEncoding.DecodeString(str(out["FileData_bin"]))
	if err != nil {
		return ""
	}
	stack := []string{}
	pending := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if strings.HasPrefix(line, "declare ") {
			pending = strings.TrimPrefix(line, "declare ")
		} else if line == "{" {
			stack = append(stack, pending)
			pending = ""
		} else if line == "}" {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		} else {
			target := []string{"root", "VirtualHUB", hub, "SecurityAccountDatabase", "UserList", name}
			if len(stack) < 6 || !strings.EqualFold(strings.Join(stack[:6], "\x00"), strings.Join(target, "\x00")) {
				continue
			}
			if len(stack) > 7 || (len(stack) == 7 && stack[6] != "AuthData") {
				continue
			}
			for _, key := range []string{"byte AuthPassword ", "byte HashedKey "} {
				if strings.HasPrefix(line, key) {
					hash := strings.TrimSpace(strings.TrimPrefix(line, key))
					if decoded, e := base64.StdEncoding.DecodeString(hash); e == nil && len(decoded) == 20 {
						a.saveCredential(serverID, hub, name, hash)
						return hash
					}
				}
			}
		}
	}
	return ""
}
func (a *App) templateState() M {
	opts := vpnOptions(a.setting("vpn_template"))
	proxy := ""
	if str(opts["ProxyPassword"]) != "" {
		proxy = "{scrambled}"
	}
	return M{"options": opts, "defaults": vpnDefaults, "account_name_template": a.setting("vpn_account_name_template"), "filename_template": a.setting("vpn_filename_template"), "embed_password_default": a.setting("vpn_embed_password_default"), "template": vpnDocument(M{"host": "{host}", "port": "{port}", "hub": "{hub}", "username": "{username}", "account": "{account_name}", "auth_type": "{auth_type}", "hashed": "{hashed_password}", "plain": "{plain_password}", "proxy_password": proxy}, opts)}
}
func (a *App) registerVPN() {
	a.endpoint("GET /system/vpn-template", func(_ http.ResponseWriter, _ *http.Request, _ M) any { return a.templateState() })
	a.endpoint("PUT /system/vpn-template", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		if options, ok := b["options"]; ok {
			a.set("vpn_template", vpnOptions(options))
		}
		for _, pair := range []struct{ field, setting, def string }{{"account_name_template", "vpn_account_name_template", "{hub} - {username}"}, {"filename_template", "vpn_filename_template", "{hub}-{username}"}} {
			if v, ok := b[pair.field]; ok && v != nil {
				text := strings.TrimSpace(str(v))
				if len(text) > 200 {
					fail(422, "Naming template is too long.")
				}
				if text == "" {
					text = pair.def
				}
				a.set(pair.setting, text)
			}
		}
		if v, ok := b["embed_password_default"]; ok {
			on, valid := v.(bool)
			if !valid {
				fail(422, "embed_password_default must be a boolean.")
			}
			a.set("vpn_embed_password_default", on)
		}
		a.audit(u, "settings.vpn_template_updated", "panel", "", "")
		return a.templateState()
	})
	a.endpoint("GET /hubs/{hub}/users/{name}/credential-state", func(_ http.ResponseWriter, r *http.Request, _ M) (result any) {
		serverID := a.serverIDFromContext(r.Context())
		hub, name := r.PathValue("hub"), r.PathValue("name")
		if a.storedCredential(serverID, hub, name) != "" {
			return M{"available": true, "source": "panel"}
		}
		result = M{"available": false, "source": nil}
		defer func() { _ = recover() }()
		if a.obtainCredential(r.Context(), serverID, hub, name) != "" {
			result = M{"available": true, "source": "server_config"}
		}
		return result
	})
	a.endpoint("POST /hubs/{hub}/users/{name}/vpn-file", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		serverID := a.serverIDFromContext(r.Context())
		hub, name := r.PathValue("hub"), r.PathValue("name")
		host := strings.TrimSpace(str(b["host"]))
		port := integer(withDefault(b, "port", 443))
		if host == "" || strings.ContainsAny(host, "\r\n") || port < 1 || port > 65535 {
			fail(422, "A valid host and port are required.")
		}
		user := a.rpc(r.Context(), "GetUser", M{"HubName_str": hub, "Name_str": name})
		auth := integer(withDefault(user, "AuthType_u32", 1))
		password, hash := "", ""
		if boolean(withDefault(b, "embed_password", true)) {
			password = str(b["password"])
			if auth == 1 {
				if password != "" {
					hash = hashedPassword(password, name)
					a.saveCredential(serverID, hub, name, hash)
				} else {
					hash = a.obtainCredential(r.Context(), serverID, hub, name)
					if hash == "" {
						fail(409, "No stored credential for this user; type the password once or turn off embedding.")
					}
				}
			}
		}
		values := M{"hub": hub, "username": name, "host": host, "port": port}
		account := strings.TrimSpace(str(b["account_name"]))
		if account == "" {
			account = renderName(str(a.setting("vpn_account_name_template")), values)
		}
		filename := str(b["filename"])
		if filename == "" {
			filename = renderName(str(a.setting("vpn_filename_template")), values)
		}
		a.audit(u, "user.vpn_file", "vpn_user", name, "hub "+hub)
		return M{"filename": safeFilename(filename), "content": buildVPN(host, port, hub, name, auth, account, password, hash, a.setting("vpn_template"))}
	})
}
