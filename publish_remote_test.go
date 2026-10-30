package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	state := emptyPublishState("note1")
	state.Targets[platformDevto] = PublishTarget{
		RemoteID:    "42",
		URL:         "https://dev.to/u/post",
		Status:      publishStatusPublished,
		SyncedAt:    1234,
		ContentHash: "sha256:abc",
	}

	if err := writePublishState(root, state); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, found, err := readPublishState(root, "note1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !found {
		t.Fatal("state not found")
	}
	target := loaded.Targets[platformDevto]
	if target.RemoteID != "42" || target.Status != publishStatusPublished {
		t.Fatalf("target = %+v", target)
	}
	if loaded.Updated == 0 {
		t.Error("updated timestamp was not written")
	}

	raw, err := os.ReadFile(publishStatePath(root, "note1"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(raw), `"remoteId": "42"`) {
		t.Errorf("sidecar is not human-readable:\n%s", raw)
	}
}

func TestReadPublishStateMissingAndCorrupt(t *testing.T) {
	root := t.TempDir()

	state, found, err := readPublishState(root, "missing")
	if err != nil || found {
		t.Fatalf("missing state = %+v, %v, %v", state, found, err)
	}

	path := publishStatePath(root, "broken")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, found, err = readPublishState(root, "broken")
	if err != nil || found {
		t.Fatalf("corrupt state should be treated as empty: %+v, %v, %v", state, found, err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("corrupt sidecar was not backed up: %v", err)
	}
}

func TestDeleteNoteRemovesSidecar(t *testing.T) {
	root := t.TempDir()
	notes := &NotesService{root: root}

	if err := os.WriteFile(filepath.Join(root, "note1.md"), []byte("# Note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := emptyPublishState("note1")
	state.Targets[platformDevto] = PublishTarget{RemoteID: "42", Status: publishStatusDraft}
	if err := writePublishState(root, state); err != nil {
		t.Fatal(err)
	}

	if err := notes.Delete("note1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(publishStatePath(root, "note1")); !os.IsNotExist(err) {
		t.Fatalf("sidecar still exists: %v", err)
	}
}

func TestPublishStateRejectsTraversal(t *testing.T) {
	if _, _, err := readPublishState(t.TempDir(), "../escape"); err == nil {
		t.Fatal("expected an error for a traversal id")
	}
}

// --- dev.to -----------------------------------------------------------------

func TestDevtoValidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "secret" {
			t.Errorf("api-key header = %q", r.Header.Get("api-key"))
		}
		if r.URL.Path != "/api/users/me" {
			t.Errorf("path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"username":"tester","name":"Test User"}`)
	}))
	defer server.Close()

	provider := &devtoProvider{client: server.Client(), baseURL: server.URL}
	account, err := provider.Validate(context.Background(), "secret")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if account.Account != "tester" {
		t.Fatalf("account = %q", account.Account)
	}
}

func TestDevtoValidateRejectsBadKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer server.Close()

	provider := &devtoProvider{client: server.Client(), baseURL: server.URL}
	_, err := provider.Validate(context.Background(), "bad")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "reconnect") {
		t.Fatalf("error = %v", err)
	}
}

