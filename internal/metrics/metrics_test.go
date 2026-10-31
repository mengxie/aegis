package metrics

import (
	"strings"
	"testing"
)

func TestPrometheusTextFormat(t *testing.T) {
	var r Registry
	requests := r.Counter("aegis_requests_total", "Requests.", "team")
	requests.Add(2, `sup"port`)
	latency := r.Histogram("aegis_seconds", "Latency.", []float64{0.5, 1})
	latency.Observe(0.7)
	var out strings.Builder
	_, _ = r.WriteTo(&out)
	for _, want := range []string{
		"# TYPE aegis_requests_total counter",
		`aegis_requests_total{team="sup\"port"} 2`,
		`aegis_seconds_bucket{le="0.5"} 0`,
		`aegis_seconds_bucket{le="1"} 1`,
		`aegis_seconds_bucket{le="+Inf"} 1`,
		"aegis_seconds_count 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
