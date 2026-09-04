package auth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// credentialComparison matches a direct == / != comparison against
	// credential material. Credentials may only be compared through hashed,
	// constant-time comparison (SEC-2), so any hit here is a regression.
	credentialComparison = regexp.MustCompile(
		`\b(creds|SyncClient)\.(Username|Password)\s*(==|!=)` +
			`|(==|!=)\s*(\w+\.)*(creds|SyncClient)\.(Username|Password)`,
	)

	// presenceCheck matches a comparison against the empty string. Those are
	// configuration presence checks -- "was a credential supplied at all" --
	// which compare no secret material and leak nothing.
	presenceCheck = regexp.MustCompile(
		`(\w+\.)*(creds|SyncClient)\.(Username|Password)\s*(==|!=)\s*""`,
	)
)

// TestNoPlaintextCredentialComparison walks the repository sources and fails on
// any surviving plaintext comparison of credential material (SEC-2), which is
// the finding's stated done-condition.
func TestNoPlaintextCredentialComparison(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "docs":
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		content, readErr := os.ReadFile(path) //nolint:gosec // scans this repository's own sources
		if readErr != nil {
			return readErr
		}

		for i, line := range strings.Split(string(content), "\n") {
			if credentialComparison.MatchString(presenceCheck.ReplaceAllString(line, "")) {
				t.Errorf(
					"%s:%d compares credential material directly (SEC-2): %s",
					path, i+1, strings.TrimSpace(line),
				)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("failed to scan sources: %v", err)
	}
}
