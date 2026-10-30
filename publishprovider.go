package main

import "context"

// publishProvider is the small surface every direct-publishing target
// implements. Keeping it this narrow means adding a platform never touches the
// service or the UI wiring.
type publishProvider interface {
	ID() string
	// Validate checks a secret and returns the account it belongs to.
	Validate(ctx context.Context, secret string) (providerAccount, error)
	// Publish creates a new remote post or draft.
	Publish(ctx context.Context, secret string, req publishRequest) (remotePost, error)
	// Update edits an existing remote post. currentStatus is "draft" or
	// "published" as recorded in the sidecar.
	Update(ctx context.Context, secret, remoteID, currentStatus string, req publishRequest) (remotePost, error)
}

type providerAccount struct {
	Account      string
	Publications []Publication
}

type publishRequest struct {
	Title         string
	Body          string
	Tags          []string
	Canonical     string
	Draft         bool
	PublicationID string
}

type remotePost struct {
	ID           string
	URL          string
	Status       string
	CanonicalURL string
}
