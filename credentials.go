package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// Publishing credentials live in the OS keychain. Some Linux desktops run
// without a secret service, so a 0600 file under the app config directory is
// used as a fallback. The fallback is reported to the UI so the user knows.
const credentialService = "post-planner-publishing"

const (
	credentialBackendKeychain = "keychain"
	credentialBackendFile     = "file"
)

// Publication is a blog owned by the connected account.
type Publication struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// ProviderStatus is the redacted connection state shown in the publish dialog.
type ProviderStatus struct {
	Platform        string        `json:"platform"`
	Connected       bool          `json:"connected"`
	Account         string        `json:"account,omitempty"`
	PublicationID   string        `json:"publicationId,omitempty"`
	PublicationName string        `json:"publicationName,omitempty"`
	Publications    []Publication `json:"publications,omitempty"`
	Storage         string        `json:"storage,omitempty"`
	Warning         string        `json:"warning,omitempty"`
}

// providerCredentials is the blob stored in the keychain, never sent to the
// frontend. Account metadata is cached alongside the secret so Connections can
// report status without a network round-trip.
type providerCredentials struct {
	Secret          string        `json:"secret"`
	Account         string        `json:"account"`
	PublicationID   string        `json:"publicationId,omitempty"`
	PublicationName string        `json:"publicationName,omitempty"`
	Publications    []Publication `json:"publications,omitempty"`
}

// credentialStore reads and writes provider blobs, preferring the OS keychain.
type credentialStore struct {
	mu  sync.Mutex
	dir string
	// forceFile skips the keychain. Tests use it so they never touch the
	// developer's real keyring.
	forceFile bool
}

func newCredentialStore() *credentialStore {
	dir := ""
	if configDir, err := os.UserConfigDir(); err == nil {
		dir = filepath.Join(configDir, "post-planner", "publishing")
	}
	return &credentialStore{dir: dir}
}

func (s *credentialStore) get(platform string) (providerCredentials, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.forceFile {
		creds, found, err := s.readFile(platform)
		return creds, credentialBackendFile, found, err
	}

	if raw, err := keyring.Get(credentialService, platform); err == nil {
		creds, decodeErr := decodeCredentials(raw)
		if decodeErr != nil {
			return providerCredentials{}, credentialBackendKeychain, false,
				fmt.Errorf("read stored %s credentials: %w", platform, decodeErr)
		}
		return creds, credentialBackendKeychain, true, nil
	}

	creds, found, err := s.readFile(platform)
	return creds, credentialBackendFile, found, err
}

func (s *credentialStore) save(platform string, creds providerCredentials) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.Marshal(creds)
	if err != nil {
		return "", fmt.Errorf("encode credentials: %w", err)
	}

	if s.forceFile {
		if err := s.writeFile(platform, raw); err != nil {
			return "", fmt.Errorf("save credentials: %w", err)
		}
		return credentialBackendFile, nil
	}

	if err := keyring.Set(credentialService, platform, string(raw)); err == nil {
		_ = s.removeFile(platform)
		return credentialBackendKeychain, nil
	}

	if err := s.writeFile(platform, raw); err != nil {
		return "", fmt.Errorf("the OS keychain is unavailable and credentials could not be saved: %w", err)
	}
	return credentialBackendFile, nil
}

func (s *credentialStore) delete(platform string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.forceFile {
		_ = keyring.Delete(credentialService, platform)
	}
	return s.removeFile(platform)
}

func (s *credentialStore) path(platform string) string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, platform+".json")
}

func (s *credentialStore) readFile(platform string) (providerCredentials, bool, error) {
	path := s.path(platform)
	if path == "" {
		return providerCredentials{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return providerCredentials{}, false, nil
	}
	if err != nil {
		return providerCredentials{}, false, fmt.Errorf("read credentials: %w", err)
	}
	creds, err := decodeCredentials(string(raw))
	if err != nil {
		return providerCredentials{}, false, fmt.Errorf("read credentials: %w", err)
	}
	return creds, true, nil
}

func (s *credentialStore) writeFile(platform string, raw []byte) error {
	path := s.path(platform)
	if path == "" {
		return errors.New("no config directory available")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	if err := writeFileAtomic(path, 0o600, func(w io.Writer) error {
		_, err := w.Write(raw)
		return err
	}); err != nil {
		return err
	}
	return nil
}

func (s *credentialStore) removeFile(platform string) error {
	path := s.path(platform)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credentials: %w", err)
	}
	return nil
}

func decodeCredentials(raw string) (providerCredentials, error) {
	var creds providerCredentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return providerCredentials{}, err
	}
	if creds.Secret == "" {
		return providerCredentials{}, errors.New("stored credentials are empty")
	}
	return creds, nil
}

func credentialWarning(backend string) string {
	if backend == credentialBackendFile {
		return "No OS keychain available; credentials are stored in a 0600 file."
	}
	return ""
}
