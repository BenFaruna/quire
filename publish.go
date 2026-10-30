package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Phase 1 of publishing: turn a note into a platform-ready draft, entirely
// locally. Nothing here touches the network or needs credentials, so the app
// can offer copy-and-post for X and LinkedIn without any OAuth setup.

const (
	platformX        = "x"
	platformLinkedIn = "linkedin"
	platformDevto    = "devto"
	platformHashnode = "hashnode"

	xCharLimit        = 280
	linkedInCharLimit = 3000

	// X counts every URL as a fixed number of characters, regardless of length.
	xURLLength = 23

	publishModeAuto   = "auto"
	publishModeThread = "thread"
	publishModeSingle = "single"
)

// PrepareOptions adjusts how a draft is generated.
type PrepareOptions struct {
	// Mode is "auto" (default), "thread" or "single". It only affects X posts
	// that are longer than the character limit.
	Mode string `json:"mode"`
	// NumberThreads prefixes thread parts with "1/5 " style counters.
	NumberThreads bool `json:"numberThreads"`
	// Canonical is an optional URL appended to the draft.
	Canonical string `json:"canonical"`
}

// Draft is a note rendered for one platform, ready to be copied or posted.
type Draft struct {
	Platform string `json:"platform"`
	Title    string `json:"title"`
	// Text is the single-post body. For X posts over the limit it is the
	// trimmed one-post version; Parts holds the full thread.
	Text      string `json:"text"`
	CharLimit int    `json:"charLimit"`
	OverLimit bool   `json:"overLimit"`
	Truncated bool   `json:"truncated"`
	// Parts holds one entry per X thread post.
	Parts []string `json:"parts"`
	// Hook is the opening text that social feeds show before "see more".
	Hook string `json:"hook"`
	// Body is the Markdown body used by blog targets. Text stays empty for
	// those platforms because they do not need a flattened social post.
	Body string `json:"body"`
	// Tags are platform-normalised tags for blog targets.
	Tags []string `json:"tags"`
	// ContentHash is a fingerprint of the current note, used to detect edits
	// made after the last publish.
	ContentHash string   `json:"contentHash"`
	Warnings    []string `json:"warnings"`
}

// PublishService converts notes into platform drafts and, for connected blog
// accounts, publishes them directly.
type PublishService struct {
	notes     *NotesService
	creds     *credentialStore
	client    *http.Client
	providers map[string]publishProvider
}

func NewPublishService(notes *NotesService) *PublishService {
	client := &http.Client{Timeout: 45 * time.Second}
	service := &PublishService{
		notes:  notes,
		creds:  newCredentialStore(),
		client: client,
		providers: map[string]publishProvider{
			platformDevto:    newDevtoProvider(client),
			platformHashnode: newHashnodeProvider(client),
		},
	}
	return service
}

// Prepare flattens a note's Markdown into a draft for platform.
//
// The source Markdown is parsed with the app's goldmark pipeline, so links,
// lists, task items and tables degrade deliberately instead of being stripped
// by a regular expression.
func (s *PublishService) Prepare(title, body, platform string, opts PrepareOptions) (Draft, error) {
	platform = normalizePublishPlatform(platform)
	limit := 0
	switch platform {
	case platformX:
		limit = xCharLimit
	case platformLinkedIn:
		limit = linkedInCharLimit
	case platformDevto, platformHashnode:
		// Blog targets have no character limit.
	default:
		return Draft{}, fmt.Errorf("unsupported publish target %q", platform)
	}

	draft := Draft{
		Platform:    platform,
		Title:       strings.TrimSpace(title),
		CharLimit:   limit,
		ContentHash: publishContentHash(title, body),
	}
	if draft.Title == "" {
		draft.Title = "Untitled note"
	}

	if platform == platformDevto || platform == platformHashnode {
		prepareBlog(&draft, body)
		return draft, nil
	}

	text, warnings := socialText(parsePublishBlocks(composeDocument(title, body)))
	if link := normalizePublishLink(opts.Canonical); link != "" {
		if text != "" {
			text += "\n\n"
		}
		text += link
	}
	draft.Text = strings.TrimSpace(text)
	draft.Warnings = warnings

	switch platform {
	case platformX:
		prepareX(&draft, limit, opts)
	case platformLinkedIn:
		prepareLinkedIn(&draft, limit)
	}
	return draft, nil
}

func normalizePublishPlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "x", "twitter":
		return platformX
	case "linkedin":
		return platformLinkedIn
	case "devto", "dev.to", "dev":
		return platformDevto
	case "hashnode":
		return platformHashnode
	default:
		return strings.ToLower(strings.TrimSpace(platform))
	}
}