func TestDevtoPublishDraftPayload(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/articles" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"id":42,"url":"https://dev.to/u/post","slug":"post","published":false}`)
	}))
	defer server.Close()

	provider := &devtoProvider{client: server.Client(), baseURL: server.URL}
	post, err := provider.Publish(context.Background(), "secret", publishRequest{
		Title:     "Title",
		Body:      "Body",
		Tags:      []string{"go", "wails"},
		Canonical: "https://example.com/post",
		Draft:     true,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if post.ID != "42" || post.Status != publishStatusDraft {
		t.Fatalf("post = %+v", post)
	}

	article, ok := payload["article"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v", payload)
	}
	if article["published"] != false {
		t.Errorf("draft should not publish: %#v", article["published"])
	}
	if article["canonical_url"] != "https://example.com/post" {
		t.Errorf("canonical_url = %#v", article["canonical_url"])
	}
	if article["body_markdown"] != "Body" {
		t.Errorf("body_markdown = %#v", article["body_markdown"])
	}
}

func TestDevtoUpdateKeepsPublishedPostLive(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/articles/42" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		io.WriteString(w, `{"id":42,"url":"https://dev.to/u/post","published":true}`)
	}))
	defer server.Close()

	provider := &devtoProvider{client: server.Client(), baseURL: server.URL}
	post, err := provider.Update(context.Background(), "secret", "42", publishStatusPublished, publishRequest{
		Title: "Title",
		Body:  "Body",
		Draft: true, // even a "save draft" update must not unpublish
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if post.Status != publishStatusPublished {
		t.Fatalf("status = %q", post.Status)
	}
	article := payload["article"].(map[string]any)
	if article["published"] != true {
		t.Errorf("published = %#v", article["published"])
	}
}

// --- Hashnode ---------------------------------------------------------------

func TestHashnodeValidateListsPublications(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"data":{"me":{"username":"tester","publications":{"edges":[
			{"node":{"id":"pub1","title":"My Blog","url":"https://blog.hashnode.dev"}}
		]}}}}`)
	}))
	defer server.Close()

	provider := &hashnodeProvider{client: server.Client(), endpoint: server.URL}
	account, err := provider.Validate(context.Background(), "token")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if account.Account != "tester" || len(account.Publications) != 1 {
		t.Fatalf("account = %+v", account)
	}
	if account.Publications[0].ID != "pub1" {
		t.Errorf("publication = %+v", account.Publications[0])
	}
}

func TestHashnodePublishCreatesThenPublishes(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++

		switch {
		case strings.Contains(body.Query, "createDraft"):
			input := body.Variables["input"].(map[string]any)
			if input["publicationId"] != "pub1" {
				t.Errorf("publicationId = %#v", input["publicationId"])
			}
			if input["contentMarkdown"] != "Body" {
				t.Errorf("contentMarkdown = %#v", input["contentMarkdown"])
			}
			io.WriteString(w, `{"data":{"createDraft":{"draft":{"id":"d1","slug":"draft-slug","title":"Title"}}}}`)
		case strings.Contains(body.Query, "publishDraft"):
			io.WriteString(w, `{"data":{"publishDraft":{"post":{"id":"p1","slug":"post-slug","url":"https://blog.hashnode.dev/post-slug"}}}}`)
		default:
			t.Errorf("unexpected query: %s", body.Query)
			io.WriteString(w, `{"data":{}}`)
		}
	}))
	defer server.Close()

	provider := &hashnodeProvider{client: server.Client(), endpoint: server.URL}
	post, err := provider.Publish(context.Background(), "token", publishRequest{
		Title:         "Title",
		Body:          "Body",
		PublicationID: "pub1",
		Draft:         false,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected createDraft then publishDraft, got %d calls", calls)
	}
	if post.ID != "p1" || post.Status != publishStatusPublished {
		t.Fatalf("post = %+v", post)
	}
	if post.URL != "https://blog.hashnode.dev/post-slug" {
		t.Errorf("url = %q", post.URL)
	}
}

func TestHashnodeProGate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"errors":[{"message":"Pro plan required","extensions":{"code":"FORBIDDEN"}}]}`)
	}))
	defer server.Close()

	provider := &hashnodeProvider{client: server.Client(), endpoint: server.URL}
	_, err := provider.Validate(context.Background(), "token")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "pro plan") {
		t.Fatalf("error = %v", err)
	}
}

// --- service ----------------------------------------------------------------

