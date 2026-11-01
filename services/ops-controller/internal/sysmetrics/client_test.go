package sysmetrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// vectorBody builds a Prometheus instant-vector envelope. The sample value is
// a string on the wire, which is the single most commonly mis-decoded part of
// this API, so the fixtures spell it out rather than letting a helper hide it.
func vectorBody(series ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` +
		strings.Join(series, ",") + `]}}`
}

func fsSeries(device, mount, fstype, value string) string {
	return `{"metric":{"__name__":"node_filesystem_avail_bytes","device":"` + device +
		`","mountpoint":"` + mount + `","fstype":"` + fstype +
		`","instance":"node-exporter:9100"},"value":[1700000000.5,"` + value + `"]}`
}

func scalarSeries(value string) string {
	return `{"metric":{},"value":[1700000000.5,"` + value + `"]}`
}

// newTestClient points a client at a handler with no network timeout games.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, WithHTTPClient(srv.Client()), WithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

// TestQueryDecodesStringSampleValues is the load-bearing wire-format
// assertion: data.result[].value[1] is a string, and decoding it as a number
// fails silently in the shapes that matter.
func TestQueryDecodesStringSampleValues(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(vectorBody(scalarSeries("42.5"))))
	})

	samples, err := c.Query(context.Background(), "up")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(samples) != 1 || samples[0].Value != 42.5 {
		t.Fatalf("got %+v, want one sample of 42.5", samples)
	}
}

// TestQuerySendsPromQLOnTheQueryEndpoint checks the request shape: the path,
// the parameter name, and that the expression survives percent-encoding with
// its regex matchers intact.
func TestQuerySendsPromQLOnTheQueryEndpoint(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(vectorBody(scalarSeries("1"))))
	})

	if _, err := c.Query(context.Background(), QueryDiskAvail); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotPath != "/api/v1/query" {
		t.Fatalf("path = %q, want /api/v1/query", gotPath)
	}
	if gotQuery != QueryDiskAvail {
		t.Fatalf("query round-tripped as %q, want %q", gotQuery, QueryDiskAvail)
	}
	if !strings.Contains(gotQuery, "tmpfs") || !strings.Contains(gotQuery, "mountpoint!~") {
		t.Fatalf("filters did not survive transport: %q", gotQuery)
	}
}

// TestQueryUnreachableIsTypedAndDistinct: an unreachable Prometheus must not
// look like "no data". The two mean different things to an operator.
func TestQueryUnreachableIsTypedAndDistinct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing is listening now

	c, err := New(base, WithTimeout(time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Query(context.Background(), "up")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrNoData) {
		t.Fatal("unreachable must not be reported as no-data")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatal("unreachable must not be reported as an API error")
	}
}

// TestQueryPrometheusErrorStatusIsTypedAPIError covers a 200 carrying
// status:"error", which is how Prometheus reports several conditions.
func TestQueryPrometheusErrorStatusIsTypedAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid parameter"}`))
	})

	_, err := c.Query(context.Background(), "up")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != "error" || apiErr.ErrorType != "bad_data" {
		t.Fatalf("apiErr = %+v, want status/errorType populated", apiErr)
	}
	if errors.Is(err, ErrNoData) || errors.Is(err, ErrUnreachable) {
		t.Fatal("a refused query must be distinguishable from no-data and unreachable")
	}
}

// TestQueryHTTPErrorIsTypedAPIError covers a non-2xx, including a body that is
// not an envelope at all (a proxy error page).
func TestQueryHTTPErrorIsTypedAPIError(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"bad request with envelope", http.StatusBadRequest, `{"status":"error","errorType":"bad_data","error":"parse error"}`},
		{"gateway html", http.StatusBadGateway, "<html>502</html>"},
		{"unauthorized empty", http.StatusUnauthorized, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.Query(context.Background(), "up")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if apiErr.StatusCode != tc.code {
				t.Fatalf("StatusCode = %d, want %d", apiErr.StatusCode, tc.code)
			}
			if !apiErr.IsHTTP() {
				t.Fatal("IsHTTP() = false for a non-2xx")
			}
			if err.Error() == "" {
				t.Fatal("APIError renders an empty string")
			}
		})
	}
}

