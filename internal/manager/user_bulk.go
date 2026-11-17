package manager

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// userCSVHeader is what export writes and what import reads.
//
// SoftEther keeps only a one-way hash of a password, so plaintext can never be
// exported. password_hash carries that hash (base64, SoftEther's HashedKey_bin)
// instead: importing it recreates the account with the original password still
// working. It is a login-capable secret -- treat the file like a key.
var userCSVHeader = []string{"name", "realname", "note", "group", "auth_type", "password", "password_hash", "expires"}

// The names the panel uses for auth types, so a hand-written CSV can say
// "password" instead of remembering that 1 means that.
var userAuthTypes = map[string]int{
	"anonymous": 0, "password": 1, "usercertificate": 2, "certificate": 2,
	"signedcertificate": 3, "signed": 3, "radius": 4, "nt": 5, "ntdomain": 5,
}

var userExpiryLayouts = []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"}

// recoverAsError turns one row's RPC failure into that row's error, so a bad
// line cannot abort the whole import.
func recoverAsError(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errors.New(fmtError(p))
		}
	}()
	return fn()
}

func (a *App) registerUserBulk() {
	a.endpoint("GET /hubs/{hub}/users/export", func(_ http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		serverID := a.serverIDFromContext(r.Context())
		out := a.rpc(r.Context(), "EnumUser", M{"HubName_str": hub})
		list, _ := out["UserList"].([]any)

		var buf strings.Builder
		w := csv.NewWriter(&buf)
		check(w.Write(userCSVHeader))
		for _, item := range list {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			expires := ""
			if boolean(row["IsExpiresFilled_bool"]) {
				expires = str(row["Expires_dt"])
			}
			hash := ""
			if integer(row["AuthType_u32"]) == 1 {
				// The vault first; otherwise ask the server once and remember.
				hash = a.storedCredential(serverID, hub, str(row["Name_str"]))
				if hash == "" {
					_ = recoverAsError(func() error {
						hash = a.obtainCredential(r.Context(), serverID, hub, str(row["Name_str"]))
						return nil
					})
				}
			}
			check(w.Write([]string{
				str(row["Name_str"]),
				str(row["Realname_utf"]),
				str(row["Note_utf"]),
				str(row["GroupName_str"]),
				str(integer(row["AuthType_u32"])),
				"",
				hash,
				expires,
			}))
		}
		w.Flush()
		check(w.Error())

		a.audit(u, "user.exported", "hub", hub, fmt.Sprintf("%d users", len(list)))
		return M{"filename": hub + "-users.csv", "csv": buf.String(), "count": len(list)}
	})

	a.endpoint("POST /hubs/{hub}/users/import", func(w http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		body := readBody(w, r)
		text := str(body["csv"])
		if strings.TrimSpace(text) == "" {
			fail(422, "The file is empty.")
		}
		rows, err := csv.NewReader(strings.NewReader(text)).ReadAll()
		if err != nil {
			fail(422, "Cannot read the CSV: "+err.Error())
		}
		if len(rows) == 0 {
			fail(422, "The file is empty.")
		}

		// Header names decide the column order, so a hand-edited file still works.
		columns := map[string]int{}
		for i, cell := range rows[0] {
			columns[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(cell, "\ufeff")))] = i
		}
		if _, ok := columns["name"]; !ok {
			fail(422, "The first line must be a header that names at least a name column.")
		}
		pick := func(row []string, key string) string {
			if i, ok := columns[key]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}

		// One read of the hub tells us which names are already taken.
		existing := map[string]bool{}
		for _, item := range listOf(a.rpc(r.Context(), "EnumUser", M{"HubName_str": hub})) {
			existing[strings.ToLower(str(item["Name_str"]))] = true
		}

		serverID := a.serverIDFromContext(r.Context())
		created := []string{}
		skipped := []M{}
		failed := []M{}

		for index, row := range rows[1:] {
			line := index + 2 // 1-based, and the header is line 1
			name := pick(row, "name")
			if name == "" {
				continue // blank line
			}
			if len([]rune(name)) > 64 || strings.ContainsAny(name, "\r\n\t") {
				failed = append(failed, M{"line": line, "name": name, "error": "invalid user name"})
				continue
			}
			lower := strings.ToLower(name)
			if existing[lower] {
				skipped = append(skipped, M{"name": name, "reason": "already exists"})
				continue
			}

			authType := 1
			if raw := pick(row, "auth_type"); raw != "" {
				n, ok := userAuthTypes[strings.ToLower(strings.ReplaceAll(raw, " ", ""))]
				if !ok {
					n64, err := strconv.Atoi(raw)
					if err != nil || n64 < 0 || n64 > 5 {
						failed = append(failed, M{"line": line, "name": name, "error": "unknown auth_type " + raw})
						continue
					}
					n = n64
				}
				authType = n
			}

			password, passwordHash := pick(row, "password"), pick(row, "password_hash")
			if authType == 1 && password == "" {
				if passwordHash == "" {
					failed = append(failed, M{"line": line, "name": name, "error": "a password-authenticated user needs password or password_hash"})
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(passwordHash)
				if err != nil || len(raw) != 20 {
					failed = append(failed, M{"line": line, "name": name, "error": "password_hash is not a SoftEther password hash"})
					continue
				}
			}

			expires := "1970-01-01T00:00:00.000"
			if raw := pick(row, "expires"); raw != "" {
				when, err := parseUserExpiry(raw)
				if err != nil {
					failed = append(failed, M{"line": line, "name": name, "error": "unreadable expires: " + raw})
					continue
				}
				expires = when.UTC().Format("2006-01-02T15:04:05.000Z")
			}

			realname, note, group, commonName := pick(row, "realname"), pick(row, "note"), pick(row, "group"), pick(row, "common_name")
			radiusUser, ntUser := pick(row, "radius_username"), pick(row, "nt_username")

			err := recoverAsError(func() error {
				payload := M{
					"HubName_str":        hub,
					"Name_str":           name,
					"Realname_utf":       realname,
					"Note_utf":           note,
					"AuthType_u32":       authType,
					"ExpireTime_dt":      expires,
					"CommonName_utf":     commonName,
					"RadiusUsername_utf": radiusUser,
					"NtUsername_utf":     ntUser,
				}
				if authType == 1 {
					if password != "" {
						payload["Auth_Password_str"] = password
					} else {
						// The original password keeps working: SoftEther stores
						// this same hash for a plaintext one.
						payload["HashedKey_bin"] = passwordHash
					}
				}
				validateWire(payload)
				a.rpc(r.Context(), "CreateUser", payload)
				if authType == 1 {
					if password != "" {
						a.saveCredential(serverID, hub, name, hashedPassword(password, name))
					} else {
						a.saveCredential(serverID, hub, name, passwordHash)
					}
				}
				if group != "" {
					return a.assignUserGroup(r.Context(), hub, name, group)
				}
				return nil
			})
			if err != nil {
				failed = append(failed, M{"line": line, "name": name, "error": err.Error()})
				continue
			}
			existing[lower] = true
			created = append(created, name)
		}

		a.audit(u, "user.imported", "hub", hub, fmt.Sprintf("%d created, %d skipped, %d failed", len(created), len(skipped), len(failed)))
		return M{"created": created, "skipped": skipped, "failed": failed}
	})
}

