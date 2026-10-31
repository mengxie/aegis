// Package metrics exposes counters and histograms in the Prometheus text format, with no dependencies.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Registry struct {
	mu     sync.Mutex
	series []*family
}

type family struct {
	name, help, kind string
	labels           []string
	buckets          []float64
	mu               sync.Mutex
	values           map[string]*sample
}

type sample struct {
	labelValues []string
	value       float64  // counters
	counts      []uint64 // histograms, one per bucket
	sum         float64
	count       uint64
}

type Counter struct{ f *family }
type Histogram struct{ f *family }

func (r *Registry) register(f *family) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	f.values = map[string]*sample{}
	r.series = append(r.series, f)
	return f
}

func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	return &Counter{r.register(&family{name: name, help: help, kind: "counter", labels: labels})}
}

func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	return &Histogram{r.register(&family{name: name, help: help, kind: "histogram", labels: labels, buckets: buckets})}
}

func (f *family) sample(labelValues []string) *sample {
	key := strings.Join(labelValues, "\x00")
	s, ok := f.values[key]
	if !ok {
		s = &sample{labelValues: append([]string(nil), labelValues...), counts: make([]uint64, len(f.buckets))}
		f.values[key] = s
	}
	return s
}

func (c *Counter) Add(value float64, labelValues ...string) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.sample(labelValues).value += value
}

func (h *Histogram) Observe(value float64, labelValues ...string) {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	s := h.f.sample(labelValues)
	for i, upper := range h.f.buckets {
		if value <= upper {
			s.counts[i]++
		}
	}
	s.sum += value
	s.count++
}

// WriteTo writes every series in the Prometheus text exposition format.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	var b strings.Builder
	r.mu.Lock()
	families := append([]*family(nil), r.series...)
	r.mu.Unlock()
	for _, f := range families {
		f.mu.Lock()
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		keys := make([]string, 0, len(f.values))
		for key := range f.values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			s := f.values[key]
			if f.kind == "counter" {
				fmt.Fprintf(&b, "%s%s %s\n", f.name, labelSet(f.labels, s.labelValues, ""), format(s.value))
				continue
			}
			for i, upper := range f.buckets {
				fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelSet(f.labels, s.labelValues, format(upper)), s.counts[i])
			}
			fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelSet(f.labels, s.labelValues, "+Inf"), s.count)
			fmt.Fprintf(&b, "%s_sum%s %s\n", f.name, labelSet(f.labels, s.labelValues, ""), format(s.sum))
			fmt.Fprintf(&b, "%s_count%s %d\n", f.name, labelSet(f.labels, s.labelValues, ""), s.count)
		}
		f.mu.Unlock()
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

func labelSet(names, values []string, le string) string {
	pairs := make([]string, 0, len(names)+1)
	for i, name := range names {
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, name, escape(values[i])))
	}
	if le != "" {
		pairs = append(pairs, fmt.Sprintf(`le="%s"`, le))
	}
	if len(pairs) == 0 {
		return ""
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func escape(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}

func format(value float64) string {
	if math.IsInf(value, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
