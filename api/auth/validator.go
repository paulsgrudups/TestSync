package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/paulsgrudups/testsync/utils"
)

var (
	// ErrNoCredentials is returned when authentication is required but the
	// configured credentials are empty. Empty credentials used to authenticate
	// every caller (SEC-1); they are now refused.
	ErrNoCredentials = errors.New("sync_client credentials are empty")

	// ErrUnknownAuthMode is returned for an auth mode the server does not
	// implement.
	ErrUnknownAuthMode = errors.New("unknown auth mode")
)

var (
	sharedMu sync.RWMutex

	// shared is the single validator both the HTTP and the WebSocket server
	// authenticate through, so the two paths cannot drift apart (SEC-1). It is
	// nil until SetShared is called, and a nil validator rejects everything.
	shared *Validator
)

// Validator validates BasicAuth credentials in constant time.
//
// Only hashes of the expected credentials are kept, so a comparison never
// leaks the length of the secret and never short-circuits on the first
// differing byte (SEC-2).
type Validator struct {
	userHash [sha256.Size]byte
	passHash [sha256.Size]byte

	// disabled marks the explicit, operator-requested opt-out. It is only ever
	// set by NewDisabledValidator; the zero value authenticates nothing.
	disabled bool
}

// NewValidator creates a validator for the provided credentials. Empty
// credentials are refused: authentication that silently accepts everyone is
// never what an operator meant to configure (SEC-1).
func NewValidator(creds utils.BasicCredentials) (*Validator, error) {
	if creds.Username == "" || creds.Password == "" {
		return nil, ErrNoCredentials
	}

	return &Validator{
		userHash: sha256.Sum256([]byte(creds.Username)),
		passHash: sha256.Sum256([]byte(creds.Password)),
		disabled: false,
	}, nil
}

// NewDisabledValidator creates a validator that accepts every request. It
// exists only for the explicit opt-out ("auth": {"mode": "none"} or
// --insecure-no-auth) and callers must announce it loudly at startup.
func NewDisabledValidator() *Validator {
	return &Validator{
		userHash: [sha256.Size]byte{},
		passHash: [sha256.Size]byte{},
		disabled: true,
	}
}

// NewFromConfig builds the validator described by the configuration. Mode
// "none" disables authentication; every other mode requires credentials.
func NewFromConfig(
	authConf utils.AuthConfig, creds utils.BasicCredentials,
) (*Validator, error) {
	switch strings.ToLower(strings.TrimSpace(authConf.Mode)) {
	case utils.AuthModeNone:
		return NewDisabledValidator(), nil
	case "", utils.AuthModeBasic:
		return NewValidator(creds)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAuthMode, authConf.Mode)
	}
}

// SetShared installs the process-wide validator used by both the HTTP and the
// WebSocket server.
func SetShared(v *Validator) {
	sharedMu.Lock()
	defer sharedMu.Unlock()

	shared = v
}

// Shared returns the process-wide validator. It is nil until SetShared is
// called, and a nil validator rejects every request.
func Shared() *Validator {
	sharedMu.RLock()
	defer sharedMu.RUnlock()

	return shared
}

// Disabled reports whether authentication was explicitly turned off.
func (v *Validator) Disabled() bool {
	return v != nil && v.disabled
}

// Validate checks provided username and password in constant time. A nil
// validator authenticates nothing: an unconfigured server denies rather than
// opens (SEC-1).
func (v *Validator) Validate(user, pass string) bool {
	if v == nil {
		return false
	}

	if v.disabled {
		return true
	}

	userHash := sha256.Sum256([]byte(user))
	passHash := sha256.Sum256([]byte(pass))

	// Both comparisons always run: && would short-circuit and leak whether the
	// username alone was correct.
	userMatch := subtle.ConstantTimeCompare(userHash[:], v.userHash[:])
	passMatch := subtle.ConstantTimeCompare(passHash[:], v.passHash[:])

	return userMatch&passMatch == 1
}
