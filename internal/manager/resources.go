package manager

import (
	"context"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type resourceState struct {
	mu       sync.RWMutex
	snapshot M
	history  []M
	cpu      map[string][]int64
	network  map[string][4]int64
	at       time.Time
}

func proc(path string) string { b, err := os.ReadFile(path); check(err); return string(b) }
func percent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(max(int64(0), part)) / float64(total) * 100
}
func readCPU() map[string][]int64 {
	out := map[string][]int64{}
	for _, line := range strings.Split(proc("/proc/stat"), "\n") {
		f := strings.Fields(line)
		if len(f) < 9 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		v := make([]int64, 8)
		for i := range v {
			v[i] = integer(f[i+1])
		}
		out[f[0]] = v
	}
	return out
}
func readNetwork() map[string][4]int64 {
	out := map[string][4]int64{}
	for _, line := range strings.Split(proc("/proc/net/dev"), "\n") {
		name, data, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(data)
		if name == "lo" || len(f) < 16 {
			continue
		}
		out[name] = [4]int64{integer(f[0]), integer(f[1]), integer(f[8]), integer(f[9])}
	}
	return out
}
func readMemory() (M, M) {
	m := M{}
	for _, line := range strings.Split(proc("/proc/meminfo"), "\n") {
		key, data, ok := strings.Cut(line, ":")
		f := strings.Fields(data)
		if ok && len(f) > 0 {
			m[key] = integer(f[0]) * 1024
		}
	}
	total, available := integer(m["MemTotal"]), integer(m["MemAvailable"])
	if available == 0 {
		available = integer(m["MemFree"]) + integer(m["Buffers"]) + integer(m["Cached"])
	}
	used := max(int64(0), total-available)
	swap, swapFree := integer(m["SwapTotal"]), integer(m["SwapFree"])
	return M{"total_bytes": total, "available_bytes": available, "used_bytes": used, "free_bytes": m["MemFree"], "buffers_bytes": m["Buffers"], "cached_bytes": m["Cached"], "used_percent": percent(used, total)}, M{"configured": swap > 0, "total_bytes": swap, "used_bytes": max(int64(0), swap-swapFree), "used_percent": percent(swap-swapFree, swap)}
}
func readDisks() []M {
	out := []M{}
	seen := map[string]bool{}
	for _, line := range strings.Split(proc("/proc/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || seen[f[0]] {
			continue
		}
		if !strings.HasPrefix(f[0], "/dev/") && !strings.HasPrefix(f[0], "//") && f[2] != "nfs" && f[2] != "nfs4" && f[2] != "zfs" {
			continue
		}
		mount := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\134", "\\").Replace(f[1])
		total, available, used, inodesPercent, ok := diskStatfs(mount)
		if !ok {
			continue
		}
		seen[f[0]] = true
		out = append(out, M{"device": f[0], "mount_point": mount, "fs_type": f[2], "total_bytes": total, "used_bytes": used, "available_bytes": available, "used_percent": percent(used, used+available), "inodes_used_percent": inodesPercent})
	}
	sort.Slice(out, func(i, j int) bool { return str(out[i]["mount_point"]) < str(out[j]["mount_point"]) })
	return out
}
func (a *App) sampleResources(_ context.Context) {
	if runtime.GOOS != "linux" {
		return
	}
	cpu, network := readCPU(), readNetwork()
	at := time.Now()
	state := &a.resources
	state.mu.Lock()
	defer state.mu.Unlock()
	interval := at.Sub(state.at).Seconds()
	if state.at.IsZero() {
		state.cpu, state.network, state.at = cpu, network, at
		return
	}
	pastCPU, pastNetwork := state.cpu, state.network
	state.cpu, state.network, state.at = cpu, network, at
	if !boolean(a.setting("resource_monitor_enabled")) {
		return
	}
	cores := []M{}
	var overall M
	for name, times := range cpu {
		before := pastCPU[name]
		if before == nil {
			continue
		}
		var d [8]int64
		var total int64
		for i := range d {
			d[i] = max(int64(0), times[i]-before[i])
			total += d[i]
		}
		entry := M{"name": name, "usage_percent": percent(total-d[3]-d[4], total), "user_percent": percent(d[0]+d[1], total), "system_percent": percent(d[2]+d[5]+d[6], total), "iowait_percent": percent(d[4], total), "steal_percent": percent(d[7], total)}
		if name == "cpu" {
			overall = entry
		} else {
			cores = append(cores, entry)
		}
	}
	sort.Slice(cores, func(i, j int) bool { return str(cores[i]["name"]) < str(cores[j]["name"]) })
	mem, swap := readMemory()
	load := M{}
	fields := strings.Fields(proc("/proc/loadavg"))
	if len(fields) >= 4 {
		load["one"], load["five"], load["fifteen"] = number(fields[0]), number(fields[1]), number(fields[2])
		run, total, _ := strings.Cut(fields[3], "/")
		load["running"], load["total"] = integer(run), integer(total)
	}
	interfaces := []M{}
	rxRate, txRate := 0., 0.
	for name, counters := range network {
		rx, tx := 0., 0.
		if past, ok := pastNetwork[name]; ok {
			rx = float64(max(int64(0), counters[0]-past[0])) / interval
			tx = float64(max(int64(0), counters[2]-past[2])) / interval
		}
		rxRate += rx
		txRate += tx
		interfaces = append(interfaces, M{"name": name, "rx_bytes": counters[0], "rx_packets": counters[1], "tx_bytes": counters[2], "tx_packets": counters[3], "rx_bytes_per_second": rx, "tx_bytes_per_second": tx})
	}
	sort.Slice(interfaces, func(i, j int) bool { return str(interfaces[i]["name"]) < str(interfaces[j]["name"]) })
	state.snapshot = M{"available": true, "at": float64(at.UnixMilli()) / 1000, "interval_seconds": interval, "cpu": M{"overall": overall, "cores": cores, "count": max(1, len(cores))}, "load": load, "memory": mem, "swap": swap, "disks": readDisks(), "network": M{"interfaces": interfaces, "rx_bytes_per_second": rxRate, "tx_bytes_per_second": txRate}, "errors": []string{}}
	state.history = append(state.history, M{"t": float64(at.UnixMilli()) / 1000, "cpu": overall["usage_percent"], "memory": mem["used_percent"], "rx": rxRate, "tx": txRate})
	keep := int(max(int64(10), min(int64(2000), integer(a.setting("resource_history_points")))))
	if len(state.history) > keep {
		state.history = state.history[len(state.history)-keep:]
	}
}
func (a *App) resourceSnapshot() M {
	a.resources.mu.RLock()
	defer a.resources.mu.RUnlock()
	out := clone(a.resources.snapshot)
	history := append([]M{}, a.resources.history...)
	out["history"] = history
	if runtime.GOOS != "linux" {
		out["available"], out["reason"] = false, "This host has no /proc; resource monitoring runs on Linux."
	} else if !boolean(a.setting("resource_monitor_enabled")) {
		out["available"], out["reason"] = false, "Resource monitoring is switched off in Settings."
	} else if out["available"] == nil {
		out["available"], out["reason"] = false, "Waiting for the first resource sample."
	}
	return out
}
