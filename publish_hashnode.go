package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const hashnodeGraphQLEndpoint = "https://gql-beta.hashnode.com/"

// hashnodeProvider talks to Hashnode's GraphQL API. All API access requires an
// active Pro plan on the publication.
type hashnodeProvider struct {
	client   *http.Client
	endpoint string
}

func newHashnodeProvider(client *http.Client) *hashnodeProvider {
	return &hashnodeProvider{client: client, endpoint: hashnodeGraphQLEndpoint}
}

func (p *hashnodeProvider) ID() string { return platformHashnode }

const hashnodeViewerQuery = `query Viewer {
  me {
    username
    name
    publications(first: 20) {
      edges { node { id title url } }
    }
  }
}`

func (p *hashnodeProvider) Validate(ctx context.Context, secret string) (providerAccount, error) {
	var data struct {
		Me struct {
			Username     string `json:"username"`
			Name         string `json:"name"`
			Publications struct {
				Edges []struct {
					Node struct {
						ID    string `json:"id"`
						Title string `json:"title"`
						URL   string `json:"url"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"publications"`
		} `json:"me"`
	}
	if err := p.gql(ctx, secret, hashnodeViewerQuery, nil, &data); err != nil {
		return providerAccount{}, err
	}

	account := strings.TrimSpace(data.Me.Username)
	if account == "" {
		account = strings.TrimSpace(data.Me.Name)
	}
	if account == "" {
		account = "Hashnode account"
	}

	publications := make([]Publication, 0, len(data.Me.Publications.Edges))
	for _, edge := range data.Me.Publications.Edges {
		node := edge.Node
		if node.ID == "" {
			continue
		}
		publications = append(publications, Publication{
			ID:   node.ID,
			Name: strings.TrimSpace(node.Title),
			URL:  strings.TrimSpace(node.URL),
		})
	}
	return providerAccount{Account: account, Publications: publications}, nil
}

const hashnodeCreateDraftMutation = `mutation CreateDraft($input: CreateDraftInput!) {
  createDraft(input: $input) {
    draft { id slug title }
  }
}`

const hashnodePublishDraftMutation = `mutation PublishDraft($input: PublishDraftInput!) {
  publishDraft(input: $input) {
    post { id slug url }
  }
}`

func (p *hashnodeProvider) Publish(ctx context.Context, secret string, req publishRequest) (remotePost, error) {
	if strings.TrimSpace(req.PublicationID) == "" {
		return remotePost{}, errors.New("choose a Hashnode publication first")
	}

	input := map[string]any{
		"publicationId":   req.PublicationID,
		"title":           req.Title,
		"contentMarkdown": req.Body,
	}
	if tags := hashnodeTags(req.Tags); len(tags) > 0 {
		input["tags"] = tags
	}
	if req.Canonical != "" {
		input["originalArticleURL"] = req.Canonical
	}

	var created struct {
		CreateDraft struct {
			Draft struct {
				ID    string `json:"id"`
				Slug  string `json:"slug"`
				Title string `json:"title"`
			} `json:"draft"`
		} `json:"createDraft"`
	}
	if err := p.gql(ctx, secret, hashnodeCreateDraftMutation, map[string]any{"input": input}, &created); err != nil {
		return remotePost{}, err
	}

	draft := created.CreateDraft.Draft
	if draft.ID == "" {
		return remotePost{}, errors.New("hashnode did not return a draft id")
	}
	if req.Draft {
		return remotePost{
			ID:     draft.ID,
			URL:    "https://hashnode.com/draft/" + draft.ID,
			Status: publishStatusDraft,
		}, nil
	}

	return p.publishDraft(ctx, secret, draft.ID)
}

const hashnodeUpdateDraftMutation = `mutation UpdateDraft($input: UpdateDraftInput!) {
  updateDraft(input: $input) {
    draft { id slug title }
  }
}`

const hashnodeUpdatePostMutation = `mutation UpdatePost($input: UpdatePostInput!) {
  updatePost(input: $input) {
    post { id slug url }
  }
}`

func (p *hashnodeProvider) Update(ctx context.Context, secret, remoteID, currentStatus string, req publishRequest) (remotePost, error) {
	if strings.TrimSpace(remoteID) == "" {
		return remotePost{}, errors.New("hashnode post id is missing")
	}

	if currentStatus == publishStatusPublished {
		input := map[string]any{
			"id":              remoteID,
			"title":           req.Title,
			"contentMarkdown": req.Body,
		}
		if tags := hashnodeTags(req.Tags); len(tags) > 0 {
			input["tags"] = tags
		}
		if req.Canonical != "" {
			input["originalArticleURL"] = req.Canonical
		}

		var updated struct {
			UpdatePost struct {
				Post struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
					URL  string `json:"url"`
				} `json:"post"`
			} `json:"updatePost"`
		}
		if err := p.gql(ctx, secret, hashnodeUpdatePostMutation, map[string]any{"input": input}, &updated); err != nil {
			return remotePost{}, err
		}
		return remotePost{
			ID:     firstNonEmpty(updated.UpdatePost.Post.ID, remoteID),
			URL:    updated.UpdatePost.Post.URL,
			Status: publishStatusPublished,
		}, nil
	}

	input := map[string]any{
		"draftId":         remoteID,
		"title":           req.Title,
		"contentMarkdown": req.Body,
	}
	if tags := hashnodeTags(req.Tags); len(tags) > 0 {
		input["tags"] = tags
	}
	if req.Canonical != "" {
		input["originalArticleURL"] = req.Canonical
	}

	var updated struct {
		UpdateDraft struct {
			Draft struct {
				ID    string `json:"id"`
				Slug  string `json:"slug"`
				Title string `json:"title"`
			} `json:"draft"`
		} `json:"updateDraft"`
	}
	if err := p.gql(ctx, secret, hashnodeUpdateDraftMutation, map[string]any{"input": input}, &updated); err != nil {
		return remotePost{}, err
	}
	if draftID := updated.UpdateDraft.Draft.ID; draftID != "" {
		remoteID = draftID
	}

	if req.Draft {
		return remotePost{
			ID:     remoteID,
			URL:    "https://hashnode.com/draft/" + remoteID,
			Status: publishStatusDraft,
		}, nil
	}

	return p.publishDraft(ctx, secret, remoteID)
}

func (p *hashnodeProvider) publishDraft(ctx context.Context, secret, draftID string) (remotePost, error) {
	var published struct {
		PublishDraft struct {
			Post struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
				URL  string `json:"url"`
			} `json:"post"`
		} `json:"publishDraft"`
	}
	if err := p.gql(ctx, secret, hashnodePublishDraftMutation, map[string]any{
		"input": map[string]any{"draftId": draftID},
	}, &published); err != nil {
		return remotePost{}, err
	}

	post := published.PublishDraft.Post
	return remotePost{
		ID:     firstNonEmpty(post.ID, draftID),
		URL:    post.URL,
		Status: publishStatusPublished,
	}, nil
}

// --- GraphQL transport -----------------------------------------------------

func (p *hashnodeProvider) gql(ctx context.Context, secret, query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("encode hashnode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build hashnode request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("x-hashnode-client", "notes-app")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("hashnode request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read hashnode response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("hashnode rejected the personal access token. Reconnect Hashnode from the publish dialog.")
		default:
			return fmt.Errorf("hashnode: HTTP %s", resp.Status)
		}
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode hashnode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		first := envelope.Errors[0]
		message := strings.TrimSpace(first.Message)
		if first.Extensions.Code == "FORBIDDEN" || strings.Contains(strings.ToLower(message), "pro plan") {
			return fmt.Errorf("hashnode requires an active Pro plan on this publication: %s", message)
		}
		if message == "" {
			message = "request failed"
		}
		return fmt.Errorf("hashnode: %s", message)
	}
	if out == nil {
		return nil
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode hashnode data: %w", err)
	}
	return nil
}

func hashnodeTags(tags []string) []map[string]string {
	out := make([]map[string]string, 0, len(tags))
	for _, tag := range tags {
		name := strings.TrimSpace(tag)
		slug := hashnodeSlug(name)
		if slug == "" {
			continue
		}
		out = append(out, map[string]string{"name": name, "slug": slug})
	}
	return out
}

func hashnodeSlug(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
