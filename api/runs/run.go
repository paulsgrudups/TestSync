package runs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// Test is the coordination state of a single test run: the agents attached to
// it and the barriers they meet at. It deliberately holds no test data. The
// payload used to be cached here as well as in the store, so an update over
// WebSocket wrote both while the janitor reclaimed them independently, and the
// two copies could disagree about what an agent had stored (CODE-3).
//
// Connections are keyed by ConnID rather than held in a slice: an agent that
// disconnects has to be able to give up its slot in the registry and in every
// barrier it joined (CONC-5).
type Test struct {
	Created     time.Time
	connections map[ConnID]*wsutil.Client
	// ordinals numbers the connections of this run from zero, in the order
	// they arrived, so operators have a short handle for an agent. ConnIDs
	// are process-wide and quickly grow large, which reads badly in a UI.
	ordinals    map[ConnID]int
	nextOrdinal int
	checkPoints map[string]*checkpoint
	mu          sync.RWMutex

	// limits is copied from the registry that created this run. It is fixed
	// for the run's lifetime, so enforcing a per-run limit costs no shared
	// state and no lock beyond the run's own (CODE-1).
	limits Limits

	// log already carries this run's test_id, so anything logged about the
	// run or its barriers is attributable without repeating it.
	log *slog.Logger
}

// logger returns the run's logger, or a discarding one for a Test that was
// built outside a registry.
func (t *Test) logger() *slog.Logger {
	if t.log == nil {
		return utils.DiscardLogger()
	}

	return t.log
}

// RegisterTestsRoutes registers all tests routes against the given service.
//
// It has no side effects: the background sweep is owned by a [Janitor] the
// process starts and stops, not by route registration (STAB-5). The validator
// is a parameter rather than a global read at request time, so a route cannot
// be registered before credentials exist and end up open (SEC-1) — there is no
// order in which these routes can be built without one.
func RegisterTestsRoutes(
	r *mux.Router, svc *Service, validator *auth.Validator, logger *slog.Logger,
) {
	subrouter := r.PathPrefix(`/tests/{testID:\d+}`).
		Subrouter().StrictSlash(false)

	subrouter.Use(auth.BasicAuthMiddleware(validator, logger))

	subrouter.HandleFunc(`/`, svc.createHandler).Methods(http.MethodPost)
	subrouter.HandleFunc(``, svc.createHandler).Methods(http.MethodPost)
	subrouter.HandleFunc(`/`, svc.readHandler).Methods(http.MethodGet)
	subrouter.HandleFunc(``, svc.readHandler).Methods(http.MethodGet)
}

func (s *Service) createHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	testID, err := GetPathID(w, r, "testID")
	if err != nil {
		s.log.DebugContext(ctx, "could not parse the test ID", "error", err)
		return
	}

	logger := s.log.With("test_id", testID)

	body, err := s.readBodyData(ctx, w, r.Body)
	if err != nil {
		logger.DebugContext(ctx, "could not read the request body", "error", err)
		return
	}

	if err := s.CreateTestData(ctx, testID, body); err != nil {
		if errors.Is(err, ErrTestExists) {
			utils.HTTPError(
				w, "Provided test already has set data", http.StatusConflict,
			)
			return
		}

		if writeLimitError(ctx, w, logger, err) {
			return
		}

		logger.ErrorContext(ctx, "could not store data", "error", err)
		utils.HTTPError(w, "Could not store data", http.StatusInternalServerError)
		return
	}

	logger.InfoContext(ctx, "stored test data", "bytes", len(body))

	writeResponse(w, body, http.StatusOK)
}

func (s *Service) readHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	testID, err := GetPathID(w, r, "testID")
	if err != nil {
		return
	}

	logger := s.log.With("test_id", testID)

	data, err := s.ReadTestData(ctx, testID)
	if err != nil {
		if errors.Is(err, ErrTestNotFound) {
			logger.DebugContext(ctx, "test data not found")
			utils.HTTPError(w, "Could not find test", http.StatusNotFound)
			return
		}

		logger.ErrorContext(ctx, "could not read data", "error", err)
		utils.HTTPError(w, "Could not read data", http.StatusInternalServerError)
		return
	}

	logger.InfoContext(ctx, "read test data", "bytes", len(data))

	writeResponse(w, data, http.StatusOK)
}

func (s *Service) readBodyData(
	ctx context.Context, w http.ResponseWriter, body io.ReadCloser,
) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	defer body.Close()

	// The body cap is the same limits.max_data_bytes that bounds a stored
	// payload and a WebSocket frame, so a payload is accepted or refused the
	// same way whichever path it arrives on.
	bodyContent, err := io.ReadAll(
		http.MaxBytesReader(w, body, s.registry.Limits().MaxDataBytes),
	)
	if err != nil {
		s.log.DebugContext(ctx, "could not read the request body", "error", err)
		utils.HTTPError(
			w, "Request data too large", http.StatusRequestEntityTooLarge,
		)

		return nil, fmt.Errorf("could not read body: %w", err)
	}

	return bodyContent, nil
}

// GetPathID parses an integer path variable, writing a 400 response itself if
// it cannot, and returning the error as well so the caller knows to stop.
//
// Doing both is a wart: it is why this was once called after a WebSocket
// connection had been hijacked, sending an HTTP error to a socket that was no
// longer speaking HTTP (CONC-11). Splitting it into a pure parser is tracked
// as CODE-2. Until then, call it only from an HTTP handler that has not
// written a response yet.
func GetPathID(
	w http.ResponseWriter, r *http.Request, field string,
) (int, error) {
	id, err := strconv.Atoi(mux.Vars(r)[field])
	if err != nil {
		utils.HTTPError(
			w,
			fmt.Sprintf(
				"Unable to parse %s as int: invalid integer %q",
				field, mux.Vars(r)[field],
			),
			http.StatusBadRequest,
		)

		return 0, fmt.Errorf("could not parse %s as an integer: %w", field, err)
	}

	return id, nil
}

// writeLimitError reports a resource limit to the client and returns whether
// it did. A limit is an operational condition the caller can act on, so it
// gets its own status rather than a generic 500 (STAB-3, SEC-8).
func writeLimitError(
	ctx context.Context, w http.ResponseWriter, logger *slog.Logger, err error,
) bool {
	switch {
	case errors.Is(err, ErrDataTooLarge):
		logger.WarnContext(ctx, "rejected an oversized payload", "error", err)
		utils.HTTPError(
			w, "Request data too large", http.StatusRequestEntityTooLarge,
		)

		return true
	case errors.Is(err, ErrTestLimitReached):
		logger.WarnContext(ctx, "rejected a new test run", "error", err)
		utils.HTTPError(
			w,
			"Too many active test runs; retry once running suites finish",
			http.StatusServiceUnavailable,
		)

		return true
	default:
		return false
	}
}

func writeResponse(w http.ResponseWriter, resp []byte, code int) {
	w.WriteHeader(code)
	w.Write(resp) //nolint: gosec, errcheck
}
