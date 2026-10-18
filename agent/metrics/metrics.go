// Package metrics samples host metrics with gopsutil.
//
// When the agent runs in a container, the host's /proc and /sys are mounted
// read-only and exposed via HOST_PROC / HOST_SYS (read by gopsutil), and the
// host root filesystem via NEXOS_HOST_ROOT for disk usage.
package metrics

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var (
	realFilesystems = map[string]bool{
		"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
		"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "f2fs": true, "bcachefs": true,
		"NTFS": true, "FAT32": true, "ReFS": true,
	}
	virtualIfacePattern = regexp.MustCompile(`^(lo|veth|docker|br-|virbr|cni|flannel|cali|vxlan|tailscale|wg|tun|tap|Loopback)`)
	partitionPattern    = regexp.MustCompile(`^((sd|vd|hd|xvd)[a-z]+\d+|(nvme\d+n\d+|mmcblk\d+)p\d+|loop\d+|ram\d+|zram\d+|sr\d+|dm-\d+|md\d+p\d+)$`)
)

// HostInfo gathers static host information.
func HostInfo(ctx context.Context) *nexosv1.HostInfo {
	info := &nexosv1.HostInfo{Arch: runtime.GOARCH}
	if h, err := host.InfoWithContext(ctx); err == nil {
		info.Hostname = h.Hostname
		info.Os = h.OS
		info.Platform = h.Platform
		info.PlatformVersion = h.PlatformVersion
		info.KernelVersion = h.KernelVersion
		info.UptimeSeconds = h.Uptime
		info.BootTimeUnix = h.BootTime
		if h.KernelArch != "" {
			info.Arch = h.KernelArch
		}
	}
	if name := os.Getenv("NEXOS_HOSTNAME"); name != "" {
		info.Hostname = name
	}
	if c, err := cpu.InfoWithContext(ctx); err == nil && len(c) > 0 {
		info.CpuModel = strings.TrimSpace(c[0].ModelName)
	}
	if n, err := cpu.CountsWithContext(ctx, false); err == nil {
		info.CpuCoresPhysical = uint32(n)
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		info.CpuCoresLogical = uint32(n)
	}
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		info.MemoryTotalBytes = v.Total
	}
	return info
}

// Sampler produces HostMetrics; rates are computed against its previous
// sample, so each consumer should own its Sampler.
type Sampler struct {
	hostRoot string

	prevAt    time.Time
	prevCPU   []cpu.TimesStat
	prevTotal *cpu.TimesStat
	prevNet   map[string]psnet.IOCountersStat
	prevDisk  map[string]disk.IOCountersStat
}

func NewSampler() *Sampler {
	return &Sampler{hostRoot: os.Getenv("NEXOS_HOST_ROOT")}
}