// prepareBlog keeps the note as Markdown (blog platforms accept it directly)
// and pulls tags out of a "Tags:" line or inline hashtags.
func prepareBlog(draft *Draft, body string) {
	cleaned, tags := extractPublishTags(body)
	draft.Body = strings.TrimSpace(cleaned)
	draft.Tags = normalizePublishTags(draft.Platform, tags)
	draft.Text = ""

	if draft.Body == "" {
		draft.Warnings = append(draft.Warnings, "This note has no body text to publish.")
	}
	if draft.Platform == platformDevto && len(tags) > 4 {
		draft.Warnings = append(draft.Warnings, "dev.to accepts at most 4 tags; extra tags were dropped.")
	}
}

var (
	publishTagLine = regexp.MustCompile(`(?i)^\s*tags?\s*:\s*(.+?)\s*$`)
	publishHashtag = regexp.MustCompile(`(?:^|\s)#([A-Za-z][A-Za-z0-9_-]{0,49})`)
)

// extractPublishTags pulls tags out of a "Tags: a, b" line, which is removed
// from the body, and out of inline #hashtags, which are left in place.
func extractPublishTags(body string) (string, []string) {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	var tags []string

	inFence := false
	sawTagLine := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if !inFence && !sawTagLine {
			if match := publishTagLine.FindStringSubmatch(line); match != nil {
				tags = append(tags, splitTagList(match[1])...)
				sawTagLine = true
				continue
			}
		}
		kept = append(kept, line)
	}

	cleaned := strings.Join(kept, "\n")
	inFence = false
	for _, line := range strings.Split(cleaned, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, match := range publishHashtag.FindAllStringSubmatch(line, -1) {
			tags = append(tags, match[1])
		}
	}

	return cleaned, tags
}

func splitTagList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	tags := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			tags = append(tags, field)
		}
	}
	return tags
}

func normalizePublishTags(platform string, tags []string) []string {
	limit := 4
	if platform == platformHashnode {
		limit = 15
	}

	out := make([]string, 0, limit)
	seen := map[string]bool{}
	for _, raw := range tags {
		var value, key string
		if platform == platformHashnode {
			value = strings.Join(strings.Fields(raw), " ")
			key = hashnodeSlug(value)
		} else {
			value = devtoTag(raw)
			key = value
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
		if len(out) == limit {
			break
		}
	}
	return out
}

// devtoTag strips everything dev.to does not accept in a tag.
func devtoTag(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func prepareX(draft *Draft, limit int, opts PrepareOptions) {
	paragraphs := splitParagraphs(draft.Text)
	if len(paragraphs) == 0 {
		draft.Parts = nil
		return
	}

	if weightedLength(draft.Text) <= limit {
		draft.Parts = []string{draft.Text}
		return
	}

	draft.OverLimit = true
	parts := splitThread(paragraphs, limit)
	if opts.Mode == publishModeThread && opts.NumberThreads {
		parts = numberThreadParts(paragraphs, limit)
	}
	draft.Parts = parts

	// The single-post version is always available so the UI can offer both
	// choices when a note is too long.
	draft.Text, draft.Truncated = truncateWeighted(draft.Text, limit)
}

func prepareLinkedIn(draft *Draft, limit int) {
	draft.Hook = firstRunes(draft.Text, 260)

	count := utf8.RuneCountInString(draft.Text)
	if count <= limit {
		return
	}
	draft.OverLimit = true
	draft.Text, draft.Truncated = truncateRunes(draft.Text, limit)
	draft.Warnings = append(draft.Warnings,
		fmt.Sprintf("Trimmed from %d to %d characters to fit LinkedIn.", count, limit))
}

// --- Markdown flattening ---------------------------------------------------

type publishBlock struct {
	text string
	code bool
}

// parsePublishBlocks parses Markdown into plain-text blocks. Headings become
// their own block; list items stay separate so thread splitting never cuts one
// in half; code blocks become a one-line summary.
func parsePublishBlocks(source string) []publishBlock {
	src := []byte(source)
	doc := markdownRenderer.Parser().Parse(text.NewReader(src))
	return collectPublishBlocks(doc, src)
}

func collectPublishBlocks(parent ast.Node, src []byte) []publishBlock {
	var blocks []publishBlock
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		switch node := n.(type) {
		case *ast.Heading:
			blocks = appendTextBlock(blocks, inlineText(node, src))
		case *ast.Paragraph, *ast.TextBlock:
			blocks = appendTextBlock(blocks, inlineText(node, src))
		case *ast.Blockquote:
			blocks = append(blocks, collectPublishBlocks(node, src)...)
		case *ast.List:
			blocks = append(blocks, listPublishBlocks(node, src)...)
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			blocks = append(blocks, publishBlock{text: codeSummary(node, src), code: true})
		case *extast.Table:
			blocks = appendTextBlock(blocks, tableText(node, src))
		}
	}
	return blocks
}

func appendTextBlock(blocks []publishBlock, value string) []publishBlock {
	if value = strings.TrimSpace(value); value != "" {
		blocks = append(blocks, publishBlock{text: value})
	}
	return blocks
}

func listPublishBlocks(list *ast.List, src []byte) []publishBlock {
	var blocks []publishBlock
	index := list.Start
	if index == 0 {
		index = 1
	}
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		li, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "- "
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d. ", index)
			index++
		}
		var lines []string
		collectListItemLines(li, src, &lines)
		if len(lines) == 0 {
			continue
		}
		blocks = appendTextBlock(blocks, marker+strings.Join(lines, "\n"))
	}
	return blocks
}

