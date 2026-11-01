package sysmetrics

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// The host-resource projection.
//
// Disk is the reading this exists for: the owner asked how close to full the
// box is. Memory and CPU are along for the ride because they are three more
// lines on the same screen and the alternative is opening Grafana on a phone.
//
// Every section is independently absent-able. One missing metric must not
// blank the whole reply — a partial answer is useful and a suppressed one is
// indistinguishable from "fine".
// ---------------------------------------------------------------------------

const (
	// WarnUsedPercent is the disk-pressure threshold. At or above it a
	// filesystem is flagged: 85% is where a filesystem stops being something
	// to look at next week.
	WarnUsedPercent = 85.0

	// CPUWindow is the rate window used for CPU utilisation, as it appears in
	// the PromQL below. Rendered next to the number so the reading is not
	// mistaken for an instantaneous one.
	CPUWindow = "5m"
)

// pseudoFSTypes are filesystem types that do not describe real capacity.
// Including them makes the number meaningless: an overlay reports the
// underlying disk twice, and a tmpfs at 100% is usually nothing at all.
//
// This is the source of truth for both the PromQL matcher and the Go-side
// filter, so the two cannot drift apart. It is a slice, not a map literal, so
// the generated regex is deterministic.
var pseudoFSTypes = []string{
	"autofs",
	"binfmt_misc",
	"bpf",
	"cgroup",
	"cgroup2",
	"configfs",
	"debugfs",
	"devpts",
	"devtmpfs",
	"efivarfs",
	"fuse.lxcfs",
	"fusectl",
	"hugetlbfs",
	"iso9660",
	"mqueue",
	"nsfs",
	"overlay",
	"overlayfs",
	"proc",
	"procfs",
	"pstore",
	"ramfs",
	"rpc_pipefs",
	"securityfs",
	"selinuxfs",
	"shm",
	"squashfs",
	"sysfs",
	"tmpfs",
	"tracefs",
}

// ignoredMountPrefixes are mount trees that are either pseudo-filesystems by
// another name or a second view of a filesystem already counted. The container
// storage trees matter most here: /var/lib/docker/... re-reports the host disk
// under a dozen mountpoints and would otherwise dominate the table.
var ignoredMountPrefixes = []string{
	"/dev",
	"/proc",
	"/sys",
	"/run",
	"/snap",
	"/boot/efi",
	"/var/lib/docker",
	"/var/lib/kubelet",
	"/var/lib/containers",
}

var pseudoFSTypeSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(pseudoFSTypes))
	for _, t := range pseudoFSTypes {
		m[t] = struct{}{}
	}
	return m
}()

// RealFilesystem reports whether a mount describes capacity worth reporting.
// It is applied twice: once as a PromQL matcher so Prometheus does the work,
// and once here on whatever actually came back. The second pass is what makes
// the filter true regardless of what the upstream chose to return.
func RealFilesystem(fstype, mountpoint string) bool {
	if strings.TrimSpace(mountpoint) == "" {
		return false
	}
	if _, pseudo := pseudoFSTypeSet[strings.ToLower(strings.TrimSpace(fstype))]; pseudo {
		return false
	}
	for _, prefix := range ignoredMountPrefixes {
		if mountpoint == prefix || strings.HasPrefix(mountpoint, prefix+"/") {
			return false
		}
	}
	return true
}

// alwaysMeaninglessFSTypes are the types that describe no durable capacity
// under any deployment. They stay excluded even in the fallback pass, where
// overlay and the container storage trees are deliberately allowed back in:
// a tmpfs at 100% is still not a full disk.
var alwaysMeaninglessFSTypes = map[string]struct{}{
	"autofs": {}, "binfmt_misc": {}, "bpf": {}, "cgroup": {}, "cgroup2": {},
	"configfs": {}, "debugfs": {}, "devpts": {}, "devtmpfs": {}, "efivarfs": {},
	"fusectl": {}, "hugetlbfs": {}, "mqueue": {}, "nsfs": {}, "proc": {},
	"procfs": {}, "pstore": {}, "ramfs": {}, "rpc_pipefs": {}, "securityfs": {},
	"selinuxfs": {}, "shm": {}, "sysfs": {}, "tmpfs": {}, "tracefs": {},
}

