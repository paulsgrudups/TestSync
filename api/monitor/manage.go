package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"
)

// maxReleaseBodyBytes bounds the release request body. It only ever carries a
// checkpoint identifier and a short reason, so anything larger is a mistake
// or an attempt to make the server read it.
const maxReleaseBodyBytes = 8 << 10

// releaseRequest is the body of a force-release. The identifier travels in the
// body rather than the path because it is arbitrary caller-supplied text: an
// agent is free to name a barrier "checkout/step?2", which no path segment
// survives intact.
type releaseRequest struct {
	Identifier string `json:"identifier"`
	Reason     string `json:"reason"`
}

// releaseResponse reports the round that was ended.
type releaseResponse struct {
	Identifier string `json:"identifier"`
	Reason     string `json:"reason"`
	Generation int    `json:"generation"`
	Released   int    `json:"released"`
	Target     int    `json:"target"`
}

// releaseCheckpointHandler ends the round in progress on one barrier and
// releases the agents waiting on it.
//
// They are released exactly as a round that ended on its own: with the
// existing checkpoint envelope, a shared resume moment, and finished: false,
// because the barrier was not met. An operator unsticking a suite is not the
// same event as the suite synchronizing, and an agent must be able to tell
// them apart.
func (a *API) releaseCheckpointHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	testID, err := runs.GetPathID(w, r, "testID")
	if err != nil {
		return
	}

	req, ok := a.decodeReleaseRequest(ctx, w, r)
	if !ok {
		return
	}

	result, err := a.service.ReleaseCheckpoint(testID, req.Identifier, req.Reason)
	if err != nil {
		a.writeReleaseError(ctx, w, testID, req.Identifier, err)
		return
	}

	a.log.InfoContext(ctx, "operator released a checkpoint round",
		"test_id", testID,
		"checkpoint", result.Identifier,
		"generation", result.Generation,
		"released", result.Released,
		"target", result.Target,
		"reason", result.Reason,
	)

	a.writeJSON(ctx, w, http.StatusOK, releaseResponse{
		Identifier: result.Identifier,
		Reason:     result.Reason,
		Generation: result.Generation,
		Released:   result.Released,
		Target:     result.Target,
	})
}

// disconnectHandler closes one agent's connection and removes it from the run,
// which frees the slot it held in every barrier it had joined.
func (a *API) disconnectHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	testID, err := runs.GetPathID(w, r, "testID")
	if err != nil {
		return
	}

	// The route only matches digits, so this fails only on a number too large
	// to be an ID that was ever minted.
	raw := mux.Vars(r)["connID"]

	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		a.log.DebugContext(ctx, "could not parse the connection ID",
			"test_id", testID, "conn_id", raw, "error", err,
		)
		utils.HTTPErrorReason(
			w, "Could not parse the connection ID", http.StatusBadRequest,
			runs.ErrorReasonInvalidRequest,
		)

		return
	}

	connID := runs.ConnID(parsed)

	if err := a.service.DisconnectAgent(testID, connID); err != nil {
		a.writeDisconnectError(ctx, w, testID, connID, err)
		return
	}

	a.log.InfoContext(ctx, "operator disconnected an agent",
		"test_id", testID, "conn_id", connID,
	)

	w.WriteHeader(http.StatusNoContent)
}

// runDataHandler returns a run's stored payload.
//
// This is the one route in this package that returns stored contents. Every
// other response reports sizes and counts, so that a payload reaches a screen
// only when an operator asked for that payload by name. It is served as opaque
// bytes, never sniffed and never cached: a run holds whatever an agent put in
// it, which may well be HTML.
func (a *API) runDataHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	testID, err := runs.GetPathID(w, r, "testID")
	if err != nil {
		return
	}

	data, err := a.service.ReadTestData(ctx, testID)
	if err != nil {
		if errors.Is(err, runs.ErrTestNotFound) {
			a.log.DebugContext(ctx, "no stored data for the run", "test_id", testID)
			utils.HTTPErrorReason(
				w, "Could not find stored data for this run", http.StatusNotFound,
				runs.ErrorReasonDataNotFound,
			)

			return
		}

		a.log.ErrorContext(ctx, "failed to read stored data",
			"test_id", testID, "error", err,
		)
		utils.HTTPError(
			w, "Could not read run data", http.StatusInternalServerError,
		)

		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	// gosec's taint analysis sees stored data reaching a response body and
	// calls it XSS. It cannot see the three headers set just above, which are
	// what makes this safe: the payload is served as opaque bytes with
	// sniffing disabled, so a browser downloads it rather than rendering it.
	//nolint:gosec // G705: octet-stream + nosniff, so the payload is never rendered.
	if _, err := w.Write(data); err != nil {
		a.log.DebugContext(ctx, "failed to write a stored payload",
			"test_id", testID, "error", err,
		)
	}
}

