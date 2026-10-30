package main

import (
	"strings"
	"testing"
)

func TestPrepareBlogStripsTagsLineAndNormalisesDevto(t *testing.T) {
	body := "Intro paragraph.\n\nTags: Go, Wails, dev.to, Wails, extra, more\n\nMore text.\n"
	draft := prepareTestDraft(t, "Blog title", body, "devto", PrepareOptions{})

	if strings.Contains(draft.Body, "Tags:") {
		t.Fatalf("tags line was not removed:\n%s", draft.Body)
	}
	if draft.Platform != platformDevto {
		t.Fatalf("platform = %q", draft.Platform)
	}
	if draft.CharLimit != 0 || draft.OverLimit || draft.Truncated {
		t.Fatalf("blog draft should have no social limits: %+v", draft)
	}
	want := []string{"go", "wails", "devto", "extra"}
	if len(draft.Tags) != len(want) {
		t.Fatalf("tags = %#v, want %#v", draft.Tags, want)
	}
	for i := range want {
		if draft.Tags[i] != want[i] {
			t.Errorf("tag %d = %q, want %q", i, draft.Tags[i], want[i])
		}
	}
	if len(draft.Warnings) == 0 || !strings.Contains(draft.Warnings[0], "4 tags") {
		t.Errorf("expected a tag-limit warning, got %#v", draft.Warnings)
	}
}

func TestPrepareBlogCollectsHashtags(t *testing.T) {
	draft := prepareTestDraft(t, "Post", "Shipping #Wails and #go today.\n", "hashnode", PrepareOptions{})

	for _, want := range []string{"Wails", "go"} {
		found := false
		for _, tag := range draft.Tags {
			if tag == want {
				found = true
			}
		}
		if !found {
			t.Errorf("tag %q missing from %#v", want, draft.Tags)
		}
	}
	if !strings.Contains(draft.Body, "#Wails") {
		t.Errorf("hashtags should stay in the body: %q", draft.Body)
	}
}

func TestPrepareBlogHashnodeKeepsMultiWordTags(t *testing.T) {
	draft := prepareTestDraft(t, "Post", "Body.\n\nTags: Web Development, Go, Web-Development, One, Two, Three\n", "hashnode", PrepareOptions{})

	if got := len(draft.Tags); got != 5 {
		t.Fatalf("expected 5 unique tags, got %d: %#v", got, draft.Tags)
	}
	if draft.Tags[0] != "Web Development" {
		t.Errorf("multi-word tag lost its display name: %#v", draft.Tags)
	}
}

func TestPrepareBlogEmptyBody(t *testing.T) {
	draft := prepareTestDraft(t, "Title", "", "devto", PrepareOptions{})
	if draft.Body != "" {
		t.Fatalf("body = %q", draft.Body)
	}
	if len(draft.Warnings) == 0 {
		t.Fatal("empty body should warn")
	}
}

func TestPublishContentHash(t *testing.T) {
	a := publishContentHash("Title", "Body")
	b := publishContentHash("Title", "Body")
	if a != b {
		t.Fatalf("hash is not stable: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("hash = %q", a)
	}
	if c := publishContentHash("Title", "Changed"); c == a {
		t.Fatal("hash should change when the body changes")
	}
	if c := publishContentHash("Other", "Body"); c == a {
		t.Fatal("hash should change when the title changes")
	}
}

func TestNormalizePublishPlatform(t *testing.T) {
	cases := map[string]string{
		"X":        platformX,
		"twitter":  platformX,
		"LinkedIn": platformLinkedIn,
		"dev.to":   platformDevto,
		"DEV":      platformDevto,
		"Hashnode": platformHashnode,
	}
	for input, want := range cases {
		if got := normalizePublishPlatform(input); got != want {
			t.Errorf("normalizePublishPlatform(%q) = %q, want %q", input, got, want)
		}
	}
}
