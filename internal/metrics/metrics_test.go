package metrics

import (
	"strings"
	"testing"
)

// TestWriteCounterIsValidExposition pins the text format: a HELP and TYPE
// header, series in a stable order, and label values escaped so that a quote
// or newline in one cannot break the line it is on.
func TestWriteCounterIsValidExposition(t *testing.T) {
	t.Parallel()

	c := NewCounterVec("demo_total", "A demo.\nSecond line.", "route", "code")
	c.Inc("/b", "200")
	c.Inc("/a", "200")
	c.Inc("/a", "200")
	c.Inc(`/q"uote\`, "500")

	var b strings.Builder
	if err := WriteCounter(&b, c); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	want := `# HELP demo_total A demo.\nSecond line.
# TYPE demo_total counter
demo_total{route="/a",code="200"} 2
demo_total{route="/b",code="200"} 1
demo_total{route="/q\"uote\\",code="500"} 1
`

	if b.String() != want {
		t.Fatalf("\n got:\n%s\nwant:\n%s", b.String(), want)
	}
}

// TestWriteGauge covers a gauge with and without labels, the labels sorted by
// name.
func TestWriteGauge(t *testing.T) {
	t.Parallel()

	var b strings.Builder

	err := WriteGauge(&b, Gauge{Name: "demo", Help: "Demo.", Samples: []Sample{
		{Value: 3},
		{Value: 1, Labels: map[string]string{"z": "1", "a": "2"}},
	}})
	if err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	want := "# HELP demo Demo.\n# TYPE demo gauge\ndemo 3\ndemo{a=\"2\",z=\"1\"} 1\n"

	if b.String() != want {
		t.Fatalf("\n got: %q\nwant: %q", b.String(), want)
	}
}

// TestNilCounterCountsNothing covers the nil receiver a component built
// without metrics relies on.
func TestNilCounterCountsNothing(t *testing.T) {
	t.Parallel()

	var c *CounterVec
	c.Inc("anything")
}