func busyPercent(prev, cur cpu.TimesStat) float64 {
	idle := func(t cpu.TimesStat) float64 { return t.Idle + t.Iowait }
	dt := cur.Total() - prev.Total()
	if dt <= 0 {
		return 0
	}
	busy := dt - (idle(cur) - idle(prev))
	if busy < 0 {
		busy = 0
	}
	return clampPct(busy / dt * 100)
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// Sample collects one HostMetrics snapshot. Errors in individual collectors
// are tolerated so that a single missing source does not hide the rest.
func (s *Sampler) Sample(ctx context.Context) *nexosv1.HostMetrics {
	now := time.Now()
	m := &nexosv1.HostMetrics{TimestampUnixMs: now.UnixMilli()}
	elapsed := now.Sub(s.prevAt).Seconds()
	havePrev := !s.prevAt.IsZero() && elapsed > 0

	if total, err := cpu.TimesWithContext(ctx, false); err == nil && len(total) == 1 {
		if s.prevTotal != nil {
			m.CpuPercent = busyPercent(*s.prevTotal, total[0])
		}
		s.prevTotal = &total[0]
	}
	if per, err := cpu.TimesWithContext(ctx, true); err == nil {
		if len(s.prevCPU) == len(per) {
			m.CpuPerCorePercent = make([]float64, len(per))
			for i := range per {
				m.CpuPerCorePercent[i] = busyPercent(s.prevCPU[i], per[i])
			}
		}
		s.prevCPU = per
	}
	if l, err := load.AvgWithContext(ctx); err == nil {
		m.Load1, m.Load5, m.Load15 = l.Load1, l.Load5, l.Load15
	}
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		m.MemoryTotalBytes, m.MemoryUsedBytes, m.MemoryAvailableBytes = v.Total, v.Total-v.Available, v.Available
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		m.SwapTotalBytes, m.SwapUsedBytes = sw.Total, sw.Used
	}
	if up, err := host.UptimeWithContext(ctx); err == nil {
		m.UptimeSeconds = up
	}

	m.Disks = s.disks(ctx)

	if counters, err := disk.IOCountersWithContext(ctx); err == nil {
		var rd, wr float64
		for name, c := range counters {
			if partitionPattern.MatchString(name) {
				continue
			}
			if p, ok := s.prevDisk[name]; ok && havePrev {
				rd += rate(p.ReadBytes, c.ReadBytes, elapsed)
				wr += rate(p.WriteBytes, c.WriteBytes, elapsed)
			}
		}
		m.DiskReadBytesPerSecond, m.DiskWriteBytesPerSecond = rd, wr
		s.prevDisk = counters
	}

	if nics, err := psnet.IOCountersWithContext(ctx, true); err == nil {
		next := make(map[string]psnet.IOCountersStat, len(nics))
		for _, n := range nics {
			st := &nexosv1.NetworkInterfaceStats{
				Name:         n.Name,
				RxBytesTotal: n.BytesRecv,
				TxBytesTotal: n.BytesSent,
				RxErrors:     n.Errin,
				TxErrors:     n.Errout,
				Virtual:      isVirtualIface(n.Name),
			}
			if p, ok := s.prevNet[n.Name]; ok && havePrev {
				st.RxBytesPerSecond = rate(p.BytesRecv, n.BytesRecv, elapsed)
				st.TxBytesPerSecond = rate(p.BytesSent, n.BytesSent, elapsed)
			}
			if !st.Virtual {
				m.NetworkRxBytesPerSecond += st.RxBytesPerSecond
				m.NetworkTxBytesPerSecond += st.TxBytesPerSecond
			}
			// veth pairs come and go with containers; keep the list readable.
			if !strings.HasPrefix(n.Name, "veth") {
				m.Network = append(m.Network, st)
			}
			next[n.Name] = n
		}
		sort.Slice(m.Network, func(i, j int) bool { return m.Network[i].Name < m.Network[j].Name })
		s.prevNet = next
	}

	// Sensor collection returns partial results together with warnings.
	temps, _ := sensors.TemperaturesWithContext(ctx)
	for _, t := range temps {
		if t.Temperature > 0 && t.Temperature < 150 {
			m.Temperatures = append(m.Temperatures, &nexosv1.Temperature{Sensor: t.SensorKey, Celsius: t.Temperature})
		}
	}

	s.prevAt = now
	return m
}

func (s *Sampler) disks(ctx context.Context) []*nexosv1.DiskUsage {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []*nexosv1.DiskUsage
	for _, p := range parts {
		if !realFilesystems[p.Fstype] || seen[p.Device] {
			continue
		}
		path := p.Mountpoint
		if s.hostRoot != "" {
			path = filepath.Join(s.hostRoot, p.Mountpoint)
		}
		u, err := disk.UsageWithContext(ctx, path)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, &nexosv1.DiskUsage{
			Mountpoint:  p.Mountpoint,
			Device:      p.Device,
			Fstype:      p.Fstype,
			TotalBytes:  u.Total,
			UsedBytes:   u.Used,
			FreeBytes:   u.Free,
			UsedPercent: u.UsedPercent,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out
}

// isVirtualIface reports whether an interface is virtual (bridge, veth,
// tunnel...). On Linux, physical NICs have a "device" link in sysfs; the name
// pattern is only a fallback where sysfs is unavailable.
func isVirtualIface(name string) bool {
	sys := os.Getenv("HOST_SYS")
	if sys == "" {
		sys = "/sys"
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat(filepath.Join(sys, "class", "net", name)); err == nil {
			_, err := os.Stat(filepath.Join(sys, "class", "net", name, "device"))
			return err != nil
		}
	}
	return virtualIfacePattern.MatchString(name)
}

func rate(prev, cur uint64, seconds float64) float64 {
	if cur < prev { // counter reset (interface re-created, reboot)
		return 0
	}
	return float64(cur-prev) / seconds
}