// CapacityBearingFilesystem is the relaxed predicate used only when the strict
// one leaves nothing. It keeps overlay, fuse.lxcfs and the container storage
// trees, which on a containerised VPS are not a second view of the disk --
// they are the only view of it.
func CapacityBearingFilesystem(fstype, mountpoint string) bool {
	if strings.TrimSpace(mountpoint) == "" {
		return false
	}
	_, meaningless := alwaysMeaninglessFSTypes[strings.ToLower(strings.TrimSpace(fstype))]
	return !meaningless
}

// fsSelector is the label matcher shared by both disk queries, generated from
// the lists above. PromQL anchors regex matchers at both ends, so each
// alternative must match the whole label value; the mountpoint arm carries an
// explicit "(|/.*)" suffix to cover the subtree.
//
// Each alternative is quoted, so a type such as "fuse.lxcfs" matches a literal
// dot rather than any character. Without it the PromQL filter would be
// marginally wider than the Go one, and the two must agree exactly.
var fsSelector = "{fstype!~\"" + alternation(pseudoFSTypes) + "\"," +
	"mountpoint!~\"(" + alternation(ignoredMountPrefixes) + ")(|/.*)\"}"

func alternation(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = regexp.QuoteMeta(v)
	}
	return strings.Join(quoted, "|")
}

// The five queries. They are constants, never built from operator input.
//
// The "max by (device, mountpoint, fstype)" wrapper collapses duplicate scrape
// targets reporting the same mount to one series. In this deployment there is
// a single node-exporter, so it is an identity; it exists so a second exporter
// cannot silently double every row.
var (
	// QueryDiskAvail is free bytes per real filesystem.
	QueryDiskAvail = "max by (device, mountpoint, fstype) (node_filesystem_avail_bytes" + fsSelector + ")"
	// QueryDiskSize is total bytes per real filesystem. Used percentage is
	// derived from the pair in Go rather than in PromQL so the reply can show
	// free bytes and the percentage from one consistent reading.
	QueryDiskSize = "max by (device, mountpoint, fstype) (node_filesystem_size_bytes" + fsSelector + ")"

	// QueryDiskSizeAll and QueryDiskAvailAll are the same readings with no
	// selector at all. They exist for the fallback pass described on Disk:
	// on a containerised host the strict selector can legitimately match
	// nothing, and an empty disk section is the one answer this command must
	// never give.
	QueryDiskSizeAll  = "max by (device, mountpoint, fstype) (node_filesystem_size_bytes)"
	QueryDiskAvailAll = "max by (device, mountpoint, fstype) (node_filesystem_avail_bytes)"

	// QueryMemAvailable is MemAvailable, the kernel's own estimate of what a
	// new allocation could actually get. MemFree would report reclaimable
	// page cache as used and make a healthy box look full.
	QueryMemAvailable = "sum(node_memory_MemAvailable_bytes)"
	// QueryMemTotal is installed memory.
	QueryMemTotal = "sum(node_memory_MemTotal_bytes)"

	// QueryCPUUsed is utilisation as the complement of idle time, averaged
	// over every core. rate() over a 5m window smooths the sampling noise
	// that makes an instantaneous CPU reading useless.
	QueryCPUUsed = "100 - (avg(rate(node_cpu_seconds_total{mode=\"idle\"}[" + CPUWindow + "])) * 100)"
)

// Filesystem is one real mount.
type Filesystem struct {
	Device      string
	Mountpoint  string
	FSType      string
	SizeBytes   float64
	AvailBytes  float64
	UsedBytes   float64
	UsedPercent float64
}

// Pressured reports whether this filesystem is at or above the warning
// threshold. The renderer flags these; a 91% disk must be obvious at a glance.
func (f Filesystem) Pressured() bool { return f.UsedPercent >= WarnUsedPercent }