// decodeReleaseRequest reads and validates a release body, answering the
// client itself and reporting false when it could not.
func (a *API) decodeReleaseRequest(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
) (releaseRequest, bool) {
	var req releaseRequest

	body := http.MaxBytesReader(w, r.Body, maxReleaseBodyBytes)
	defer body.Close()

	if err := json.NewDecoder(body).Decode(&req); err != nil {
		a.log.DebugContext(ctx, "could not decode a release request", "error", err)
		utils.HTTPErrorReason(
			w, "Could not parse the request body", http.StatusBadRequest,
			runs.ErrorReasonInvalidRequest,
		)

		return releaseRequest{}, false
	}

	req.Identifier = strings.TrimSpace(req.Identifier)
	req.Reason = strings.TrimSpace(req.Reason)

	if req.Identifier == "" {
		utils.HTTPErrorReason(
			w, "A checkpoint identifier is required", http.StatusBadRequest,
			runs.ErrorReasonInvalidRequest,
		)

		return releaseRequest{}, false
	}

	if len(req.Reason) > runs.MaxReleaseReasonBytes {
		utils.HTTPErrorReason(
			w, "The release reason is too long", http.StatusBadRequest,
			runs.ErrorReasonInvalidRequest,
		)

		return releaseRequest{}, false
	}

	return req, true
}

// writeReleaseError reports why a round could not be released.
func (a *API) writeReleaseError(
	ctx context.Context, w http.ResponseWriter,
	testID int, identifier string, err error,
) {
	switch {
	case errors.Is(err, runs.ErrRunNotFound):
		a.log.DebugContext(ctx, "release asked for an unknown run", "test_id", testID)
		utils.HTTPErrorReason(
			w, "Could not find test run", http.StatusNotFound,
			runs.ErrorReasonRunNotFound,
		)
	case errors.Is(err, runs.ErrCheckpointNotFound):
		a.log.DebugContext(ctx, "release asked for an unknown checkpoint",
			"test_id", testID, "checkpoint", identifier,
		)
		utils.HTTPErrorReason(
			w, "Could not find checkpoint", http.StatusNotFound,
			runs.ErrorReasonCheckpointNotFound,
		)
	case errors.Is(err, runs.ErrNoRoundInProgress):
		a.log.DebugContext(ctx, "release asked for an idle checkpoint",
			"test_id", testID, "checkpoint", identifier,
		)
		utils.HTTPErrorReason(
			w, "No checkpoint round is in progress", http.StatusConflict,
			runs.ErrorReasonNoRoundInProgress,
		)
	default:
		a.log.ErrorContext(ctx, "failed to release a checkpoint round",
			"test_id", testID, "checkpoint", identifier, "error", err,
		)
		utils.HTTPError(
			w, "Could not release the checkpoint", http.StatusInternalServerError,
		)
	}
}

// writeDisconnectError reports why an agent could not be disconnected.
func (a *API) writeDisconnectError(
	ctx context.Context, w http.ResponseWriter,
	testID int, connID runs.ConnID, err error,
) {
	switch {
	case errors.Is(err, runs.ErrRunNotFound):
		a.log.DebugContext(ctx, "disconnect asked for an unknown run", "test_id", testID)
		utils.HTTPErrorReason(
			w, "Could not find test run", http.StatusNotFound,
			runs.ErrorReasonRunNotFound,
		)
	case errors.Is(err, runs.ErrConnectionNotFound):
		a.log.DebugContext(ctx, "disconnect asked for an unknown connection",
			"test_id", testID, "conn_id", connID,
		)
		utils.HTTPErrorReason(
			w, "Could not find connection", http.StatusNotFound,
			runs.ErrorReasonConnectionNotFound,
		)
	default:
		a.log.ErrorContext(ctx, "failed to disconnect an agent",
			"test_id", testID, "conn_id", connID, "error", err,
		)
		utils.HTTPError(
			w, "Could not disconnect the agent", http.StatusInternalServerError,
		)
	}
}
