package auth

import "github.com/paulsgrudups/testsync/utils"

// Validator validates BasicAuth credentials.
type Validator struct {
	creds utils.BasicCredentials
}

// NewValidator creates a validator with provided credentials.
func NewValidator(creds utils.BasicCredentials) *Validator {
	return &Validator{creds: creds}
}

// Validate checks provided username and password.
func (v *Validator) Validate(user, pass string) bool {
	if v.creds.Username == "" && v.creds.Password == "" {
		return true
	}

	return user == v.creds.Username && pass == v.creds.Password
}
