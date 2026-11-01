package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/sysmetrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// ---------------------------------------------------------------------------
// /resources rendering. Every helper here carries a resource prefix: this
// package holds several commands' tests and the names must not collide.
// ---------------------------------------------------------------------------

// resourceStub serves canned Prometheus envelopes keyed by PromQL expression.
func resourceStub(t *testing.T, bodies map[string]string) *sysmetrics.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Query().Get("query")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"unstubbed"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c, err := sysmetrics.New(srv.URL, sysmetrics.WithHTTPClient(srv.Client()), sysmetrics.WithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("sysmetrics.New: %v", err)
	}
	return c
}

func resourceVector(series ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(series, ",") + `]}}`
}

func resourceFS(device, mount, fstype, value string) string {
	return `{"metric":{"device":"` + device + `","mountpoint":"` + mount + `","fstype":"` + fstype +
		`"},"value":[1700000000,"` + value + `"]}`
}

func resourceScalar(value string) string {
	return `{"metric":{},"value":[1700000000,"` + value + `"]}`
}

// resourceHealthyBodies is the baseline fixture: a 60%-used root, a quiet
// /data, and the pseudo-filesystems node-exporter really reports.
func resourceHealthyBodies() map[string]string {
	size := resourceVector(
		resourceFS("/dev/sda1", "/", "ext4", "100000000000"),
		resourceFS("/dev/sdb1", "/data", "xfs", "500000000000"),
		resourceFS("tmpfs", "/run/lock", "tmpfs", "5242880"),
		resourceFS("overlay", "/var/lib/docker/overlay2/a/merged", "overlay", "100000000000"),
	)
	avail := resourceVector(
		resourceFS("/dev/sda1", "/", "ext4", "40000000000"),
		resourceFS("/dev/sdb1", "/data", "xfs", "450000000000"),
		resourceFS("tmpfs", "/run/lock", "tmpfs", "5242880"),
		resourceFS("overlay", "/var/lib/docker/overlay2/a/merged", "overlay", "40000000000"),
	)
	return map[string]string{
		sysmetrics.QueryDiskSize:     size,
		sysmetrics.QueryDiskAvail:    avail,
		sysmetrics.QueryMemTotal:     resourceVector(resourceScalar("16000000000")),
		sysmetrics.QueryMemAvailable: resourceVector(resourceScalar("6000000000")),
		sysmetrics.QueryCPUUsed:      resourceVector(resourceScalar("23.75")),
	}
}

func resourceReply(t *testing.T, deps ResourceDeps) string {
	t.Helper()
	resp, err := deps.resources(context.Background(), telegram.Request{})
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if resp.Silent {
		t.Fatal("/resources replied Silent; an ops read command must always answer")
	}
	if strings.TrimSpace(resp.Body) == "" {
		t.Fatal("/resources replied with an empty body")
	}
	return resp.Body
}

// TestResourcesRendersDiskFirst. The command exists to answer "how close to
// full is it", so disk must lead and must carry both figures.
func TestResourcesRendersDiskFirst(t *testing.T) {
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, resourceHealthyBodies())})

	diskAt := strings.Index(body, "Disk")
	memAt := strings.Index(body, "Memory")
	cpuAt := strings.Index(body, "CPU")
	if diskAt < 0 || memAt < 0 || cpuAt < 0 {
		t.Fatalf("a section heading is missing:\n%s", body)
	}
	if !(diskAt < memAt && memAt < cpuAt) {
		t.Fatalf("sections are out of order (disk %d, memory %d, cpu %d):\n%s", diskAt, memAt, cpuAt, body)
	}
	if !strings.Contains(body, "60.0%") {
		t.Fatalf("root used percentage missing:\n%s", body)
	}
	if !strings.Contains(body, "free") {
		t.Fatalf("free bytes missing:\n%s", body)
	}
	if !strings.Contains(body, "23.8%") {
		t.Fatalf("cpu reading missing:\n%s", body)
	}
	if strings.Contains(body, unavailable) {
		t.Fatalf("a healthy snapshot rendered %q:\n%s", unavailable, body)
	}
}

// TestResourcesExcludesPseudoFilesystems: an overlay reports the host disk a
// second time and a tmpfs at 100% is usually nothing at all.
func TestResourcesExcludesPseudoFilesystems(t *testing.T) {
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, resourceHealthyBodies())})

	for _, unwanted := range []string{"/run/lock", "/var/lib/docker"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("pseudo filesystem %q reached the reply:\n%s", unwanted, body)
		}
	}
	if !strings.Contains(body, "/data") {
		t.Fatalf("a real filesystem was filtered out:\n%s", body)
	}
}

