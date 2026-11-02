package server

import (
	"net/http"
	"sync"
	"time"
)

// Metrics is a lightweight, self-made Prometheus text-format exposition.
// No client dependency; format-compatible with Prometheus.
type Metrics struct {
	mu            sync.RWMutex
	reqByStatus   map[string]int64 // "200" → count
	upstreamErrs  map[string]int64 // "provider|status" → count
	latBuckets    []int64          // aligned with latLe
	latCount      int64
	latSum        float64
	activeStreams int64
	startedAt     time.Time
	version       string
	routeByStatus map[string]int64 // model/route label → count (successful chat calls)
}

var latLe = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// NewMetrics creates a fresh metrics registry.
func NewMetrics() *Metrics {
	return &Metrics{
		reqByStatus:   make(map[string]int64),
		upstreamErrs:  make(map[string]int64),
		latBuckets:    make([]int64, len(latLe)),
		activeStreams: 0,
		startedAt:     time.Now(),
		routeByStatus: make(map[string]int64),
	}
}

// ---------------------------------------------------------------------------
// recording (all safe for concurrent use)
// ---------------------------------------------------------------------------

// ActiveStreamsInc decrements via the returned function.
func (m *Metrics) ActiveStreamsInc() func() {
	m.mu.Lock()
	m.activeStreams++
	m.mu.Unlock()
	did := false
	return func() {
		if did {
			return
		}
		did = true
		m.mu.Lock()
		m.activeStreams--
		if m.activeStreams < 0 {
			m.activeStreams = 0
		}
		m.mu.Unlock()
	}
}

// RecordRequest records one completed request with its latency.
func (m *Metrics) RecordRequest(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latCount++
	dv := d.Seconds()
	m.latSum += dv
	for i, le := range latLe {
		if dv <= le {
			m.latBuckets[i]++
		}
	}
}

// RecordUpstreamError records one upstream failure for provider/status.
func (m *Metrics) RecordUpstreamError(provider string, status int) {
	key := provider + "|" + itoa(status)
	m.mu.Lock()
	m.upstreamErrs[key]++
	m.mu.Unlock()
}

// RecordRoute records one successful chat call by model label.
func (m *Metrics) RecordRoute(label string) {
	m.mu.Lock()
	m.routeByStatus[label]++
	m.mu.Unlock()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---------------------------------------------------------------------------
// exposition (Prometheus text format)
// ---------------------------------------------------------------------------

// WriteTo writes the registry in Prometheus text exposition format.
func (m *Metrics) WriteTo(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	m.mu.RLock()
	defer m.mu.RUnlock()

	w.Write([]byte(`# HELP freellm_up time the router has been up (seconds).
# TYPE freellm_up gauge
`))
	val := int(time.Since(m.startedAt).Seconds())
	w.Write([]byte("freellm_up " + itoa(val) + "\n"))
	w.Write([]byte(`# HELP freellm_active_streams number of streams currently in flight.
# TYPE freellm_active_streams gauge
`))
	w.Write([]byte("freellm_active_streams " + itoa(int(m.activeStreams)) + "\n"))
	w.Write([]byte(`# HELP freellm_requests_total total completed HTTP requests by status.
# TYPE freellm_requests_total counter
`))
	for _, k := range sortedKeys(m.reqByStatus) {
		w.Write([]byte("freellm_requests_total{status=\"" + k + "\"} " + itoa(int(m.reqByStatus[k])) + "\n"))
	}
	w.Write([]byte(`# HELP freellm_chat_requests_total successful chat completions by model label.
# TYPE freellm_chat_requests_total counter
`))
	for _, k := range sortedKeys(m.routeByStatus) {
		w.Write([]byte("freellm_chat_requests_total{model=\"" + k + "\"} " + itoa(int(m.routeByStatus[k])) + "\n"))
	}
	w.Write([]byte(`# HELP freellm_upstream_errors_total upstream failures by provider and status.
# TYPE freellm_upstream_errors_total counter
`))
	for _, k := range sortedKeys(m.upstreamErrs) {
		provider, status := splitOnce(k)
		w.Write([]byte("freellm_upstream_errors_total{provider=\"" + provider + "\",status=\"" + status + "\"} " + itoa(int(m.upstreamErrs[k])) + "\n"))
	}
	w.Write([]byte(`# HELP freellm_request_duration_seconds request latency histogram.
# TYPE freellm_request_duration_seconds histogram
`))
	cum := int64(0)
	for i, le := range latLe {
		cum += m.latBuckets[i]
		w.Write([]byte("freellm_request_duration_seconds_bucket{le=\"" + fmtFloat(le) + "\"} " + itoa(int(cum)) + "\n"))
	}
	w.Write([]byte("freellm_request_duration_seconds_bucket{le=\"+Inf\"} " + itoa(int(m.latCount)) + "\n"))
	w.Write([]byte("freellm_request_duration_seconds_sum " + fmtFloat(m.latSum) + "\n"))
	w.Write([]byte("freellm_request_duration_seconds_count " + itoa(int(m.latCount)) + "\n"))
}

func splitOnce(k string) (provider, status string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '|' {
			return k[:i], k[i+1:]
		}
	}
	return k, "0"
}

func fmtFloat(f float64) string {
	if f == 0 {
		return "0.0"
	}
	s := ""
	neg := f < 0
	if neg {
		f = -f
		s = "-"
	}
	ip := int64(f)
	frac := f - float64(ip)
	if frac > 0 && ip == 0 {
		// 0.xxxxx
		ds := 6
		fv := frac
		buf := make([]byte, 0, 8)
		for j := 0; j < ds; j++ {
			fv *= 10
			d := int(fv) % 10
			buf = append(buf, byte('0'+d))
		}
		return s + "0." + string(buf)
	}
	if ip > 0 && frac > 0 {
		// keep up to 4 significant decimals
		m := int64(frac * 10000)
		for m%10 == 0 {
			m /= 10
		}
		digits := make([]byte, 0, 6)
		for m > 0 {
			digits = append([]byte{byte('0' + m%10)}, digits...)
			m /= 10
		}
		return s + itoa(int(ip)) + "." + string(digits)
	}
	return s + itoa(int(ip))
}

// sortedKeys returns map keys in sorted order (small N) for deterministic output.
func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