// assignUserGroup merges a group into the record the server just created:
// CreateUser cannot set one, and SetUser replaces the whole user.
func (a *App) assignUserGroup(ctx context.Context, hub, name, group string) error {
	fresh, err := a.client(ctx).Call(ctx, "GetUser", M{"HubName_str": hub, "Name_str": name})
	if err != nil {
		return err
	}
	merged := M{}
	for key, value := range fresh {
		if strings.HasPrefix(key, "Recv.") || strings.HasPrefix(key, "Send.") || key == "Auth_Password_str" {
			continue
		}
		merged[key] = value
	}
	merged["HubName_str"], merged["Name_str"] = hub, name
	merged["GroupName_str"] = group
	validateWire(merged)
	_, err = a.client(ctx).Call(ctx, "SetUser", merged)
	return err
}

func parseUserExpiry(raw string) (time.Time, error) {
	for _, layout := range userExpiryLayouts {
		if when, err := time.Parse(layout, raw); err == nil {
			return when, nil
		}
	}
	return time.Time{}, errors.New("unreadable date")
}

func listOf(out M) []M {
	items, _ := out["UserList"].([]any)
	list := make([]M, 0, len(items))
	for _, item := range items {
		if row, ok := item.(map[string]any); ok {
			list = append(list, row)
		}
	}
	return list
}
