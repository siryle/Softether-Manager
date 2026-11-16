//go:build linux

package manager

import "golang.org/x/sys/unix"

func diskStatfs(path string) (total, available, used int64, inodesPercent float64, ok bool) {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return 0, 0, 0, 0, false
	}
	tot := int64(stat.Blocks) * int64(stat.Bsize)
	if tot == 0 {
		return 0, 0, 0, 0, false
	}
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	u := tot - int64(stat.Bfree)*int64(stat.Bsize)
	inPct := percent(int64(stat.Files-stat.Ffree), int64(stat.Files))
	return tot, avail, u, inPct, true
}
