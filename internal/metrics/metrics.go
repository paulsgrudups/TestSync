// Package metrics keeps the server's counters and writes them in the
// Prometheus text exposition format.
//
// It is deliberately small: a handful of counters and gauges do not justify
// the Prometheus client library and its dependency tree. Label values are
// always drawn from a closed set chosen by the server - a route template, a
// command name, a release reason - never from what a client sent, so the
// number of series stays bounded.
package metrics

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// CounterVec is a counter partitioned by a fixed list of labels. It is safe
// for concurrent use.
type CounterVec struct {
	name   string
	help   string
	labels []string

	mu     sync.Mutex
	values map[string]uint64
}

// NewCounterVec creates a counter with the given label names.
func NewCounterVec(name, help string, labels ...string) *CounterVec {
	return &CounterVec{name: name, help: help, labels: labels, values: make(map[string]uint64)}
}

// Inc adds one to the series with the given label values, which must match
// the label names in number and order. A nil CounterVec counts nothing, so a
// component built without metrics needs no special case.
func (c *CounterVec) Inc(values ...string) {
	if c == nil {
		return
	}

	if len(values) != len(c.labels) {
		panic(fmt.Sprintf("metrics: %s takes %d label values, got %d", c.name, len(c.labels), len(values)))
	}

	key := seriesKey(c.labels, values)

	c.mu.Lock()
	c.values[key]++
	c.mu.Unlock()
}

// Value returns the current value of one series. It exists for tests.
func (c *CounterVec) Value(values ...string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.values[seriesKey(c.labels, values)]
}

// Sample is one gauge series, computed when the metrics are scraped.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Gauge is a metric whose value is read at scrape time rather than
// maintained, such as how many agents are connected right now.
type Gauge struct {
	Name    string
	Help    string
	Samples []Sample
}

// WriteCounter writes a counter family. Series appear in a stable order, so
// consecutive scrapes are easy to compare by eye.
func WriteCounter(w io.Writer, c *CounterVec) error {
	c.mu.Lock()
	snapshot := maps.Clone(c.values)
	c.mu.Unlock()

	var b strings.Builder

	writeHeader(&b, c.name, c.help, "counter")

	for _, key := range slices.Sorted(maps.Keys(snapshot)) {
		fmt.Fprintf(&b, "%s%s %d\n", c.name, key, snapshot[key])
	}

	_, err := io.WriteString(w, b.String())

	return err
}

// WriteGauge writes a gauge family.
func WriteGauge(w io.Writer, g Gauge) error {
	var b strings.Builder

	writeHeader(&b, g.Name, g.Help, "gauge")

	for _, s := range g.Samples {
		names := slices.Sorted(maps.Keys(s.Labels))
		values := make([]string, len(names))

		for i, name := range names {
			values[i] = s.Labels[name]
		}

		fmt.Fprintf(&b, "%s%s %s\n", g.Name, seriesKey(names, values),
			strconv.FormatFloat(s.Value, 'g', -1, 64))
	}

	_, err := io.WriteString(w, b.String())

	return err
}

func writeHeader(b *strings.Builder, name, help, kind string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, kind)
}

// seriesKey renders a label set as it appears in the exposition format:
// {a="1",b="2"}, or nothing at all for a series without labels.
func seriesKey(names, values []string) string {
	if len(names) == 0 {
		return ""
	}

	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + `="` + escapeLabel(values[i]) + `"`
	}

	return "{" + strings.Join(parts, ",") + "}"
}

// The escapers are immutable once built.
var (
	labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }

func escapeHelp(s string) string { return helpEscaper.Replace(s) }