// TestResourcesFlagsPressuredFilesystem is the point of the command. A 91%
// disk must be obvious at a glance, not buried in a column.
func TestResourcesFlagsPressuredFilesystem(t *testing.T) {
	bodies := resourceHealthyBodies()
	bodies[sysmetrics.QueryDiskSize] = resourceVector(
		resourceFS("/dev/sda1", "/", "ext4", "100000000000"),
		resourceFS("/dev/sdb1", "/data", "xfs", "100000000000"),
	)
	bodies[sysmetrics.QueryDiskAvail] = resourceVector(
		resourceFS("/dev/sda1", "/", "ext4", "9000000000"), // 91% used
		resourceFS("/dev/sdb1", "/data", "xfs", "60000000000"),
	)
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, bodies)})

	if !strings.Contains(body, resourceWarnGlyph) {
		t.Fatalf("a 91%% filesystem was not flagged:\n%s", body)
	}
	if !strings.Contains(body, "91.0%") {
		t.Fatalf("the 91%% figure is missing:\n%s", body)
	}
	// The pressured filesystem sorts first, so the glyph must appear before
	// the quiet one's row.
	glyphAt := strings.Index(body, resourceWarnGlyph)
	quietAt := strings.Index(body, "/data")
	if glyphAt < 0 || quietAt < 0 || glyphAt > quietAt {
		t.Fatalf("the flagged filesystem is not first:\n%s", body)
	}
	if !strings.Contains(body, "at or above") {
		t.Fatalf("the pressure summary line is missing:\n%s", body)
	}
}

// TestResourcesDoesNotFlagQuietFilesystems.
func TestResourcesDoesNotFlagQuietFilesystems(t *testing.T) {
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, resourceHealthyBodies())})
	if strings.Contains(body, resourceWarnGlyph) {
		t.Fatalf("a 60%% filesystem was flagged as pressure:\n%s", body)
	}
}

// TestResourcesPrometheusUnreachableIsUnavailableNotAnError. An ops command
// whose backend is down must say so, in every section, and must not surface an
// error into the poller.
func TestResourcesPrometheusUnreachableIsUnavailableNotAnError(t *testing.T) {
	// Nothing is listening on port 1.
	client, err := sysmetrics.New("http://127.0.0.1:1", sysmetrics.WithTimeout(500*time.Millisecond))
	if err != nil {
		t.Fatalf("sysmetrics.New: %v", err)
	}
	resp, err := ResourceDeps{Metrics: client, QueryTimeout: 3 * time.Second}.
		resources(context.Background(), telegram.Request{})
	if err != nil {
		t.Fatalf("resources returned an error instead of degrading: %v", err)
	}
	if resp.Silent || strings.TrimSpace(resp.Body) == "" {
		t.Fatal("an unreachable Prometheus produced no reply")
	}
	if strings.Count(resp.Body, unavailable) < 3 {
		t.Fatalf("every section must render %q, got:\n%s", unavailable, resp.Body)
	}
	// The headings survive: a section that vanishes reads as "fine".
	for _, heading := range []string{"Disk", "Memory", "CPU"} {
		if !strings.Contains(resp.Body, heading) {
			t.Fatalf("section %q was omitted rather than marked unavailable:\n%s", heading, resp.Body)
		}
	}
}

// TestResourcesHandlesPrometheusErrorStatus: a 200 carrying status:"error".
func TestResourcesHandlesPrometheusErrorStatus(t *testing.T) {
	errBody := `{"status":"error","errorType":"bad_data","error":"invalid parameter \"query\""}`
	bodies := map[string]string{}
	for q := range resourceHealthyBodies() {
		bodies[q] = errBody
	}
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, bodies)})

	if strings.Count(body, unavailable) < 3 {
		t.Fatalf("a refused query must render %q per section:\n%s", unavailable, body)
	}
	if strings.Contains(body, "bad_data") || strings.Contains(body, "invalid parameter") {
		t.Fatalf("upstream error text leaked into an operator reply:\n%s", body)
	}
}

