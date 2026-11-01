package sysmetrics

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// promStub serves canned envelopes keyed by the exact PromQL expression the
// client sends. A query with no canned answer fails the test loudly: a typo in
// a constant must not silently become an "unavailable" section.
type promStub struct {
	t        *testing.T
	bodies   map[string]string
	statuses map[string]int
	seen     []string
}

func newPromStub(t *testing.T) *promStub {
	return &promStub{t: t, bodies: map[string]string{}, statuses: map[string]int{}}
}

func (s *promStub) on(query, body string) *promStub {
	s.bodies[query] = body
	return s
}

func (s *promStub) status(query string, code int) *promStub {
	s.statuses[query] = code
	return s
}

func (s *promStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		s.seen = append(s.seen, q)
		if code, ok := s.statuses[q]; ok {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"nope"}`))
			return
		}
		body, ok := s.bodies[q]
		if !ok {
			s.t.Errorf("unexpected query: %q", q)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

// healthyStub is the baseline fixture: one real ext4 root plus a set of
// pseudo-filesystems that node-exporter genuinely reports.
func healthyStub(t *testing.T) *promStub {
	return newPromStub(t).
		on(QueryDiskSize, vectorBody(
			fsSeries("/dev/sda1", "/", "ext4", "107374182400"), // 100 GiB
			fsSeries("/dev/sdb1", "/data", "xfs", "536870912000"),
			fsSeries("tmpfs", "/run/lock", "tmpfs", "5242880"),
			fsSeries("overlay", "/var/lib/docker/overlay2/abc/merged", "overlay", "107374182400"),
			fsSeries("udev", "/dev", "devtmpfs", "8388608000"),
			fsSeries("/dev/loop0", "/snap/core/1", "squashfs", "134217728"),
		)).
		on(QueryDiskAvail, vectorBody(
			fsSeries("/dev/sda1", "/", "ext4", "42949672960"), // 40 GiB free ⇒ 60% used
			fsSeries("/dev/sdb1", "/data", "xfs", "483183820800"),
			fsSeries("tmpfs", "/run/lock", "tmpfs", "5242880"),
			fsSeries("overlay", "/var/lib/docker/overlay2/abc/merged", "overlay", "42949672960"),
			fsSeries("udev", "/dev", "devtmpfs", "8388608000"),
			fsSeries("/dev/loop0", "/snap/core/1", "squashfs", "0"),
		)).
		on(QueryMemTotal, vectorBody(scalarSeries("16777216000"))).
		on(QueryMemAvailable, vectorBody(scalarSeries("6710886400"))).
		on(QueryCPUUsed, vectorBody(scalarSeries("23.75")))
}

// TestSnapshotNormalReading is the happy path across all three sections.
func TestSnapshotNormalReading(t *testing.T) {
	c, _ := newTestClient(t, healthyStub(t).handler())

	snap := c.Snapshot(context.Background())
	if snap.DiskErr != nil || snap.MemoryErr != nil || snap.CPUErr != nil {
		t.Fatalf("errors on a healthy snapshot: %v / %v / %v", snap.DiskErr, snap.MemoryErr, snap.CPUErr)
	}

	if len(snap.Filesystems) != 2 {
		t.Fatalf("got %d filesystems, want 2 real ones: %+v", len(snap.Filesystems), snap.Filesystems)
	}
	// Fullest first: / at 60% must outrank /data at ~10%.
	root := snap.Filesystems[0]
	if root.Mountpoint != "/" {
		t.Fatalf("first filesystem is %q, want the fullest (/)", root.Mountpoint)
	}
	if root.UsedPercent < 59.9 || root.UsedPercent > 60.1 {
		t.Fatalf("root used = %.2f%%, want ~60%%", root.UsedPercent)
	}
	if root.AvailBytes != 42949672960 {
		t.Fatalf("root free = %.0f, want 42949672960", root.AvailBytes)
	}
	if root.FSType != "ext4" || root.Device != "/dev/sda1" {
		t.Fatalf("root labels lost: %+v", root)
	}

	if snap.Memory == nil {
		t.Fatal("memory section is nil on a healthy snapshot")
	}
	if snap.Memory.UsedPercent < 59.9 || snap.Memory.UsedPercent > 60.1 {
		t.Fatalf("memory used = %.2f%%, want ~60%%", snap.Memory.UsedPercent)
	}
	if snap.Memory.TotalBytes != 16777216000 {
		t.Fatalf("memory total = %.0f, want 16777216000", snap.Memory.TotalBytes)
	}

	if snap.CPU == nil || snap.CPU.UsedPercent != 23.75 {
		t.Fatalf("cpu = %+v, want 23.75%%", snap.CPU)
	}
	if snap.CPU.Window != CPUWindow {
		t.Fatalf("cpu window = %q, want %q", snap.CPU.Window, CPUWindow)
	}
}

// TestPseudoFilesystemsAreExcluded is the assertion that keeps the disk figure
// meaningful. tmpfs, overlay, devtmpfs and squashfs all report capacity that
// is either imaginary or a second view of a disk already counted.
func TestPseudoFilesystemsAreExcluded(t *testing.T) {
	c, _ := newTestClient(t, healthyStub(t).handler())

	snap := c.Snapshot(context.Background())
	if snap.DiskErr != nil {
		t.Fatalf("DiskErr: %v", snap.DiskErr)
	}
	for _, fs := range snap.Filesystems {
		switch fs.FSType {
		case "tmpfs", "overlay", "devtmpfs", "squashfs":
			t.Fatalf("pseudo filesystem %q survived the filter: %+v", fs.FSType, fs)
		}
		if strings.HasPrefix(fs.Mountpoint, "/var/lib/docker") ||
			strings.HasPrefix(fs.Mountpoint, "/dev") ||
			strings.HasPrefix(fs.Mountpoint, "/snap") ||
			strings.HasPrefix(fs.Mountpoint, "/run") {
			t.Fatalf("ignored mountpoint %q survived the filter", fs.Mountpoint)
		}
	}
}

// TestRealFilesystemFilter covers the predicate directly, including the
// boundary cases a prefix check gets wrong: "/dev" is excluded but a mount
// named "/devdata" is a real disk.
func TestRealFilesystemFilter(t *testing.T) {
	for _, tc := range []struct {
		fstype, mount string
		want          bool
	}{
		{"ext4", "/", true},
		{"xfs", "/data", true},
		{"btrfs", "/mnt/backup", true},
		{"ext4", "/devdata", true},
		{"ext4", "/runners", true},
		{"tmpfs", "/dev/shm", false},
		{"tmpfs", "/tmp", false},
		{"overlay", "/", false},
		{"devtmpfs", "/dev", false},
		{"squashfs", "/snap/core/1", false},
		{"ext4", "/var/lib/docker/volumes/x", false},
		{"EXT4", "", false},
		{"TMPFS", "/tmp", false},
	} {
		if got := RealFilesystem(tc.fstype, tc.mount); got != tc.want {
			t.Errorf("RealFilesystem(%q, %q) = %v, want %v", tc.fstype, tc.mount, got, tc.want)
		}
	}
}

// TestPromQLFiltersMatchTheGoFilter: the query matcher and the Go-side check
// are generated from the same lists, and this proves the generation actually
// covers what the predicate covers.
func TestPromQLFiltersMatchTheGoFilter(t *testing.T) {
	for _, fstype := range pseudoFSTypes {
		// The selector carries the regexp-quoted form, so "fuse.lxcfs" cannot
		// widen into "fuseXlxcfs".
		if !strings.Contains(fsSelector, regexp.QuoteMeta(fstype)) {
			t.Errorf("fstype %q is excluded in Go but not in the PromQL selector", fstype)
		}
		if RealFilesystem(fstype, "/") {
			t.Errorf("fstype %q is in the pseudo list but RealFilesystem accepts it", fstype)
		}
	}
	for _, prefix := range ignoredMountPrefixes {
		if !strings.Contains(fsSelector, regexp.QuoteMeta(prefix)) {
			t.Errorf("mountpoint %q is excluded in Go but not in the PromQL selector", prefix)
		}
	}
	// The generated alternation must be a valid regular expression: PromQL
	// rejects the whole query otherwise, and the failure would only surface
	// against a live Prometheus.
	for _, arm := range []string{alternation(pseudoFSTypes), "(" + alternation(ignoredMountPrefixes) + ")(|/.*)"} {
		if _, err := regexp.Compile("^(?:" + arm + ")$"); err != nil {
			t.Fatalf("generated matcher %q does not compile: %v", arm, err)
		}
	}
	if !strings.Contains(QueryDiskAvail, "node_filesystem_avail_bytes") ||
		!strings.Contains(QueryDiskSize, "node_filesystem_size_bytes") ||
		!strings.Contains(QueryCPUUsed, "node_cpu_seconds_total") ||
		!strings.Contains(QueryMemTotal, "node_memory_MemTotal_bytes") ||
		!strings.Contains(QueryMemAvailable, "node_memory_MemAvailable_bytes") {
		t.Fatal("a query no longer names the metric it reads")
	}
}

// TestPressuredFilesystemIsIdentified: 91% must be reported as pressure.
func TestPressuredFilesystemIsIdentified(t *testing.T) {
	stub := healthyStub(t).
		on(QueryDiskSize, vectorBody(
			fsSeries("/dev/sda1", "/", "ext4", "100000000000"),
			fsSeries("/dev/sdb1", "/data", "xfs", "100000000000"),
		)).
		on(QueryDiskAvail, vectorBody(
			fsSeries("/dev/sda1", "/", "ext4", "9000000000"), // 91% used
			fsSeries("/dev/sdb1", "/data", "xfs", "60000000000"),
		))
	c, _ := newTestClient(t, stub.handler())

	snap := c.Snapshot(context.Background())
	if snap.DiskErr != nil {
		t.Fatalf("DiskErr: %v", snap.DiskErr)
	}
	pressured := snap.Pressured()
	if len(pressured) != 1 || pressured[0].Mountpoint != "/" {
		t.Fatalf("Pressured() = %+v, want just /", pressured)
	}
	if !snap.Filesystems[0].Pressured() {
		t.Fatal("the 91%% filesystem is not flagged")
	}
	if snap.Filesystems[1].Pressured() {
		t.Fatal("the 40%% filesystem is flagged")
	}
	// The threshold boundary is inclusive: exactly 85% is pressure.
	if !(Filesystem{UsedPercent: WarnUsedPercent}).Pressured() {
		t.Fatal("the threshold is exclusive; it must be inclusive")
	}
	if (Filesystem{UsedPercent: WarnUsedPercent - 0.1}).Pressured() {
		t.Fatal("a filesystem below the threshold is flagged")
	}
}

// TestSectionsDegradeIndependently: one dead metric costs one section.
func TestSectionsDegradeIndependently(t *testing.T) {
	stub := healthyStub(t).status(QueryCPUUsed, http.StatusBadRequest)
	c, _ := newTestClient(t, stub.handler())

	snap := c.Snapshot(context.Background())
	if snap.DiskErr != nil {
		t.Fatalf("a failed CPU query blanked the disk section: %v", snap.DiskErr)
	}
	if snap.MemoryErr != nil {
		t.Fatalf("a failed CPU query blanked the memory section: %v", snap.MemoryErr)
	}
	if len(snap.Filesystems) == 0 || snap.Memory == nil {
		t.Fatal("live sections came back empty")
	}
	var apiErr *APIError
	if !errors.As(snap.CPUErr, &apiErr) {
		t.Fatalf("CPUErr = %v, want *APIError", snap.CPUErr)
	}
	if snap.CPU != nil {
		t.Fatal("CPU is non-nil alongside CPUErr")
	}
}

// TestEmptyDiskResultIsNoDataNotAnEmptyTable. "Prometheus has no filesystem
// series" is a different fact from "every filesystem was filtered out", and
// the renderer treats them differently.
func TestEmptyDiskResultIsNoDataNotAnEmptyTable(t *testing.T) {
	stub := healthyStub(t).
		on(QueryDiskSize, `{"status":"success","data":{"resultType":"vector","result":[]}}`).
		on(QueryDiskAvail, `{"status":"success","data":{"resultType":"vector","result":[]}}`).
		on(QueryDiskSizeAll, `{"status":"success","data":{"resultType":"vector","result":[]}}`).
		on(QueryDiskAvailAll, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	c, _ := newTestClient(t, stub.handler())

	snap := c.Snapshot(context.Background())
	if !errors.Is(snap.DiskErr, ErrNoData) {
		t.Fatalf("DiskErr = %v, want ErrNoData", snap.DiskErr)
	}
	if snap.MemoryErr != nil || snap.CPUErr != nil {
		t.Fatal("an empty disk vector took the other sections with it")
	}
}

// TestOnlyContainerFilesystemsFallsBackRatherThanGoingBlank covers the report
// that started this: /resources answered "disk unavailable" on a host whose
// only capacity-bearing mount is an overlay. The strict filter excludes
// overlay by design -- correctly on a normal host, uselessly on a
// containerised one -- and the section must still carry a number.
func TestOnlyContainerFilesystemsFallsBackRatherThanGoingBlank(t *testing.T) {
	size := vectorBody(
		fsSeries("tmpfs", "/run/lock", "tmpfs", "5242880"),
		fsSeries("overlay", "/var/lib/docker/overlay2/x/merged", "overlay", "107374182400"),
	)
	avail := vectorBody(
		fsSeries("tmpfs", "/run/lock", "tmpfs", "5242880"),
		fsSeries("overlay", "/var/lib/docker/overlay2/x/merged", "overlay", "10737418240"),
	)
	stub := healthyStub(t).on(QueryDiskSize, size).on(QueryDiskAvail, avail).
		on(QueryDiskSizeAll, size).on(QueryDiskAvailAll, avail)
	c, _ := newTestClient(t, stub.handler())

	snap := c.Snapshot(context.Background())
	if snap.DiskErr != nil {
		t.Fatalf("DiskErr = %v, want a fallback reading", snap.DiskErr)
	}
	if !snap.DiskFallback {
		t.Fatal("the reading came from the relaxed pass but is not marked as a fallback")
	}
	if len(snap.Filesystems) != 1 {
		t.Fatalf("got %+v, want just the overlay", snap.Filesystems)
	}
	if fs := snap.Filesystems[0]; fs.FSType != "overlay" || int(fs.UsedPercent) != 90 {
		t.Fatalf("fallback row = %+v, want the overlay at 90%% used", fs)
	}
	for _, fs := range snap.Filesystems {
		if fs.FSType == "tmpfs" {
			t.Fatal("tmpfs carries no durable capacity and must stay out even here")
		}
	}
}

// TestRepeatedContainerViewsCollapse: a dozen Docker overlays of one disk all
// report the same size and free bytes, and must not fill the table with the
// same number twelve times.
func TestRepeatedContainerViewsCollapse(t *testing.T) {
	size := vectorBody(
		fsSeries("overlay", "/var/lib/docker/overlay2/a/merged", "overlay", "107374182400"),
		fsSeries("overlay", "/var/lib/docker/overlay2/b/merged", "overlay", "107374182400"),
		fsSeries("overlay", "/var/lib/docker/overlay2/c/merged", "overlay", "107374182400"),
	)
	avail := vectorBody(
		fsSeries("overlay", "/var/lib/docker/overlay2/a/merged", "overlay", "10737418240"),
		fsSeries("overlay", "/var/lib/docker/overlay2/b/merged", "overlay", "10737418240"),
		fsSeries("overlay", "/var/lib/docker/overlay2/c/merged", "overlay", "10737418240"),
	)
	stub := healthyStub(t).on(QueryDiskSize, size).on(QueryDiskAvail, avail).
		on(QueryDiskSizeAll, size).on(QueryDiskAvailAll, avail)
	c, _ := newTestClient(t, stub.handler())

	snap := c.Snapshot(context.Background())
	if len(snap.Filesystems) != 1 {
		t.Fatalf("got %d rows, want one collapsed row: %+v", len(snap.Filesystems), snap.Filesystems)
	}
}

// A host with a real filesystem must never reach the relaxed pass, or the
// overlay double-counting the strict filter exists to prevent comes back.
func TestRealFilesystemsNeverTriggerTheFallback(t *testing.T) {
	c, _ := newTestClient(t, healthyStub(t).handler())

	snap := c.Snapshot(context.Background())
	if snap.DiskFallback {
		t.Fatal("a host with a real ext4 root fell back to the relaxed view")
	}
	if len(snap.Filesystems) == 0 {
		t.Fatal("the strict pass returned nothing on a healthy host")
	}
}

// TestZeroSizedFilesystemIsDropped: a size of zero has no percentage, and
// inventing one would be a fabricated reading.
func TestZeroSizedFilesystemIsDropped(t *testing.T) {
	stub := healthyStub(t).
		on(QueryDiskSize, vectorBody(
			fsSeries("/dev/sda1", "/", "ext4", "0"),
			fsSeries("/dev/sdb1", "/data", "xfs", "1000"),
		)).
		on(QueryDiskAvail, vectorBody(
			fsSeries("/dev/sda1", "/", "ext4", "0"),
			fsSeries("/dev/sdb1", "/data", "xfs", "500"),
		))
	c, _ := newTestClient(t, stub.handler())

	snap := c.Snapshot(context.Background())
	if len(snap.Filesystems) != 1 || snap.Filesystems[0].Mountpoint != "/data" {
		t.Fatalf("got %+v, want only /data", snap.Filesystems)
	}
}

// TestUnpairedFilesystemIsDropped: a mount present in only one of the two
// queries has no consistent denominator.
func TestUnpairedFilesystemIsDropped(t *testing.T) {
	stub := healthyStub(t).
		on(QueryDiskSize, vectorBody(fsSeries("/dev/sda1", "/", "ext4", "1000"))).
		on(QueryDiskAvail, vectorBody(
			fsSeries("/dev/sda1", "/", "ext4", "500"),
			fsSeries("/dev/sdc1", "/orphan", "ext4", "500"),
		))
	c, _ := newTestClient(t, stub.handler())

	snap := c.Snapshot(context.Background())
	if len(snap.Filesystems) != 1 || snap.Filesystems[0].Mountpoint != "/" {
		t.Fatalf("got %+v, want only /", snap.Filesystems)
	}
}

// TestCPUPercentIsClamped: a counter reset inside the rate window can push the
// derived percentage marginally outside [0,100].
func TestCPUPercentIsClamped(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"-0.0000001", "0"},
		{"100.0000001", "100"},
	} {
		stub := healthyStub(t).on(QueryCPUUsed, vectorBody(scalarSeries(tc.raw)))
		c, _ := newTestClient(t, stub.handler())
		snap := c.Snapshot(context.Background())
		if snap.CPU == nil {
			t.Fatalf("CPU section nil for %q: %v", tc.raw, snap.CPUErr)
		}
		if snap.CPU.UsedPercent < 0 || snap.CPU.UsedPercent > 100 {
			t.Fatalf("CPU %.6f%% from raw %q is outside [0,100]", snap.CPU.UsedPercent, tc.raw)
		}
	}
}

// TestSnapshotUnreachablePrometheusMarksEverySection.
func TestSnapshotUnreachablePrometheusMarksEverySection(t *testing.T) {
	c, err := New("http://127.0.0.1:1", WithTimeout(500_000_000))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snap := c.Snapshot(context.Background())
	for name, secErr := range map[string]error{"disk": snap.DiskErr, "memory": snap.MemoryErr, "cpu": snap.CPUErr} {
		if !errors.Is(secErr, ErrUnreachable) {
			t.Errorf("%s err = %v, want ErrUnreachable", name, secErr)
		}
	}
	if snap.Memory != nil || snap.CPU != nil || len(snap.Filesystems) != 0 {
		t.Fatal("an unreachable Prometheus produced data")
	}
}
