// Package metrics is a minimal, dependency-free Prometheus text-format
// registry for the worker. It intentionally mirrors the API's observability
// approach (hand-rolled Prometheus text) so no new client dependency is added.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// Registry holds counters and gauges rendered on scrape.
type Registry struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
	order    []string
}

func New() *Registry {
	return &Registry{counters: map[string]float64{}, gauges: map[string]float64{}}
}

// IncCounter increments a Prometheus counter (untyped).
func (r *Registry) IncCounter(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[name]++
	r.trackOrder(name)
}

// AddCounter adds delta to a counter.
func (r *Registry) AddCounter(name string, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[name] += delta
	r.trackOrder(name)
}

// SetGauge sets a Prometheus gauge.
func (r *Registry) SetGauge(name string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[name] = value
	r.trackOrder(name)
}

func (r *Registry) trackOrder(name string) {
	for _, existing := range r.order {
		if existing == name {
			return
		}
	}
	r.order = append(r.order, name)
}

// SafeMetricName maps an arbitrary string (e.g. a queue or request-reason
// label) to a valid Prometheus metric-name component. Empty input maps to
// "unknown"; a leading digit is prefixed so the component stays valid.
func SafeMetricName(value string) string {
	if value == "" {
		return "unknown"
	}
	var name string
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			name += string(r)
		default:
			name += "_"
		}
	}
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// Handler serves Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		names := append([]string{}, r.order...)
		counters := make(map[string]float64, len(r.counters))
		gauges := make(map[string]float64, len(r.gauges))
		for key, value := range r.counters {
			counters[key] = value
		}
		for key, value := range r.gauges {
			gauges[key] = value
		}
		r.mu.Unlock()

		sort.Strings(names)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		for _, name := range names {
			if value, ok := counters[name]; ok {
				fmt.Fprintf(w, "# TYPE %s counter\n%s %g\n", name, name, value)
			}
			if value, ok := gauges[name]; ok {
				fmt.Fprintf(w, "# TYPE %s gauge\n%s %g\n", name, name, value)
			}
		}
	})
}
