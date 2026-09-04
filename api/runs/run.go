package runs

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	stderrors "errors"

	log "github.com/sirupsen/logrus"

	"github.com/gorilla/mux"
	"github.com/pkg/errors"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// Test describes a single test instance with it's saved data and connections.
//
// Connections are keyed by ConnID rather than held in a slice: an agent that
// disconnects has to be able to give up its slot in the registry and in every
// barrier it joined (CONC-5).
type Test struct {
	Created     time.Time
	Data        []byte
	ForceEnd    bool
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
}

// RegisterTestsRoutes registers all tests routes against the given service.
//
// It has no side effects: the background sweep is owned by a [Janitor] the
// process starts and stops, not by route registration (STAB-5). The validator
// is a parameter rather than a global read at request time, so a route cannot
// be registered before credentials exist and end up open (SEC-1) — there is no
// order in which these routes can be built without one.
func RegisterTestsRoutes(r *mux.Router, svc *Service, validator *auth.Validator) {
	subrouter := r.PathPrefix(`/tests/{testID:\d+}`).
		Subrouter().StrictSlash(false)

	subrouter.Use(auth.BasicAuthMiddleware(validator))

	subrouter.HandleFunc(`/`, svc.createHandler).Methods(http.MethodPost)
	subrouter.HandleFunc(``, svc.createHandler).Methods(http.MethodPost)
	subrouter.HandleFunc(`/`, svc.readHandler).Methods(http.MethodGet)
	subrouter.HandleFunc(``, svc.readHandler).Methods(http.MethodGet)
}

func (s *Service) createHandler(w http.ResponseWriter, r *http.Request) {
	testID, err := GetPathID(w, r, "testID")
	if err != nil {
		log.Errorf("Could not get test ID: %s", err.Error())
		return
	}

	logger := log.WithField("test_id", testID)

	body, err := s.readBodyData(w, r.Body)
	if err != nil {
		logger.Errorf("Could not read body data: %s", err.Error())
		return
	}

	if err := s.CreateTestData(testID, body); err != nil {
		if stderrors.Is(err, ErrTestExists) {
			utils.HTTPError(
				w, "Provided test already has set data", http.StatusConflict,
			)
			return
		}

		if writeLimitError(w, logger, err) {
			return
		}

		logger.Errorf("Could not store data: %s", err.Error())
		utils.HTTPError(w, "Could not store data", http.StatusInternalServerError)
		return
	}

	logger.Info("Set data for test")

	writeResponse(w, body, http.StatusOK)
}

func (s *Service) readHandler(w http.ResponseWriter, r *http.Request) {
	testID, err := GetPathID(w, r, "testID")
	if err != nil {
		return
	}

	logger := log.WithField("test_id", testID)

	data, err := s.ReadTestData(testID)
	if err != nil {
		if stderrors.Is(err, ErrTestNotFound) {
			logger.Debug("Data not found")
			utils.HTTPError(w, "Could not find test", http.StatusNotFound)
			return
		}

		logger.Errorf("Could not read data: %s", err.Error())
		utils.HTTPError(w, "Could not read data", http.StatusInternalServerError)
		return
	}

	logger.Info("Reading data for test")

	writeResponse(w, data, http.StatusOK)
}

func (s *Service) readBodyData(
	w http.ResponseWriter, body io.ReadCloser,
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
		log.Debugf("Could not read body: %s", err.Error())
		utils.HTTPError(
			w, "Request data too large", http.StatusRequestEntityTooLarge,
		)

		return nil, errors.Wrap(err, "could not read body")
	}

	return bodyContent, nil
}

// GetPathID ...
func GetPathID(
	w http.ResponseWriter, r *http.Request, field string,
) (int, error) {
	id, err := strconv.Atoi(mux.Vars(r)[field])
	if err != nil {
		log.Debugf(
			"Unable to parse %s as int: invalid integer %q",
			field, mux.Vars(r)[field],
		)
		utils.HTTPError(
			w,
			fmt.Sprintf(
				"Unable to parse %s as int: invalid integer %q",
				field, mux.Vars(r)[field],
			),
			http.StatusBadRequest,
		)

		return 0, errors.Wrap(err, "could not parse integer value")
	}

	return id, nil
}

// writeLimitError reports a resource limit to the client and returns whether
// it did. A limit is an operational condition the caller can act on, so it
// gets its own status rather than a generic 500 (STAB-3, SEC-8).
func writeLimitError(w http.ResponseWriter, logger *log.Entry, err error) bool {
	switch {
	case stderrors.Is(err, ErrDataTooLarge):
		logger.Warnf("Rejected oversized payload: %s", err.Error())
		utils.HTTPError(
			w, "Request data too large", http.StatusRequestEntityTooLarge,
		)

		return true
	case stderrors.Is(err, ErrTestLimitReached):
		logger.Warnf("Rejected new test run: %s", err.Error())
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
