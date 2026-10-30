package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PublishRequest is the post content the dialog sends for a blog target.
type PublishRequest struct {
	NoteID    string   `json:"noteId"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Tags      []string `json:"tags"`
	Canonical string   `json:"canonical"`
	// Draft saves without publishing. For an existing published target the
	// provider keeps it published instead of silently unpublishing it.
	Draft bool `json:"draft"`
}

// PublishResult describes a successful create or update.
type PublishResult struct {
	Platform string       `json:"platform"`
	RemoteID string       `json:"remoteId"`
	URL      string       `json:"url"`
	Status   string       `json:"status"`
	Warning  string       `json:"warning,omitempty"`
	State    PublishState `json:"state"`
}

// Connections reports which direct-publishing accounts are configured. Secrets
// never cross this boundary.
func (s *PublishService) Connections() ([]ProviderStatus, error) {
	platforms := []string{platformDevto, platformHashnode}
	statuses := make([]ProviderStatus, 0, len(platforms))
	for _, platform := range platforms {
		creds, backend, found, err := s.creds.get(platform)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, providerStatus(platform, creds, backend, found))
	}
	return statuses, nil
}

// Connect validates a secret with the provider and stores it.
func (s *PublishService) Connect(platform, secret string) (ProviderStatus, error) {
	platform = normalizePublishPlatform(platform)
	provider, ok := s.providers[platform]
	if !ok {
		return ProviderStatus{}, fmt.Errorf("%s does not support direct publishing", platform)
	}

	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ProviderStatus{}, errors.New("enter an API key first")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	account, err := provider.Validate(ctx, secret)
	if err != nil {
		return ProviderStatus{}, err
	}

	creds := providerCredentials{
		Secret:       secret,
		Account:      account.Account,
		Publications: account.Publications,
	}
	if platform == platformHashnode && len(account.Publications) > 0 {
		creds.PublicationID = account.Publications[0].ID
		creds.PublicationName = account.Publications[0].Name
	}

	backend, err := s.creds.save(platform, creds)
	if err != nil {
		return ProviderStatus{}, err
	}
	return providerStatus(platform, creds, backend, true), nil
}

// Disconnect forgets the stored credentials for a platform.
func (s *PublishService) Disconnect(platform string) error {
	platform = normalizePublishPlatform(platform)
	if _, ok := s.providers[platform]; !ok {
		return fmt.Errorf("%s does not support direct publishing", platform)
	}
	return s.creds.delete(platform)
}

// SetPublication chooses which publication Hashnode posts to.
func (s *PublishService) SetPublication(platform, publicationID string) (ProviderStatus, error) {
	platform = normalizePublishPlatform(platform)
	if platform != platformHashnode {
		return ProviderStatus{}, errors.New("publication selection is only used by Hashnode")
	}

	creds, backend, found, err := s.creds.get(platform)
	if err != nil {
		return ProviderStatus{}, err
	}
	if !found {
		return ProviderStatus{}, errors.New("connect Hashnode first")
	}

	publicationID = strings.TrimSpace(publicationID)
	var selected *Publication
	for i := range creds.Publications {
		if creds.Publications[i].ID == publicationID {
			selected = &creds.Publications[i]
			break
		}
	}
	if selected == nil {
		return ProviderStatus{}, errors.New("that publication is not available for this account")
	}

	creds.PublicationID = selected.ID
	creds.PublicationName = selected.Name
	backend, err = s.creds.save(platform, creds)
	if err != nil {
		return ProviderStatus{}, err
	}
	return providerStatus(platform, creds, backend, true), nil
}

// PublishState returns the sidecar for a note. A note that was never published
// returns an empty state rather than an error.
func (s *PublishService) PublishState(noteID string) (PublishState, error) {
	root, err := s.notes.publishRoot()
	if err != nil {
		return PublishState{}, err
	}
	state, _, err := readPublishState(root, noteID)
	return state, err
}

// Publish creates a remote draft or post and records it in the sidecar.
func (s *PublishService) Publish(platform string, req PublishRequest) (PublishResult, error) {
	req = normalizePublishRequest(req)
	platform, provider, root, state, err := s.preparePublish(platform, req)
	if err != nil {
		return PublishResult{}, err
	}

	hash := publishContentHash(req.Title, req.Body)
	if target, exists := state.Targets[platform]; exists && target.ContentHash == hash {
		return PublishResult{}, fmt.Errorf("this note is already on %s; use Update instead", platformLabel(platform))
	}

	creds, found, err := s.credentialsFor(platform)
	if err != nil {
		return PublishResult{}, err
	}
	if !found {
		return PublishResult{}, fmt.Errorf("%s is not connected", platformLabel(platform))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	post, err := provider.Publish(ctx, creds.Secret, publishRequest{
		Title:         req.Title,
		Body:          req.Body,
		Tags:          normalizePublishTags(platform, req.Tags),
		Canonical:     normalizePublishLink(req.Canonical),
		Draft:         req.Draft,
		PublicationID: creds.PublicationID,
	})
	if err != nil {
		return PublishResult{}, err
	}

	now := time.Now().UnixMilli()
	target := PublishTarget{
		RemoteID:      post.ID,
		URL:           post.URL,
		Status:        post.Status,
		CanonicalURL:  post.CanonicalURL,
		PublicationID: creds.PublicationID,
		SyncedAt:      now,
		ContentHash:   hash,
	}
	if post.Status == publishStatusPublished {
		target.PublishedAt = now
	}
	state.Targets[platform] = target

	return visibleResult(platform, root, state, post, target, "The post was published, but saving tracking data failed: %s")
}

// UpdatePublished edits an existing remote post or draft.
func (s *PublishService) UpdatePublished(platform string, req PublishRequest) (PublishResult, error) {
	req = normalizePublishRequest(req)
	platform, provider, root, state, err := s.preparePublish(platform, req)
	if err != nil {
		return PublishResult{}, err
	}

	target, exists := state.Targets[platform]
	if !exists || target.RemoteID == "" {
		return PublishResult{}, fmt.Errorf("this note has not been published to %s yet", platformLabel(platform))
	}

	creds, found, err := s.credentialsFor(platform)
	if err != nil {
		return PublishResult{}, err
	}
	if !found {
		return PublishResult{}, fmt.Errorf("%s is not connected", platformLabel(platform))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	post, err := provider.Update(ctx, creds.Secret, target.RemoteID, target.Status, publishRequest{
		Title:         req.Title,
		Body:          req.Body,
		Tags:          normalizePublishTags(platform, req.Tags),
		Canonical:     normalizePublishLink(req.Canonical),
		Draft:         req.Draft,
		PublicationID: creds.PublicationID,
	})
	if err != nil {
		return PublishResult{}, err
	}

	now := time.Now().UnixMilli()
	target.RemoteID = firstNonEmpty(post.ID, target.RemoteID)
	if post.URL != "" {
		target.URL = post.URL
	}
	if post.Status != "" {
		target.Status = post.Status
	}
	if post.CanonicalURL != "" {
		target.CanonicalURL = post.CanonicalURL
	}
	if target.Status == publishStatusPublished && target.PublishedAt == 0 {
		target.PublishedAt = now
	}
	target.SyncedAt = now
	target.ContentHash = publishContentHash(req.Title, req.Body)
	state.Targets[platform] = target

	return visibleResult(platform, root, state, post, target, "The post was updated, but saving tracking data failed: %s")
}

// Unlink forgets a platform for a note without touching the remote post.
func (s *PublishService) Unlink(platform, noteID string) (PublishState, error) {
	platform = normalizePublishPlatform(platform)
	if _, ok := s.providers[platform]; !ok {
		return PublishState{}, fmt.Errorf("%s does not support direct publishing", platform)
	}

	root, err := s.notes.publishRoot()
	if err != nil {
		return PublishState{}, err
	}

	state, found, err := readPublishState(root, noteID)
	if err != nil {
		return PublishState{}, err
	}
	if !found {
		return state, nil
	}

	delete(state.Targets, platform)
	if len(state.Targets) == 0 {
		if err := deletePublishState(root, noteID); err != nil {
			return emptyPublishState(noteID), err
		}
		return emptyPublishState(noteID), nil
	}
	if err := writePublishState(root, state); err != nil {
		return state, err
	}
	return state, nil
}

// --- helpers ---------------------------------------------------------------

func normalizePublishRequest(req PublishRequest) PublishRequest {
	req.NoteID = strings.TrimSpace(req.NoteID)
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		req.Title = "Untitled note"
	}
	req.Body = strings.ReplaceAll(req.Body, "\r\n", "\n")
	req.Canonical = strings.TrimSpace(req.Canonical)
	return req
}

func (s *PublishService) preparePublish(platform string, req PublishRequest) (string, publishProvider, string, PublishState, error) {
	platform = normalizePublishPlatform(platform)
	provider, ok := s.providers[platform]
	if !ok {
		return "", nil, "", PublishState{}, fmt.Errorf("%s does not support direct publishing", platform)
	}
	if strings.TrimSpace(req.NoteID) == "" {
		return "", nil, "", PublishState{}, errors.New("no note selected")
	}
	if strings.TrimSpace(req.Body) == "" {
		return "", nil, "", PublishState{}, errors.New("this note has no body text to publish")
	}

	root, err := s.notes.publishRoot()
	if err != nil {
		return "", nil, "", PublishState{}, err
	}
	state, _, err := readPublishState(root, req.NoteID)
	if err != nil {
		return "", nil, "", PublishState{}, err
	}
	return platform, provider, root, state, nil
}

func (s *PublishService) credentialsFor(platform string) (providerCredentials, bool, error) {
	creds, _, found, err := s.creds.get(platform)
	if err != nil {
		return providerCredentials{}, false, err
	}
	if found && strings.TrimSpace(creds.Secret) == "" {
		return providerCredentials{}, false, nil
	}
	return creds, found, nil
}

func visibleResult(platform, root string, state PublishState, post remotePost, target PublishTarget, warningTemplate string) (PublishResult, error) {
	warning := ""
	state.Updated = time.Now().UnixMilli()
	if err := writePublishState(root, state); err != nil {
		warning = fmt.Sprintf(warningTemplate, err)
	}

	result := PublishResult{
		Platform: platform,
		RemoteID: target.RemoteID,
		URL:      target.URL,
		Status:   target.Status,
		Warning:  warning,
		State:    state,
	}
	if post.URL != "" {
		result.URL = post.URL
	}
	if post.Status != "" {
		result.Status = post.Status
	}
	return result, nil
}

func providerStatus(platform string, creds providerCredentials, backend string, connected bool) ProviderStatus {
	status := ProviderStatus{
		Platform:  platform,
		Connected: connected,
	}
	if !connected {
		return status
	}
	status.Account = creds.Account
	status.PublicationID = creds.PublicationID
	status.PublicationName = creds.PublicationName
	status.Publications = creds.Publications
	status.Storage = backend
	status.Warning = credentialWarning(backend)
	return status
}

func platformLabel(platform string) string {
	switch platform {
	case platformDevto:
		return "dev.to"
	case platformHashnode:
		return "Hashnode"
	default:
		return platform
	}
}
