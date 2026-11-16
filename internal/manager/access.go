package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func accessVersion(rows any) string {
	data, err := json.Marshal(rows)
	check(err)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (a *App) checkAccessVersion(ctx context.Context, hub, key, expected string) {
	if expected == "" {
		fail(428, "Reload the access list before saving changes.")
	}
	method := "EnumAccess"
	if key == "ACList" {
		method = "GetAcList"
	}
	current := a.rpc(ctx, method, M{"HubName_str": hub})
	if expected != accessVersion(current[key]) {
		fail(409, "The access list changed. Reload it before saving changes.")
	}
}