// TestQueryEmptyResultIsNoData: success with an empty vector is its own fact.
func TestQueryEmptyResultIsNoData(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	})

	_, err := c.Query(context.Background(), "up")
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
	if errors.Is(err, ErrUnreachable) {
		t.Fatal("no-data must not be reported as unreachable")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatal("no-data must not be reported as an API error")
	}
}

// TestQueryRejectsMalformedBodies. Every case here is something a broken or
// hostile upstream can send; none of them may become a number in a reply.
func TestQueryRejectsMalformedBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"not json", "this is not json"},
		{"matrix result type", `{"status":"success","data":{"resultType":"matrix","result":[]}}`},
		{"scalar result type", `{"status":"success","data":{"resultType":"scalar","result":[]}}`},
		{"value is a number not a string", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,12]}]}}`},
		{"value pair is short", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000]}]}}`},
		{"value is not numeric", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"twelve"]}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			if _, err := c.Query(context.Background(), "up"); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("err = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

// TestQueryRejectsOversizedBody proves the io.LimitReader bound is enforced
// rather than decorative: the read stops and the caller is told why.
func TestQueryRejectsOversizedBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[`))
		chunk := strings.Repeat(scalarSeries("1")+",", 64)
		for written := 0; written < maxResponseBytes+len(chunk); written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	})

	_, err := c.Query(context.Background(), "up")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

// TestQueryDropsNonFiniteSamples. A NaN gauge is missing data; rendering
// "NaN%" as a disk figure is worse than rendering "unavailable".
func TestQueryDropsNonFiniteSamples(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(vectorBody(scalarSeries("NaN"), scalarSeries("+Inf"))))
	})

	if _, err := c.Query(context.Background(), "up"); !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData once non-finite samples are dropped", err)
	}
}

// TestQueryBoundsItsOwnDeadline: a caller with no deadline must still not wait
// forever on a hung Prometheus.
func TestQueryBoundsItsOwnDeadline(t *testing.T) {
	release := make(chan struct{})
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	c.timeout = 150 * time.Millisecond
	t.Cleanup(func() { close(release) })

	start := time.Now()
	_, err := c.Query(context.Background(), "up")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable on timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("query took %s; the timeout is not bounding it", elapsed)
	}
}

// TestQueryRejectsUnusableExpressions guards the outbound bounds.
func TestQueryRejectsUnusableExpressions(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should have been made")
	})

	if _, err := c.Query(context.Background(), "   "); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("err = %v, want ErrEmptyQuery", err)
	}
	if _, err := c.Query(context.Background(), strings.Repeat("x", maxQueryChars+1)); !errors.Is(err, ErrQueryTooLong) {
		t.Fatalf("err = %v, want ErrQueryTooLong", err)
	}
}

// TestNewValidatesBaseURL. A misconfigured URL must fail at construction, not
// at 3am inside a command.
func TestNewValidatesBaseURL(t *testing.T) {
	if c, err := New(""); err != nil || c.BaseURL() != DefaultBaseURL {
		t.Fatalf("New(\"\") = %v, %v; want the default base URL", c, err)
	}
	for _, bad := range []string{"::not a url", "ftp://prometheus:9090", "prometheus:9090", "/relative"} {
		if _, err := New(bad); err == nil {
			t.Fatalf("New(%q) succeeded; want an error", bad)
		}
	}
	c, err := New("http://prometheus:9090/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.BaseURL() != "http://prometheus:9090" {
		t.Fatalf("BaseURL = %q, want the trailing slash trimmed", c.BaseURL())
	}
	if _, err := url.Parse(c.BaseURL()); err != nil {
		t.Fatalf("BaseURL is not parseable: %v", err)
	}
}

// TestNilClientDoesNotPanic: the command's dependency is allowed to be unwired.
func TestNilClientDoesNotPanic(t *testing.T) {
	var c *Client
	if _, err := c.Query(context.Background(), "up"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	snap := c.Snapshot(context.Background())
	if snap.DiskErr == nil || snap.MemoryErr == nil || snap.CPUErr == nil {
		t.Fatalf("nil client produced a snapshot with a live section: %+v", snap)
	}
	if c.BaseURL() != "" || c.Timeout() != DefaultTimeout {
		t.Fatal("nil client accessors misbehaved")
	}
}
