package manager

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// Only edit the boolean in root/DDnsClient; preserve every other configuration byte.
func ddnsConfigValue(config string) (start, end int, disabled bool, err error) {
	stack := []string{}
	pending := ""
	start, end = -1, -1
	offset := 0
	roots := 0
	sections := 0
	for _, line := range strings.SplitAfter(config, "\n") {
		text := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		switch {
		case strings.HasPrefix(text, "declare "):
			if pending != "" {
				return 0, 0, false, fmt.Errorf("Invalid server configuration.")
			}
			pending = strings.TrimSpace(strings.TrimPrefix(text, "declare "))
		case text == "{":
			if pending == "" || (len(stack) == 0 && pending != "root") {
				return 0, 0, false, fmt.Errorf("Invalid server configuration.")
			}
			if len(stack) == 0 {
				roots++
			}
			if len(stack) == 1 && pending == "DDnsClient" {
				sections++
			}
			stack = append(stack, pending)
			pending = ""
		case text == "}":
			if len(stack) == 0 || pending != "" {
				return 0, 0, false, fmt.Errorf("Invalid server configuration.")
			}
			stack = stack[:len(stack)-1]
		default:
			fields := strings.Fields(text)
			if len(stack) == 2 && stack[0] == "root" && stack[1] == "DDnsClient" && len(fields) >= 2 && fields[1] == "Disabled" {
				if start != -1 || len(fields) != 3 || fields[0] != "bool" || (fields[2] != "true" && fields[2] != "false") {
					return 0, 0, false, fmt.Errorf("Invalid dynamic DNS configuration.")
				}
				start = offset + strings.LastIndex(line, fields[2])
				end = start + len(fields[2])
				disabled = fields[2] == "true"
			}
		}
		offset += len(line)
	}
	if len(stack) != 0 || pending != "" || roots != 1 {
		return 0, 0, false, fmt.Errorf("Invalid server configuration.")
	}
	if start == -1 || sections != 1 {
		return 0, 0, false, fmt.Errorf("The server configuration does not expose a dynamic DNS switch.")
	}
	return start, end, disabled, nil
}

func (a *App) registerDDNS() {
	a.endpoint("GET /server/ddns", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		out, err := a.client(r.Context()).Call(r.Context(), "GetDDnsClientStatus", nil)
		if err == nil {
			out["enabled"] = true
			return out
		}
		// SoftEther also reports Unsupported when DDNS is intentionally disabled.
		rpcErr, ok := err.(httpError)
		if ok && str(object(rpcErr.Detail)["se_error"]) == "UnsupportedError" {
			config, configErr := a.client(r.Context()).Call(r.Context(), "GetConfig", nil)
			check(configErr)
			raw, decodeErr := base64.StdEncoding.DecodeString(str(config["FileData_bin"]))
			if decodeErr != nil {
				fail(502, "SoftEther returned an invalid configuration buffer.")
			}
			_, _, disabled, parseErr := ddnsConfigValue(string(raw))
			if parseErr == nil && disabled {
				return M{"enabled": false}
			}
		}
		check(err)
		return nil
	})
	a.endpoint("PUT /server/ddns", func(w http.ResponseWriter, r *http.Request, _ M) any {
		body := readBody(w, r)
		_, ok := body["enabled"].(bool)
		if !ok || body["confirm_restart"] != true {
			fail(422, "enabled must be a boolean and confirm_restart must be true.")
		}
		// SoftEther has no dedicated toggle. A GetConfig/SetConfig round trip
		// can overwrite unrelated changes from other management clients.
		fail(409, "Change DDNS during VPN server maintenance; the panel cannot safely rewrite the entire configuration.")
		return nil
	})
}
