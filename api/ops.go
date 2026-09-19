package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/buildinfo"
	"github.com/paulsgrudups/testsync/internal/metrics"
)

// readinessTimeout bounds the storage check behind /readyz. A probe that
// hangs is worse than one that fails: the orchestrator's own timeout would
// fire with no explanation.
const readinessTimeout = 2 * time.Second

// metricsContentType is the Prometheus text exposition format, version 0.0.4.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// registerOperationalRoutes adds the routes that describe and probe the server
// rather than the test runs it holds (API-1, API-4).
//
// The descriptor, the version and both probes are unauthenticated: an
// orchestrator's probe cannot send credentials, and none of them reveals
// anything about a run. The metrics count what the runs do, so they sit behind
// the same credentials as everything else, which every Prometheus scrape
// config supports.
func registerOperationalRoutes(router *mux.Router, a *app.App) {
	router.HandleFunc("/", rootHandler(a)).Methods(http.MethodGet)
	router.HandleFunc("/version", versionHandler(a)).Methods(http.MethodGet)

	live := livenessHandler(a)
	router.HandleFunc("/healthz", live).Methods(http.MethodGet)
	// /health is the original name of the liveness probe, kept so existing
	// probes keep working.
	router.HandleFunc("/health", live).Methods(http.MethodGet)

	router.HandleFunc("/readyz", readinessHandler(a)).Methods(http.MethodGet)

	router.Handle("/metrics",
		auth.BasicAuthMiddleware(a.Auth, a.Log)(metricsHandler(a)),
	).Methods(http.MethodGet)
}

func rootHandler(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(r.Context(), a, w, http.StatusOK, buildinfo.Describe())
	}
}

func versionHandler(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(r.Context(), a, w, http.StatusOK, buildinfo.Get())
	}
}

// probeStatus is the body of both probes.
type probeStatus struct {
	Status string `json:"status"`

	// Checks names each dependency that was checked and whether it passed.
	// It is omitted by the liveness probe, which checks nothing but the
	// process answering.
	Checks map[string]string `json:"checks,omitempty"`
}

// livenessHandler answers as long as the process can serve a request at all.
// It deliberately checks no dependency: an orchestrator restarts a process
// whose liveness fails, and restarting does not bring a database back.
func livenessHandler(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(r.Context(), a, w, http.StatusOK, probeStatus{Status: "ok"})
	}
}

// readinessHandler answers 200 only while the server can do its job, which
// needs its storage. A 503 takes the instance out of rotation without
// restarting it.
func readinessHandler(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		if err := a.Service.Ready(ctx); err != nil {
			// The cause stays in the log: this route is unauthenticated, and
			// a storage error can describe the host's filesystem.
			a.Log.WarnContext(ctx, "readiness check failed", "error", err)

			writeJSON(r.Context(), a, w, http.StatusServiceUnavailable, probeStatus{
				Status: "not_ready",
				Checks: map[string]string{"storage": "failing"},
			})

			return
		}

		writeJSON(r.Context(), a, w, http.StatusOK, probeStatus{
			Status: "ready",
			Checks: map[string]string{"storage": "ok"},
		})
	}
}

// metricsHandler writes the server's metrics. The counters are maintained as
// things happen; the gauges are read from the registry now.
func metricsHandler(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", metricsContentType)
		w.Header().Set("Cache-Control", "no-store")

		if err := writeMetrics(w, a); err != nil {
			a.Log.DebugContext(r.Context(), "failed to write the metrics", "error", err)
		}
	}
}

func writeMetrics(w io.Writer, a *app.App) error {
	var runs, connections, waiting int

	for _, state := range a.Registry.States() {
		runs++

		for _, conn := range state.Connections {
			if conn.Active {
				connections++
			}
		}

		for _, cp := range state.Checkpoints {
			if cp.Waiting {
				waiting++
			}
		}
	}

	info := buildinfo.Get()

	gauges := []metrics.Gauge{
		{
			Name: "testsync_build_info",
			Help: "The running build. Always 1; the build is in the labels.",
			Samples: []metrics.Sample{{Value: 1, Labels: map[string]string{
				"version": info.Version, "commit": info.Commit, "go_version": info.GoVersion,
			}}},
		},
		{
			Name:    "testsync_runs_active",
			Help:    "Test runs registered on the server.",
			Samples: []metrics.Sample{{Value: float64(runs)}},
		},
		{
			Name:    "testsync_connections_active",
			Help:    "Agents connected over WebSocket.",
			Samples: []metrics.Sample{{Value: float64(connections)}},
		},
		{
			Name:    "testsync_checkpoints_waiting",
			Help:    "Checkpoints with a round in progress, holding agents.",
			Samples: []metrics.Sample{{Value: float64(waiting)}},
		},
	}

	for _, g := range gauges {
		if err := metrics.WriteGauge(w, g); err != nil {
			return err
		}
	}

	for _, c := range []*metrics.CounterVec{
		a.Registry.Releases(), a.Metrics.Requests, a.Metrics.Commands,
	} {
		if err := metrics.WriteCounter(w, c); err != nil {
			return err
		}
	}

	return nil
}

// writeJSON writes a JSON response with an explicit content type.
func writeJSON(ctx context.Context, a *app.App, w http.ResponseWriter, code int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		a.Log.ErrorContext(ctx, "failed to encode a response", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)

	if _, err := w.Write(encoded); err != nil {
		a.Log.DebugContext(ctx, "failed to write a response", "error", err)
	}
}