// Memory is the memory section.
type Memory struct {
	TotalBytes     float64
	AvailableBytes float64
	UsedPercent    float64
}

// CPU is the CPU section.
type CPU struct {
	UsedPercent float64
	// Window is the rate window the figure was derived over.
	Window string
}

// Snapshot is the whole projection. Each section carries its own error, and a
// section with a non-nil error must render "unavailable" rather than being
// dropped: an omitted line reads as "fine" and it is not.
type Snapshot struct {
	// Filesystems is the disk section, sorted most-pressured first. It is
	// meaningful only when DiskErr is nil; an empty slice with a nil DiskErr
	// means Prometheus answered and no real filesystem survived the filter.
	Filesystems []Filesystem
	DiskErr     error

	// DiskFallback records that the strict filesystem filter matched nothing
	// and the relaxed pass supplied these rows instead. The renderer says so:
	// an overlay root is the real number on a containerised host, but the
	// reader still needs to know which view they are looking at.
	DiskFallback bool

	// Memory is nil when MemoryErr is set.
	Memory    *Memory
	MemoryErr error

	// CPU is nil when CPUErr is set.
	CPU    *CPU
	CPUErr error
}

// Pressured returns the filesystems at or above the warning threshold.
func (s Snapshot) Pressured() []Filesystem {
	var out []Filesystem
	for _, fs := range s.Filesystems {
		if fs.Pressured() {
			out = append(out, fs)
		}
	}
	return out
}

// Snapshot reads all three sections. It never returns an error: a failure is
// recorded on the section it belongs to, so one dead metric costs one line
// rather than the whole reply.
//
// A nil client yields a fully-unavailable snapshot rather than a panic — the
// command's dependency is allowed to be unwired.
func (c *Client) Snapshot(ctx context.Context) Snapshot {
	var snap Snapshot
	if c == nil {
		notWired := ErrUnreachable
		return Snapshot{DiskErr: notWired, MemoryErr: notWired, CPUErr: notWired}
	}
	snap.Filesystems, snap.DiskFallback, snap.DiskErr = c.Disk(ctx)
	snap.Memory, snap.MemoryErr = c.MemorySection(ctx)
	snap.CPU, snap.CPUErr = c.CPUSection(ctx)
	return snap
}

// Disk reads the filesystem section, in up to two passes.
//
// The strict pass asks Prometheus for real filesystems only. That is the right
// reading on a normal host, but it can legitimately match nothing: on a
// containerised VPS the only mount carrying the host's capacity is an overlay,
// which the strict selector excludes by design. The first version of this
// command reported that case as "disk unavailable" -- indistinguishable from a
// dead Prometheus, and useless to the person asking how full the box is.
//
// So when the strict pass yields no rows -- whether Prometheus returned an
// empty vector or the Go-side filter emptied it -- a second pass reads the
// same metrics unfiltered and keeps anything that bears capacity at all,
// collapsing the repeated views of one device that this admits. The caller is
// told which pass answered.
//
// The two queries of a pass are joined in Go on (device, mountpoint, fstype);
// a mount present in only one of them is dropped rather than reported against
// a mismatched denominator.
func (c *Client) Disk(ctx context.Context) ([]Filesystem, bool, error) {
	strict, err := c.diskPass(ctx, QueryDiskSize, QueryDiskAvail, RealFilesystem, false)
	if err != nil && !errors.Is(err, ErrNoData) {
		return nil, false, err
	}
	if len(strict) > 0 {
		return strict, false, nil
	}

	relaxed, fallbackErr := c.diskPass(ctx, QueryDiskSizeAll, QueryDiskAvailAll, CapacityBearingFilesystem, true)
	if fallbackErr != nil {
		// Report the strict failure when there was one: it describes the
		// reading that was actually wanted. Otherwise the fallback's own.
		if err != nil {
			return nil, false, err
		}
		return nil, false, fallbackErr
	}
	if len(relaxed) == 0 {
		// Prometheus answered and nothing on this host reports capacity. That
		// is "no data", not "unavailable", and the renderer says so.
		if err != nil {
			return nil, false, err
		}
		return nil, false, ErrNoData
	}
	return relaxed, true, nil
}