func newTestPublishService(t *testing.T, handler http.HandlerFunc) (*PublishService, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	root := t.TempDir()
	service := NewPublishService(&NotesService{root: root})
	service.creds = &credentialStore{dir: t.TempDir(), forceFile: true}
	service.providers[platformDevto] = &devtoProvider{client: server.Client(), baseURL: server.URL}

	if _, err := service.creds.save(platformDevto, providerCredentials{Secret: "secret", Account: "tester"}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	return service, root
}

func TestPublishCreatesSidecarAndGuardsDuplicates(t *testing.T) {
	service, root := newTestPublishService(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":42,"url":"https://dev.to/u/post","published":false}`)
	})

	req := PublishRequest{NoteID: "note1", Title: "Title", Body: "Body", Tags: []string{"go"}, Draft: true}
	result, err := service.Publish(platformDevto, req)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if result.RemoteID != "42" || result.Status != publishStatusDraft {
		t.Fatalf("result = %+v", result)
	}
	if result.Warning != "" {
		t.Errorf("unexpected warning: %s", result.Warning)
	}

	state, found, err := readPublishState(root, "note1")
	if err != nil || !found {
		t.Fatalf("sidecar missing: %+v, %v, %v", state, found, err)
	}
	target := state.Targets[platformDevto]
	if target.RemoteID != "42" || target.ContentHash != publishContentHash("Title", "Body") {
		t.Fatalf("target = %+v", target)
	}

	if _, err := service.Publish(platformDevto, req); err == nil || !strings.Contains(err.Error(), "Update") {
		t.Fatalf("duplicate publish should be refused, got %v", err)
	}

	if _, err := service.Unlink(platformDevto, "note1"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if _, err := os.Stat(publishStatePath(root, "note1")); !os.IsNotExist(err) {
		t.Fatalf("empty sidecar should be removed: %v", err)
	}
}

func TestUpdatePublishedRefreshesHash(t *testing.T) {
	service, root := newTestPublishService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			io.WriteString(w, `{"id":42,"url":"https://dev.to/u/post","published":true}`)
			return
		}
		io.WriteString(w, `{"id":42,"url":"https://dev.to/u/post-v2","published":true}`)
	})

	if _, err := service.Publish(platformDevto, PublishRequest{
		NoteID: "note1", Title: "Title", Body: "First", Draft: false,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	result, err := service.UpdatePublished(platformDevto, PublishRequest{
		NoteID: "note1", Title: "Title", Body: "Second", Draft: false,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.URL != "https://dev.to/u/post-v2" {
		t.Errorf("url = %q", result.URL)
	}

	state, _, err := readPublishState(root, "note1")
	if err != nil {
		t.Fatal(err)
	}
	target := state.Targets[platformDevto]
	if target.ContentHash != publishContentHash("Title", "Second") {
		t.Errorf("hash was not refreshed: %q", target.ContentHash)
	}
	if target.PublishedAt == 0 {
		t.Error("publishedAt was not recorded")
	}
}

func TestUpdatePublishedRequiresTrackedPost(t *testing.T) {
	service, _ := newTestPublishService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
	})

	_, err := service.UpdatePublished(platformDevto, PublishRequest{
		NoteID: "note1", Title: "Title", Body: "Body",
	})
	if err == nil || !strings.Contains(err.Error(), "not been published") {
		t.Fatalf("error = %v", err)
	}
}

func TestPublishRequiresConnection(t *testing.T) {
	service, _ := newTestPublishService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
	})
	if err := service.creds.delete(platformDevto); err != nil {
		t.Fatal(err)
	}

	_, err := service.Publish(platformDevto, PublishRequest{NoteID: "note1", Title: "T", Body: "B"})
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("error = %v", err)
	}
}

func TestConnectionsNeverLeaksSecret(t *testing.T) {
	service, _ := newTestPublishService(t, func(w http.ResponseWriter, r *http.Request) {})

	statuses, err := service.Connections()
	if err != nil {
		t.Fatalf("connections: %v", err)
	}
	var devto ProviderStatus
	for _, status := range statuses {
		if status.Platform == platformDevto {
			devto = status
		}
	}
	if !devto.Connected || devto.Account != "tester" {
		t.Fatalf("devto status = %+v", devto)
	}
	if devto.Storage != credentialBackendFile {
		t.Errorf("storage = %q", devto.Storage)
	}
	if devto.Warning == "" {
		t.Error("file fallback should be surfaced to the user")
	}
}
