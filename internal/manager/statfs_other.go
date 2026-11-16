//go:build !linux

package manager

func diskStatfs(path string) (total, available, used int64, inodesPercent float64, ok bool) {
	return 0, 0, 0, 0, false
}
