package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func prepareTestDraft(t *testing.T, title, body, platform string, opts PrepareOptions) Draft {
	t.Helper()
	service := NewPublishService(nil)
	draft, err := service.Prepare(title, body, platform, opts)
	if err != nil {
		t.Fatalf("prepare %s: %v", platform, err)
	}
	return draft
}

func TestPrepareXShortNote(t *testing.T) {
	draft := prepareTestDraft(t, "Hello world", "This is **bold**, *italic* and a [link](https://example.com).", "x", PrepareOptions{})

	if draft.OverLimit {
		t.Fatalf("short note reported as over limit: %+v", draft)
	}
	if draft.Truncated {
		t.Fatal("short note was truncated")
	}
	for _, unwanted := range []string{"**", "](http"} {
		if strings.Contains(draft.Text, unwanted) {
			t.Errorf("Markdown markers leaked into draft: %q", draft.Text)
		}
	}
	for _, want := range []string{"Hello world", "bold", "italic", "link (https://example.com)"} {
		if !strings.Contains(draft.Text, want) {
			t.Errorf("draft missing %q: %q", want, draft.Text)
		}
	}
	if len(draft.Parts) != 1 || draft.Parts[0] != draft.Text {
		t.Fatalf("short note should have exactly one part, got %#v", draft.Parts)
	}
}

func TestPrepareXLongNoteOffersThreadAndSingle(t *testing.T) {
	body := strings.Repeat("This is a sentence about writing long notes. ", 20)
	draft := prepareTestDraft(t, "Long note", body, "x", PrepareOptions{})

	if !draft.OverLimit {
		t.Fatal("long note should be over the X limit")
	}
	if len(draft.Parts) < 2 {
		t.Fatalf("expected a thread, got %d part(s)", len(draft.Parts))
	}
	for i, part := range draft.Parts {
		if got := weightedLength(part); got > xCharLimit {
			t.Errorf("part %d is %d characters (max %d): %q", i+1, got, xCharLimit, part)
		}
	}
	if got := weightedLength(draft.Text); got > xCharLimit {
		t.Errorf("single-post fallback is %d characters (max %d)", got, xCharLimit)
	}
	if !draft.Truncated {
		t.Error("single-post fallback should be marked truncated")
	}
}

func TestPrepareXThreadNumbering(t *testing.T) {
	body := strings.Repeat("Another sentence that takes up space in a thread. ", 24)
	draft := prepareTestDraft(t, "Thread", body, "x", PrepareOptions{
		Mode:          publishModeThread,
		NumberThreads: true,
	})

	if len(draft.Parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(draft.Parts))
	}
	for i, part := range draft.Parts {
		if got := weightedLength(part); got > xCharLimit {
			t.Errorf("numbered part %d is %d characters (max %d)", i+1, got, xCharLimit)
		}
		prefix := strings.Fields(part)
		if len(prefix) == 0 || !strings.Contains(prefix[0], "/") {
			t.Errorf("part %d is not numbered: %q", i+1, part)
		}
	}
}

func TestPrepareXShortNoteIsNeverSplit(t *testing.T) {
	draft := prepareTestDraft(t, "Short", "Just one line.", "x", PrepareOptions{Mode: publishModeThread})
	if draft.OverLimit {
		t.Fatal("short note should not be over the limit")
	}
	if len(draft.Parts) != 1 {
		t.Fatalf("short note should not be split, got %d parts", len(draft.Parts))
	}
}

func TestPrepareLinkedInTrimsToLimit(t *testing.T) {
	body := strings.Repeat("word ", 900)
	draft := prepareTestDraft(t, "Article", body, "linkedin", PrepareOptions{})

	if !draft.OverLimit || !draft.Truncated {
		t.Fatalf("long LinkedIn post should be trimmed: %+v", draft)
	}
	if got := utf8.RuneCountInString(draft.Text); got > linkedInCharLimit {
		t.Errorf("LinkedIn post is %d characters (max %d)", got, linkedInCharLimit)
	}
	if !strings.HasSuffix(draft.Text, "…") {
		t.Errorf("trimmed post should end with an ellipsis: %q", tail(draft.Text, 40))
	}
	if draft.Hook == "" {
		t.Error("draft should expose a hook preview")
	}
	if len(draft.Warnings) == 0 {
		t.Error("trimming should produce a warning")
	}
}