func collectListItemLines(node ast.Node, src []byte, lines *[]string) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch n := child.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			*lines = append(*lines, inlineText(n, src))
		case *ast.Blockquote:
			collectListItemLines(n, src, lines)
		case *ast.List:
			for _, nested := range listPublishBlocks(n, src) {
				for _, line := range strings.Split(nested.text, "\n") {
					*lines = append(*lines, "  "+line)
				}
			}
		}
	}
}

func codeSummary(node ast.Node, src []byte) string {
	language := ""
	lines := 0
	switch n := node.(type) {
	case *ast.FencedCodeBlock:
		language = strings.TrimSpace(string(n.Language(src)))
		lines = n.Lines().Len()
	case *ast.CodeBlock:
		lines = n.Lines().Len()
	}

	switch {
	case language != "" && lines > 0:
		return fmt.Sprintf("Code snippet (%s, %s)", language, pluralLines(lines))
	case lines > 0:
		return fmt.Sprintf("Code snippet (%s)", pluralLines(lines))
	default:
		return "Code snippet"
	}
}

func pluralLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func tableText(table *extast.Table, src []byte) string {
	var rows []string
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, strings.TrimSpace(inlineText(cell, src)))
		}
		if len(cells) > 0 {
			rows = append(rows, strings.Join(cells, " | "))
		}
	}
	return strings.Join(rows, "\n")
}

// inlineText renders the inline children of n as plain text. Links keep their
// label and absolute destination; images keep their alt text.
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(parent ast.Node) {
		for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
			switch node := child.(type) {
			case *ast.Text:
				b.Write(node.Segment.Value(src))
				if node.HardLineBreak() || node.SoftLineBreak() {
					b.WriteByte('\n')
				}
			case *ast.String:
				b.Write(node.Value)
			case *ast.AutoLink:
				b.Write(node.URL(src))
			case *ast.Link:
				label := strings.TrimSpace(inlineText(node, src))
				destination := strings.TrimSpace(string(node.Destination))
				switch {
				case label == "":
					b.WriteString(destination)
				case destination == "" || destination == label:
					b.WriteString(label)
				case strings.HasPrefix(destination, "http://"), strings.HasPrefix(destination, "https://"):
					b.WriteString(label)
					b.WriteString(" (")
					b.WriteString(destination)
					b.WriteByte(')')
				default:
					b.WriteString(label)
				}
			case *ast.RawHTML:
				// Raw HTML is never meaningful in a social post.
			case *extast.TaskCheckBox:
				if node.IsChecked {
					b.WriteString("[x] ")
				} else {
					b.WriteString("[ ] ")
				}
			default:
				walk(child)
			}
		}
	}
	walk(n)
	return b.String()
}

// socialText joins blocks into the paragraph flow used for both platforms.
func socialText(blocks []publishBlock) (string, []string) {
	var paragraphs []string
	var warnings []string
	sawCode := false

	for _, block := range blocks {
		if block.code {
			sawCode = true
		}
		if text := strings.TrimSpace(block.text); text != "" {
			paragraphs = append(paragraphs, text)
		}
	}
	if sawCode {
		warnings = append(warnings, "Code blocks are summarised for social posts.")
	}
	return strings.Join(paragraphs, "\n\n"), warnings
}

// --- Platform shaping ------------------------------------------------------

func splitParagraphs(value string) []string {
	var paragraphs []string
	for _, paragraph := range strings.Split(value, "\n\n") {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			paragraphs = append(paragraphs, paragraph)
		}
	}
	return paragraphs
}

