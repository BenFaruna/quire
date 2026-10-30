package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Each published note keeps a sidecar named <id>.publish.json next to it. The
// sidecar records where the note was published, so the UI can offer Update,
// View and Unlink without ever asking the user to remember a remote ID.
const (
	publishStateVersion = 1

	publishStatusDraft     = "draft"
	publishStatusPublished = "published"
)

// PublishTarget is the tracking record for one platform.
type PublishTarget struct {
	RemoteID      string `json:"remoteId"`
	URL           string `json:"url,omitempty"`
	Status        string `json:"status"`
	CanonicalURL  string `json:"canonicalUrl,omitempty"`
	PublicationID string `json:"publicationId,omitempty"`
	PublishedAt   int64  `json:"publishedAt,omitempty"`
	SyncedAt      int64  `json:"syncedAt"`
	ContentHash   string `json:"contentHash"`
}

// PublishState is the whole sidecar document.
type PublishState struct {
	Version int                      `json:"version"`
	NoteID  string                   `json:"noteId"`
	Updated int64                    `json:"updated"`
	Targets map[string]PublishTarget `json:"targets"`
}

func emptyPublishState(noteID string) PublishState {
	return PublishState{
		Version: publishStateVersion,
		NoteID:  noteID,
		Targets: map[string]PublishTarget{},
	}
}

func publishStatePath(root, noteID string) string {
	return filepath.Join(root, noteID+".publish.json")
}

// readPublishState loads a sidecar. A missing file is not an error. A corrupt
// file is renamed to .bak and treated as if the note had never been published.
func readPublishState(root, noteID string) (PublishState, bool, error) {
	if err := validateID(noteID); err != nil {
		return PublishState{}, false, err
	}

	state := emptyPublishState(noteID)
	path := publishStatePath(root, noteID)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil {
		return state, false, fmt.Errorf("read publish state: %w", err)
	}

	if err := json.Unmarshal(raw, &state); err != nil {
		_ = os.Rename(path, path+".bak")
		return emptyPublishState(noteID), false, nil
	}
	if state.Targets == nil {
		state.Targets = map[string]PublishTarget{}
	}
	state.NoteID = noteID
	if state.Version == 0 {
		state.Version = publishStateVersion
	}
	return state, true, nil
}

func writePublishState(root string, state PublishState) error {
	if err := validateID(state.NoteID); err != nil {
		return err
	}
	state.Version = publishStateVersion
	state.Updated = time.Now().UnixMilli()

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode publish state: %w", err)
	}
	raw = append(raw, '\n')

	path := publishStatePath(root, state.NoteID)
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, err := w.Write(raw)
		return err
	}); err != nil {
		return fmt.Errorf("write publish state: %w", err)
	}
	return nil
}

func deletePublishState(root, noteID string) error {
	if err := validateID(noteID); err != nil {
		return err
	}
	if err := os.Remove(publishStatePath(root, noteID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove publish state: %w", err)
	}
	return nil
}

// publishContentHash fingerprints the note at publish time. A mismatch later
// means the note changed after it was published.
func publishContentHash(title, body string) string {
	sum := sha256.Sum256([]byte(composeDocument(title, body)))
	return "sha256:" + hex.EncodeToString(sum[:])
}
