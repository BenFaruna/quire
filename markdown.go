package main

import (
	"bytes"
	"fmt"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// markdownRenderer is a shared, thread-safe goldmark instance configured for
// GitHub-flavoured Markdown.
//
// Raw HTML is intentionally NOT enabled: goldmark replaces it with a comment,
// so note content can never inject markup/scripts into the webview.
var markdownRenderer = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,         // tables, strikethrough, task lists, autolinks
		extension.Footnote,    // [^1] style footnotes
		extension.Typographer, // smart quotes, em/en dashes, ellipses
		highlighting.NewHighlighting(
			highlighting.WithStyle("github-dark"),
		),
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
	),
)

// RenderMarkdown converts a Markdown document to sanitised HTML.
func renderMarkdown(source string) (string, error) {
	var buf bytes.Buffer
	if err := markdownRenderer.Convert([]byte(source), &buf); err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	return buf.String(), nil
}