// diskPass runs one size/avail pair and joins it. collapseDuplicates folds
// mounts reporting an identical size and free figure into one row, which is
// what a dozen Docker overlay mounts of the same underlying disk look like.
func (c *Client) diskPass(ctx context.Context, sizeQuery, availQuery string, keep func(string, string) bool, collapseDuplicates bool) ([]Filesystem, error) {
	sizes, err := c.Query(ctx, sizeQuery)
	if err != nil {
		return nil, err
	}
	avails, err := c.Query(ctx, availQuery)
	if err != nil {
		return nil, err
	}

	type key struct{ device, mountpoint, fstype string }
	sizeBy := make(map[key]float64, len(sizes))
	for _, s := range sizes {
		if !keep(s.Label("fstype"), s.Label("mountpoint")) {
			continue
		}
		// A zero-sized filesystem has no percentage. Reporting 0% or 100% for
		// it would both be inventions.
		if s.Value <= 0 {
			continue
		}
		sizeBy[key{s.Label("device"), s.Label("mountpoint"), s.Label("fstype")}] = s.Value
	}

	out := make([]Filesystem, 0, len(sizeBy))
	seen := make(map[key]struct{}, len(sizeBy))
	type shape struct{ size, avail float64 }
	shapes := make(map[shape]struct{}, len(sizeBy))
	for _, a := range avails {
		fstype, mount := a.Label("fstype"), a.Label("mountpoint")
		if !keep(fstype, mount) {
			continue
		}
		k := key{a.Label("device"), mount, fstype}
		size, ok := sizeBy[k]
		if !ok {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}

		avail := a.Value
		if avail < 0 {
			avail = 0
		}
		if avail > size {
			avail = size
		}
		if collapseDuplicates {
			if _, repeat := shapes[shape{size, avail}]; repeat {
				continue
			}
			shapes[shape{size, avail}] = struct{}{}
		}
		used := size - avail
		out = append(out, Filesystem{
			Device:      k.device,
			Mountpoint:  mount,
			FSType:      fstype,
			SizeBytes:   size,
			AvailBytes:  avail,
			UsedBytes:   used,
			UsedPercent: used / size * 100,
		})
	}

	// Fullest first: the reason to run this command is to find the one that
	// is about to fill, and it must not be somewhere down the list.
	sort.Slice(out, func(i, j int) bool {
		if out[i].UsedPercent != out[j].UsedPercent {
			return out[i].UsedPercent > out[j].UsedPercent
		}
		return out[i].Mountpoint < out[j].Mountpoint
	})
	return out, nil
}

// MemorySection reads memory. Available and total are read separately and the
// percentage derived here, so the reply can show both without a third query.
func (c *Client) MemorySection(ctx context.Context) (*Memory, error) {
	total, err := c.QueryScalar(ctx, QueryMemTotal)
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return nil, ErrNoData
	}
	avail, err := c.QueryScalar(ctx, QueryMemAvailable)
	if err != nil {
		return nil, err
	}
	if avail < 0 {
		avail = 0
	}
	if avail > total {
		avail = total
	}
	return &Memory{
		TotalBytes:     total,
		AvailableBytes: avail,
		UsedPercent:    (total - avail) / total * 100,
	}, nil
}

// CPUSection reads CPU utilisation.
func (c *Client) CPUSection(ctx context.Context) (*CPU, error) {
	used, err := c.QueryScalar(ctx, QueryCPUUsed)
	if err != nil {
		return nil, err
	}
	// rate() over a window that straddles a counter reset can land marginally
	// outside [0,100]. Clamping is honest here: the reading is a percentage
	// and a negative one is a rounding artefact, not information.
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	return &CPU{UsedPercent: used, Window: CPUWindow}, nil
}
