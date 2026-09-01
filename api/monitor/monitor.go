// Package monitor serves the read-only monitoring API and the operator UI.
//
// Everything in here observes: no route mutates a run, releases a barrier or
// touches stored test data, and no response ever contains a stored payload.
// Runs can hold anything an agent put in them, so the monitor reports sizes
// and counts instead.
package monitor

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"

	log "github.com/sirupsen/logrus"
)

// APIPrefix is the namespace of the monitoring API. It is versioned and kept
// away from /tests/{testID}, so the agent-facing surface never collides with
// it.
const APIPrefix = "/api/v1"

// UIPrefix is where the operator page is served from.
const UIPrefix = "/ui"

// runSummary is one row of the run list.
type runSummary struct {
	TestID                int       `json:"test_id"`
	Created               time.Time `json:"created"`
	AgeSeconds            float64   `json:"age_seconds"`
	ConnectionCount       int       `json:"connection_count"`
	ActiveConnectionCount int       `json:"active_connection_count"`
	CheckpointCount       int       `json:"checkpoint_count"`
	WaitingCheckpoints    int       `json:"waiting_checkpoint_count"`
	Waiting               bool      `json:"waiting"`
	HasData               bool      `json:"has_data"`
	DataSizeBytes         int       `json:"data_size_bytes"`
	ForceEnd              bool      `json:"force_end"`
}

// runListResponse is the body of GET /api/v1/runs.
type runListResponse struct {
	ServerTime time.Time    `json:"server_time"`
	RunCount   int          `json:"run_count"`
	Runs       []runSummary `json:"runs"`
}

// connectionView describes one agent connection of a run.
type connectionView struct {
	Index  int         `json:"index"`
	ConnID runs.ConnID `json:"conn_id"`
	Active bool        `json:"active"`
}

// checkpointView describes one checkpoint barrier of a run.
type checkpointView struct {
	Identifier      string `json:"identifier"`
	Generation      int    `json:"generation"`
	RoundsCompleted int    `json:"rounds_completed"`
	TargetCount     int    `json:"target_count"`
	JoinedCount     int    `json:"joined_count"`
	Waiting         bool   `json:"waiting"`
	Members         []int  `json:"members"`
}

// runDetailResponse is the body of GET /api/v1/runs/{testID}.
type runDetailResponse struct {
	ServerTime  time.Time        `json:"server_time"`
	Run         runSummary       `json:"run"`
	Connections []connectionView `json:"connections"`
	Checkpoints []checkpointView `json:"checkpoints"`
}

// RegisterRoutes registers the monitoring API and the UI page.
//
// Both sit behind the one shared validator, resolved per request, so they are
// authenticated exactly like every other route and cannot be registered into
// an open state (SEC-1).
func RegisterRoutes(r *mux.Router) {
	apiRouter := r.PathPrefix(APIPrefix).Subrouter().StrictSlash(false)
	apiRouter.Use(challengeUnauthorized, auth.SharedMiddleware())

	apiRouter.HandleFunc("/runs", listRunsHandler).Methods(http.MethodGet)
	apiRouter.HandleFunc(`/runs/{testID:\d+}`, runDetailHandler).
		Methods(http.MethodGet)

	registerUIRoutes(r)
}

// listRunsHandler answers with every known run and enough state to tell at a
// glance which of them is stuck on a barrier.
func listRunsHandler(w http.ResponseWriter, _ *http.Request) {
	states := runs.AllTestStates()

	now := time.Now().UTC()
	body := runListResponse{
		ServerTime: now,
		RunCount:   len(states),
		Runs:       make([]runSummary, 0, len(states)),
	}

	for _, state := range states {
		body.Runs = append(body.Runs, summarize(state, now))
	}

	writeJSON(w, http.StatusOK, body)
}

// runDetailHandler answers with the agents and checkpoints of a single run.
func runDetailHandler(w http.ResponseWriter, r *http.Request) {
	testID, err := runs.GetPathID(w, r, "testID")
	if err != nil {
		return
	}

	test, ok := runs.GetTest(testID)
	if !ok {
		utils.HTTPError(w, "Could not find test run", http.StatusNotFound)
		return
	}

	state := test.State(testID)

	now := time.Now().UTC()
	body := runDetailResponse{
		ServerTime:  now,
		Run:         summarize(state, now),
		Connections: make([]connectionView, 0, len(state.Connections)),
		Checkpoints: make([]checkpointView, 0, len(state.Checkpoints)),
	}

	for _, conn := range state.Connections {
		body.Connections = append(body.Connections, connectionView{
			Index:  conn.Ordinal,
			ConnID: conn.ID,
			Active: conn.Active,
		})
	}

	for _, cp := range state.Checkpoints {
		body.Checkpoints = append(body.Checkpoints, checkpointView{
			Identifier:      cp.Identifier,
			Generation:      cp.Generation,
			RoundsCompleted: cp.RoundsCompleted,
			TargetCount:     cp.TargetCount,
			JoinedCount:     len(cp.Members),
			Waiting:         cp.Waiting,
			Members:         cp.Members,
		})
	}

	writeJSON(w, http.StatusOK, body)
}

// summarize reduces a run snapshot to the counters the list view needs. It
// deliberately drops the stored data and keeps only its size.
func summarize(state runs.TestState, now time.Time) runSummary {
	summary := runSummary{
		TestID:                state.TestID,
		Created:               state.Created.UTC(),
		AgeSeconds:            now.Sub(state.Created).Seconds(),
		ConnectionCount:       len(state.Connections),
		ActiveConnectionCount: 0,
		CheckpointCount:       len(state.Checkpoints),
		WaitingCheckpoints:    0,
		Waiting:               false,
		HasData:               state.DataSize > 0,
		DataSizeBytes:         state.DataSize,
		ForceEnd:              state.ForceEnd,
	}

	for _, conn := range state.Connections {
		if conn.Active {
			summary.ActiveConnectionCount++
		}
	}

	for _, cp := range state.Checkpoints {
		if cp.Waiting {
			summary.WaitingCheckpoints++
		}
	}

	summary.Waiting = summary.WaitingCheckpoints > 0

	return summary
}

// writeJSON encodes body as the response. Responses are never cached: the page
// polls this endpoint and a stale answer would read as a stalled run.
func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Debugf("failed to write monitoring response: %v", err)
	}
}