func TestPrepareAppendsCanonicalLink(t *testing.T) {
	draft := prepareTestDraft(t, "Post", "Body text.", "linkedin", PrepareOptions{
		Canonical: "example.com/my-post",
	})
	if !strings.Contains(draft.Text, "https://example.com/my-post") {
		t.Fatalf("canonical link missing: %q", draft.Text)
	}

	draft = prepareTestDraft(t, "Post", "Body text.", "x", PrepareOptions{
		Canonical: "https://example.com/already-absolute",
	})
	if !strings.Contains(draft.Text, "https://example.com/already-absolute") {
		t.Fatalf("absolute canonical link was altered: %q", draft.Text)
	}
}

func TestPrepareUnsupportedPlatform(t *testing.T) {
	service := NewPublishService(nil)
	if _, err := service.Prepare("Title", "Body", "myspace", PrepareOptions{}); err == nil {
		t.Fatal("expected an error for an unsupported platform")
	}
}

func TestParsePublishBlocksFlattening(t *testing.T) {
	source := composeDocument("A title", `Intro with a [link](https://example.com) and ![alt text](pic.png).

- first item
- second **item**
  - nested item
1. ordered one
2. ordered two

- [x] done
- [ ] todo

| Name | Value |
| --- | --- |
| alpha | 1 |

`+"```go\nfunc main() {}\nfmt.Println()\n```\n")

	blocks := parsePublishBlocks(source)
	var all []string
	for _, block := range blocks {
		all = append(all, block.text)
	}
	joined := strings.Join(all, "\n")

	for _, want := range []string{
		"A title",
		"link (https://example.com)",
		"alt text",
		"- first item",
		"- second item",
		"  - nested item",
		"1. ordered one",
		"2. ordered two",
		"- [x] done",
		"- [ ] todo",
		"Name | Value",
		"alpha | 1",
		"Code snippet (go, 2 lines)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("flattened text missing %q:\n%s", want, joined)
		}
	}
}

func TestWeightedLengthCountsURLsAsFixed(t *testing.T) {
	short := "read https://example.com"
	long := "read https://example.com/a/very/long/path/that/goes/on/and/on"
	if weightedLength(short) != weightedLength(long) {
		t.Fatalf("URLs should weigh the same: %d vs %d", weightedLength(short), weightedLength(long))
	}
	if got, want := weightedLength(short), len("read ")+xURLLength; got != want {
		t.Fatalf("weightedLength = %d, want %d", got, want)
	}
}

func TestSplitSentences(t *testing.T) {
	got := splitSentences(`First sentence. Second one! Is this third? Yes.`)
	want := []string{"First sentence.", "Second one!", "Is this third?", "Yes."}
	if len(got) != len(want) {
		t.Fatalf("splitSentences = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTruncateWeightedRespectsLimit(t *testing.T) {
	value := "Intro text that is long enough. https://example.com/some/really/long/path"
	got, truncated := truncateWeighted(value, 40)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if weightedLength(got) > 40 {
		t.Fatalf("truncated text is %d characters (max 40)", weightedLength(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated text should end with an ellipsis: %q", got)
	}
}

func TestPrepareEmptyNote(t *testing.T) {
	draft := prepareTestDraft(t, "", "", "x", PrepareOptions{})
	if draft.OverLimit || draft.Truncated {
		t.Fatalf("empty note should not be over the limit: %+v", draft)
	}
	if draft.Title != "Untitled note" {
		t.Fatalf("title = %q", draft.Title)
	}
}

func tail(value string, n int) string {
	runes := []rune(value)
	if len(runes) <= n {
		return value
	}
	return string(runes[len(runes)-n:])
}