// splitThread packs paragraphs into as few parts as possible without exceeding
// limit, splitting an oversized paragraph on sentence and then word boundaries.
func splitThread(paragraphs []string, limit int) []string {
	if limit <= 0 {
		return nil
	}

	var parts []string
	var current []string
	flush := func() {
		if len(current) > 0 {
			parts = append(parts, strings.Join(current, "\n\n"))
			current = nil
		}
	}

	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if weightedLength(paragraph) > limit {
			flush()
			parts = append(parts, splitLongBlock(paragraph, limit)...)
			continue
		}
		if len(current) == 0 {
			current = append(current, paragraph)
			continue
		}
		candidate := strings.Join(append(append([]string{}, current...), paragraph), "\n\n")
		if weightedLength(candidate) <= limit {
			current = append(current, paragraph)
			continue
		}
		flush()
		current = append(current, paragraph)
	}
	flush()
	return parts
}

func splitLongBlock(block string, limit int) []string {
	var parts []string
	current := ""
	for _, sentence := range splitSentences(block) {
		for _, chunk := range splitByWords(sentence, limit) {
			switch {
			case current == "":
				current = chunk
			case weightedLength(current+" "+chunk) <= limit:
				current += " " + chunk
			default:
				parts = append(parts, current)
				current = chunk
			}
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func splitByWords(value string, limit int) []string {
	if weightedLength(value) <= limit {
		return []string{value}
	}

	var chunks []string
	current := ""
	for _, word := range strings.Fields(value) {
		if weightedLength(word) > limit {
			if current != "" {
				chunks = append(chunks, current)
				current = ""
			}
			chunks = append(chunks, hardSplit(word, limit)...)
			continue
		}
		switch {
		case current == "":
			current = word
		case weightedLength(current+" "+word) <= limit:
			current += " " + word
		default:
			chunks = append(chunks, current)
			current = word
		}
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}

// hardSplit handles a single token longer than the limit (typically a URL).
func hardSplit(value string, limit int) []string {
	runes := []rune(value)
	var chunks []string
	for len(runes) > 0 {
		cut := limit
		if cut > len(runes) {
			cut = len(runes)
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	return chunks
}

// splitSentences splits on ., ! and ? followed by whitespace, keeping the
// punctuation and any closing quotes or brackets with the sentence.
func splitSentences(value string) []string {
	var sentences []string
	start := 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '.', '!', '?':
		default:
			continue
		}

		end := i + 1
		for end < len(value) && strings.ContainsRune(`"')]`, rune(value[end])) {
			end++
		}
		if end < len(value) && !isSpace(value[end]) {
			continue
		}
		sentences = append(sentences, strings.TrimSpace(value[start:end]))
		start = end
		i = end - 1
	}
	if rest := strings.TrimSpace(value[start:]); rest != "" {
		sentences = append(sentences, rest)
	}
	if len(sentences) == 0 {
		return []string{strings.TrimSpace(value)}
	}
	return sentences
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\n' || b == '\t' || b == '\r'
}

func numberThreadParts(paragraphs []string, limit int) []string {
	// Reserve room for a "99/99 " prefix so numbering never pushes a part over
	// the platform limit.
	const reserve = 8
	if limit <= reserve+20 {
		return splitThread(paragraphs, limit)
	}
	parts := splitThread(paragraphs, limit-reserve)
	total := len(parts)
	for i := range parts {
		parts[i] = fmt.Sprintf("%d/%d %s", i+1, total, parts[i])
	}
	return parts
}

// --- Character counting ----------------------------------------------------

var urlPattern = regexp.MustCompile(`https?://[^\s]+`)

// weightedLength mirrors how X counts a post: URLs count as xURLLength
// characters and everything else counts as one rune.
func weightedLength(value string) int {
	count := 0
	last := 0
	for _, loc := range urlPattern.FindAllStringIndex(value, -1) {
		count += utf8.RuneCountInString(value[last:loc[0]])
		count += xURLLength
		last = loc[1]
	}
	count += utf8.RuneCountInString(value[last:])
	return count
}

func truncateWeighted(value string, limit int) (string, bool) {
	if weightedLength(value) <= limit {
		return value, false
	}
	if limit < 2 {
		return "", true
	}

	var builder strings.Builder
	for _, word := range strings.Fields(value) {
		separator := ""
		if builder.Len() > 0 {
			separator = " "
		}
		if weightedLength(builder.String()+separator+word) > limit-1 {
			break
		}
		builder.WriteString(separator)
		builder.WriteString(word)
	}
	result := strings.TrimRight(builder.String(), " \n\t,;:.")
	if result == "" {
		return "…", true
	}
	return result + "…", true
}

func truncateRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	if limit < 2 {
		return "", true
	}

	cut := string(runes[:limit-1])
	if index := strings.LastIndexAny(cut, " \n"); index > limit/2 {
		cut = cut[:index]
	}
	return strings.TrimRight(cut, " \n\t,;:.") + "…", true
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func normalizePublishLink(raw string) string {
	link := strings.TrimSpace(raw)
	if link == "" {
		return ""
	}
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	return "https://" + link
}