// TestResourcesHandlesEmptyResultSet. Prometheus is up, the metric is not
// being scraped: still "unavailable", never a blank line.
func TestResourcesHandlesEmptyResultSet(t *testing.T) {
	empty := `{"status":"success","data":{"resultType":"vector","result":[]}}`
	bodies := map[string]string{}
	for q := range resourceHealthyBodies() {
		bodies[q] = empty
	}
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, bodies)})

	if strings.Count(body, unavailable) < 3 {
		t.Fatalf("an empty result set must render %q per section:\n%s", unavailable, body)
	}
}

// TestResourcesHandlesMalformedBody: a proxy that returns HTML, a value that
// is a number rather than the string the API actually sends.
func TestResourcesHandlesMalformedBody(t *testing.T) {
	for name, body := range map[string]string{
		"html":             "<html><body>502 Bad Gateway</body></html>",
		"numeric value":    `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,2]}]}}`,
		"wrong resulttype": `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			bodies := map[string]string{}
			for q := range resourceHealthyBodies() {
				bodies[q] = body
			}
			reply := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, bodies)})
			if strings.Count(reply, unavailable) < 3 {
				t.Fatalf("a malformed body must render %q per section:\n%s", unavailable, reply)
			}
			if strings.Contains(reply, "<html>") {
				t.Fatalf("an upstream body was echoed into the reply:\n%s", reply)
			}
		})
	}
}

// TestResourcesDegradesSectionIndependently: one dead metric costs one line.
func TestResourcesDegradesSectionIndependently(t *testing.T) {
	bodies := resourceHealthyBodies()
	bodies[sysmetrics.QueryCPUUsed] = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, bodies)})

	if !strings.Contains(body, "60.0%") {
		t.Fatalf("a dead CPU metric blanked the disk section:\n%s", body)
	}
	if strings.Count(body, unavailable) != 1 {
		t.Fatalf("expected exactly one unavailable section:\n%s", body)
	}
}

// TestResourcesWithNilDepsReplies. Nil dependencies must degrade, never panic
// and never go silent.
func TestResourcesWithNilDepsReplies(t *testing.T) {
	var deps ResourceDeps // Metrics is nil
	resp, err := deps.resources(context.Background(), telegram.Request{})
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if resp.Silent {
		t.Fatal("nil deps produced a silent reply")
	}
	if strings.TrimSpace(resp.Body) == "" {
		t.Fatal("nil deps produced an empty reply")
	}
	if strings.Count(resp.Body, unavailable) < 3 {
		t.Fatalf("nil deps must mark every section %q:\n%s", unavailable, resp.Body)
	}
}

// TestRegisterResourcesRegistersAReadOnlyCommand.
func TestRegisterResourcesRegistersAReadOnlyCommand(t *testing.T) {
	registry := telegram.NewRegistry()
	if err := RegisterResources(registry, ResourceDeps{}); err != nil {
		t.Fatalf("RegisterResources: %v", err)
	}
	reg, ok := registry.Lookup("resources")
	if !ok {
		t.Fatal("/resources is not dispatchable after registration")
	}
	if reg.Mutating {
		t.Fatal("/resources is marked mutating; it only issues GET requests")
	}
	if strings.TrimSpace(reg.Summary) == "" {
		t.Fatal("/resources has no summary, so /help cannot render it")
	}
	// Registering twice must fail rather than shadow.
	if err := RegisterResources(registry, ResourceDeps{}); err == nil {
		t.Fatal("a duplicate registration succeeded")
	}
	if err := RegisterResources(nil, ResourceDeps{}); err == nil {
		t.Fatal("a nil registry was accepted")
	}
}

// TestResourcesBodyIsSendable: the reply must survive the chunker as valid
// HTML, because an unbalanced tag fails the whole Telegram send.
func TestResourcesBodyIsSendable(t *testing.T) {
	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, resourceHealthyBodies())})
	parts := telegram.Chunk(body)
	if len(parts) == 0 {
		t.Fatal("the reply chunked to nothing")
	}
	for _, part := range parts {
		if strings.Count(part, "<pre>") != strings.Count(part, "</pre>") {
			t.Fatalf("unbalanced <pre> in a sendable part:\n%s", part)
		}
		if strings.Count(part, "<b>") != strings.Count(part, "</b>") {
			t.Fatalf("unbalanced <b> in a sendable part:\n%s", part)
		}
	}
}

// TestResourceBytesRendersReadableUnits.
func TestResourceBytesRendersReadableUnits(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1073741824, "1.0 GiB"},
		{1099511627776, "1.0 TiB"},
		{-1, "?"},
	} {
		if got := resourceBytes(tc.in); got != tc.want {
			t.Errorf("resourceBytes(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResourcesCapsTheFilesystemTable. Bounded output: a host with many mounts
// must not produce a reply the chunker has to truncate mid-table.
func TestResourcesCapsTheFilesystemTable(t *testing.T) {
	var sizes, avails []string
	for i := 0; i < resourceDiskCap+4; i++ {
		mount := "/mnt/disk" + string(rune('a'+i))
		sizes = append(sizes, resourceFS("/dev/sd"+string(rune('a'+i)), mount, "ext4", "100000000000"))
		// Descending fullness, so the capped rows are the quietest.
		avails = append(avails, resourceFS("/dev/sd"+string(rune('a'+i)), mount, "ext4",
			strconv.Itoa(10000000000+i*5000000000)))
	}
	bodies := resourceHealthyBodies()
	bodies[sysmetrics.QueryDiskSize] = resourceVector(sizes...)
	bodies[sysmetrics.QueryDiskAvail] = resourceVector(avails...)

	body := resourceReply(t, ResourceDeps{Metrics: resourceStub(t, bodies)})
	if !strings.Contains(body, "not shown") {
		t.Fatalf("the table was not capped:\n%s", body)
	}
	// The fullest filesystem is /mnt/diska; it must survive the cap.
	if !strings.Contains(body, "/mnt/diska") {
		t.Fatalf("the fullest filesystem was capped away:\n%s", body)
	}
	if strings.Contains(body, "/mnt/diskj") {
		t.Fatalf("a capped row was rendered anyway:\n%s", body)
	}
}

// TestResourceSectionErrorsAreNotEchoed guards the reply against upstream
// text: renderResources must never interpolate an error string.
func TestResourceSectionErrorsAreNotEchoed(t *testing.T) {
	snap := sysmetrics.Snapshot{
		DiskErr:   errors.New("<script>alert(1)</script>"),
		MemoryErr: errors.New("secret-token-abc"),
		CPUErr:    errors.New("boom"),
	}
	body := renderResources(snap)
	for _, leak := range []string{"script", "secret-token-abc", "boom"} {
		if strings.Contains(body, leak) {
			t.Fatalf("error text %q reached the reply:\n%s", leak, body)
		}
	}
	if strings.Count(body, unavailable) < 3 {
		t.Fatalf("every failed section must render %q:\n%s", unavailable, body)
	}
}

// The report behind this: the reply said only "disk unavailable" while memory
// and CPU rendered fine. That is three different faults wearing one word, and
// none of them is actionable from a phone. Each now names itself.
func TestDiskFailuresSayWhichFailureTheyAre(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"no filesystem series", sysmetrics.ErrNoData, "node-exporter filesystem collector"},
		{"prometheus down", sysmetrics.ErrUnreachable, "unreachable"},
		{"query refused", &sysmetrics.APIError{StatusCode: 400, Status: "error"}, "refused the query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := renderResources(sysmetrics.Snapshot{DiskErr: tc.err})
			if !strings.Contains(body, tc.want) {
				t.Fatalf("disk line does not say %q:\n%s", tc.want, body)
			}
			if !strings.Contains(body, unavailable) {
				t.Fatalf("the section dropped its unavailable marker:\n%s", body)
			}
		})
	}
}

// A fallback reading is still a reading, but the reader must be told which
// view it is: on a normal host this note appearing is itself the problem.
func TestFallbackDiskViewIsLabelled(t *testing.T) {
	snap := sysmetrics.Snapshot{
		DiskFallback: true,
		Filesystems: []sysmetrics.Filesystem{
			{Device: "overlay", Mountpoint: "/var/lib/docker/overlay2/x/merged", FSType: "overlay",
				SizeBytes: 107374182400, AvailBytes: 10737418240, UsedBytes: 96636764160, UsedPercent: 90},
		},
	}
	body := renderResources(snap)
	// Only the disk section is asserted: this snapshot carries no memory or
	// CPU reading, so those rows are unavailable and rightly say so.
	disk := body[strings.Index(body, "Disk"):]
	if strings.Contains(disk[:strings.Index(disk, "Memory")], unavailable) {
		t.Fatalf("a host with a fallback reading still reported the disk unavailable:\n%s", body)
	}
	if !strings.Contains(body, "carries capacity") {
		t.Fatalf("the fallback view is not labelled:\n%s", body)
	}
	if !strings.Contains(body, "90") {
		t.Fatalf("the fallback reading lost its percentage:\n%s", body)
	}
}
