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
	"github.com/gorilla/websocket"
	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/pkg/errors"
)

var (
	// SyncClient defines sync client credentials.
	SyncClient utils.BasicCredentials
)

const (
	cleanupInterval = 12 * time.Hour
	cleanupAge      = 12 * time.Hour
	maxBodyBytes    = 10 << 20
)

// Test describes a single test instance with it's saved data and connections.
type Test struct {
	Created     time.Time
	Data        []byte
	Connections []*websocket.Conn
	CheckPoints map[string]*Checkpoint
	ForceEnd    bool
	mu          sync.RWMutex
}

// RegisterTestsRoutes registers all tests routes.
func RegisterTestsRoutes(r *mux.Router) {
	subrouter := r.PathPrefix(`/tests/{testID:\d+}`).
		Subrouter().StrictSlash(false)

	subrouter.Use(auth.BasicAuthMiddleware(auth.NewValidator(SyncClient)))

	startCleanupTicker()

	subrouter.HandleFunc(`/`, createHandler).Methods(http.MethodPost)
	subrouter.HandleFunc(``, createHandler).Methods(http.MethodPost)
	subrouter.HandleFunc(`/`, readHandler).Methods(http.MethodGet)
	subrouter.HandleFunc(``, readHandler).Methods(http.MethodGet)
}

func createHandler(w http.ResponseWriter, r *http.Request) {
	testID, err := GetPathID(w, r, "testID")
	if err != nil {
		log.Errorf("Could not get test ID: %s", err.Error())
		return
	}

	logger := log.WithField("test_id", testID)

	body, err := readBodyData(w, r.Body)
	if err != nil {
		logger.Errorf("Could not read body data: %s", err.Error())
		return
	}

	if err := DefaultService.CreateTestData(testID, body); err != nil {
		if stderrors.Is(err, ErrTestExists) {
			utils.HTTPError(
				w, "Provided test already has set data", http.StatusConflict,
			)
			return
		}

		logger.Errorf("Could not store data: %s", err.Error())
		utils.HTTPError(w, "Could not store data", http.StatusInternalServerError)
		return
	}

	logger.Info("Set data for test")

	writeResponse(w, body, http.StatusOK)
}

func readHandler(w http.ResponseWriter, r *http.Request) {
	testID, err := GetPathID(w, r, "testID")
	if err != nil {
		return
	}

	logger := log.WithField("test_id", testID)

	data, err := DefaultService.ReadTestData(testID)
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

func readBodyData(w http.ResponseWriter, body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	defer body.Close() //nolint:errcheck

	bodyContent, err := io.ReadAll(http.MaxBytesReader(w, body, maxBodyBytes))
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

func writeResponse(w http.ResponseWriter, resp []byte, code int) {
	w.WriteHeader(code)
	w.Write(resp) // nolint: gosec, errcheck
}

func startCleanupTicker() {
	ticker := time.NewTicker(cleanupInterval)

	go func() {
		for range ticker.C {
			deleteLimit := time.Now().Add(-cleanupAge)

			RangeTests(func(testID int, t *Test) {
				if t.Created.Before(deleteLimit) {
					log.WithField("test_id", testID).Info("Deleting expired test")
					DeleteTest(testID)
				}
			})

			if err := DeleteDataOlderThan(deleteLimit); err != nil {
				log.Errorf("Failed to delete old data: %s", err.Error())
			}
		}
	}()
}
