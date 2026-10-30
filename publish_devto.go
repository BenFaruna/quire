package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const devtoAPIBase = "https://dev.to"

// devtoProvider talks to the official dev.to REST API.
type devtoProvider struct {
	client  *http.Client
	baseURL string
}

func newDevtoProvider(client *http.Client) *devtoProvider {
	return &devtoProvider{client: client, baseURL: devtoAPIBase}
}

func (p *devtoProvider) ID() string { return platformDevto }

func (p *devtoProvider) Validate(ctx context.Context, secret string) (providerAccount, error) {
	var user struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := p.do(ctx, http.MethodGet, "/api/users/me", secret, nil, &user); err != nil {
		return providerAccount{}, err
	}
	account := user.Username
	if account == "" {
		account = user.Name
	}
	if account == "" {
		account = "dev.to account"
	}
	return providerAccount{Account: account}, nil
}

func (p *devtoProvider) Publish(ctx context.Context, secret string, req publishRequest) (remotePost, error) {
	var response devtoArticleResponse
	if err := p.do(ctx, http.MethodPost, "/api/articles", secret, map[string]any{
		"article": p.articlePayload(req, req.Draft),
	}, &response); err != nil {
		return remotePost{}, err
	}
	return response.remotePost(), nil
}

func (p *devtoProvider) Update(ctx context.Context, secret, remoteID, currentStatus string, req publishRequest) (remotePost, error) {
	if strings.TrimSpace(remoteID) == "" {
		return remotePost{}, fmt.Errorf("dev.to article id is missing")
	}

	// An update must not accidentally unpublish a live post. Staying published
	// is only relaxed when the post is still a draft.
	draft := req.Draft && currentStatus != publishStatusPublished

	var response devtoArticleResponse
	if err := p.do(ctx, http.MethodPut, "/api/articles/"+remoteID, secret, map[string]any{
		"article": p.articlePayload(req, draft),
	}, &response); err != nil {
		return remotePost{}, err
	}
	return response.remotePost(), nil
}

func (p *devtoProvider) articlePayload(req publishRequest, draft bool) map[string]any {
	article := map[string]any{
		"title":         req.Title,
		"body_markdown": req.Body,
		"published":     !draft,
	}
	if len(req.Tags) > 0 {
		article["tags"] = req.Tags
	}
	if req.Canonical != "" {
		article["canonical_url"] = req.Canonical
	}
	return article
}

func (p *devtoProvider) do(ctx context.Context, method, path, secret string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode dev.to request: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.baseURL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("build dev.to request: %w", err)
	}
	req.Header.Set("api-key", secret)
	req.Header.Set("Accept", "application/vnd.forem.api-v1+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("dev.to request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read dev.to response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return devtoError(resp, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode dev.to response: %w", err)
	}
	return nil
}

func devtoError(resp *http.Response, raw []byte) error {
	var payload struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &payload)

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("dev.to rejected the API key. Reconnect dev.to from the publish dialog.")
	}

	message := strings.TrimSpace(payload.Error)
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("dev.to: %s", message)
}

type devtoArticleResponse struct {
	ID           int    `json:"id"`
	URL          string `json:"url"`
	Slug         string `json:"slug"`
	Published    bool   `json:"published"`
	CanonicalURL string `json:"canonical_url"`
}

func (r devtoArticleResponse) remotePost() remotePost {
	status := publishStatusDraft
	if r.Published {
		status = publishStatusPublished
	}
	url := r.URL
	if url == "" && r.Slug != "" {
		url = "https://dev.to/" + r.Slug
	}
	return remotePost{
		ID:           strconv.Itoa(r.ID),
		URL:          url,
		Status:       status,
		CanonicalURL: r.CanonicalURL,
	}
}
