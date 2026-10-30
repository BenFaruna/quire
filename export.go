package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"html/template"
	"io"
	"regexp"
	"strings"
)

const (
	exportFormatMarkdown = "markdown"
	exportFormatHTML     = "html"
)

// Export writes the selected notes to destPath.
//
// With a single note the destination is a .md/.html file. With several notes it
// is a .zip containing one file per note. The source Markdown is always used as
// the basis, so an HTML export is rendered from the same documents the app shows.
func (s *NotesService) Export(ids []string, format, destPath string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, errors.New("no notes selected")
	}
	if format != exportFormatMarkdown && format != exportFormatHTML {
		return 0, fmt.Errorf("unsupported export format %q", format)
	}
	destPath = strings.TrimSpace(destPath)
	if destPath == "" {
		return 0, errors.New("no destination selected")
	}

	type exportFile struct {
		name    string
		content string
	}

	files := make([]exportFile, 0, len(ids))
	used := make(map[string]bool)
	for _, id := range ids {
		note, err := s.readNoteLocked(id)
		if err != nil {
			return 0, err
		}

		document := composeDocument(note.Title, note.Body)
		var content string
		if format == exportFormatHTML {
			body, err := renderMarkdown(document)
			if err != nil {
				return 0, fmt.Errorf("render %q: %w", note.Title, err)
			}
			content, err = renderExportHTML(note.Title, body)
			if err != nil {
				return 0, err
			}
		} else {
			content = document
		}

		files = append(files, exportFile{
			name:    uniqueExportName(used, safeExportFilename(note.Title), format),
			content: content,
		})
	}

	if len(files) == 1 {
		err := writeFileAtomic(destPath, 0o644, func(w io.Writer) error {
			_, err := io.WriteString(w, files[0].content)
			return err
		})
		if err != nil {
			return 0, fmt.Errorf("export note: %w", err)
		}
		return 1, nil
	}

	err := writeFileAtomic(destPath, 0o644, func(w io.Writer) error {
		zw := zip.NewWriter(w)
		for _, file := range files {
			entry, err := zw.Create(file.name)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(entry, file.content); err != nil {
				return err
			}
		}
		return zw.Close()
	})
	if err != nil {
		return 0, fmt.Errorf("export notes: %w", err)
	}
	return len(files), nil
}

var invalidFilenameChars = regexp.MustCompile(`[^\p{L}\p{N} ._-]+`)

// safeExportFilename turns a note title into a portable file name stem.
func safeExportFilename(title string) string {
	name := invalidFilenameChars.ReplaceAllString(strings.TrimSpace(title), "-")
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, " .-")
	if name == "" {
		name = "note"
	}
	if runes := []rune(name); len(runes) > 80 {
		name = strings.TrimSpace(string(runes[:80]))
	}
	return name
}

// uniqueExportName prevents duplicate titles from overwriting each other inside
// a multi-note archive.
func uniqueExportName(used map[string]bool, stem, format string) string {
	ext := ".md"
	if format == exportFormatHTML {
		ext = ".html"
	}
	name := stem + ext
	for i := 2; used[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
	used[strings.ToLower(name)] = true
	return name
}

func renderExportHTML(title, body string) (string, error) {
	var buf strings.Builder
	err := exportHTMLTemplate.Execute(&buf, struct {
		Title string
		Body  template.HTML
	}{
		Title: title,
		Body:  template.HTML(body), // goldmark output; raw HTML is already omitted
	})
	if err != nil {
		return "", fmt.Errorf("render HTML export: %w", err)
	}
	return buf.String(), nil
}

var exportHTMLTemplate = template.Must(template.New("export").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>{{.Title}}</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background: #0e1116;
    color: #dbe3ee;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Inter, system-ui, sans-serif;
    font-size: 16px;
    line-height: 1.72;
    -webkit-font-smoothing: antialiased;
  }
  main { max-width: 820px; margin: 0 auto; padding: 52px 34px 96px; }
  h1, h2, h3, h4, h5, h6 {
    color: #f0f4fa;
    font-weight: 650;
    line-height: 1.3;
    letter-spacing: -0.015em;
    margin: 1.6em 0 0.6em;
  }
  h1 { font-size: 1.85em; padding-bottom: 0.35em; border-bottom: 1px solid #222a35; margin-top: 0; }
  h2 { font-size: 1.45em; padding-bottom: 0.3em; border-bottom: 1px solid #222a35; }
  h3 { font-size: 1.2em; }
  p { margin: 0.85em 0; }
  a { color: #e8b563; text-decoration: none; border-bottom: 1px solid rgba(232,181,99,.35); }
  a:hover { color: #f2c87e; }
  strong { color: #f2f6fc; }
  del { color: #66707f; }
  ul, ol { padding-left: 1.6em; }
  li { margin: 0.3em 0; }
  li::marker { color: #66707f; }
  input[type="checkbox"] { accent-color: #e8b563; margin: 0 0.45em 0 -1.35em; }
  blockquote {
    margin: 1em 0;
    padding: 0.3em 1em;
    border-left: 3px solid #e8b563;
    background: rgba(232,181,99,.14);
    border-radius: 0 7px 7px 0;
    color: #8b97a8;
  }
  blockquote p { margin: 0.5em 0; }
  hr { height: 1px; border: 0; background: #222a35; margin: 2em 0; }
  code {
    padding: 0.15em 0.38em;
    border: 1px solid #222a35;
    border-radius: 5px;
    background: #171e27;
    color: #e7c07a;
    font-family: "SF Mono", "JetBrains Mono", ui-monospace, Menlo, Consolas, monospace;
    font-size: 0.87em;
  }
  pre {
    margin: 1em 0;
    padding: 15px 17px;
    border: 1px solid #222a35;
    border-radius: 10px;
    overflow-x: auto;
    print-color-adjust: exact;
    -webkit-print-color-adjust: exact;
  }
  pre code { padding: 0; border: 0; background: transparent; color: inherit; font-size: 12.8px; line-height: 1.6; }
  table { border-collapse: collapse; margin: 1.1em 0; font-size: 0.93em; display: block; overflow-x: auto; }
  th, td { padding: 7px 13px; border: 1px solid #222a35; text-align: left; }
  th { background: #151b24; color: #eef3fa; font-weight: 600; }
  img { max-width: 100%; border-radius: 10px; }
  .footnotes { margin-top: 2.5em; padding-top: 1em; border-top: 1px solid #222a35; color: #8b97a8; font-size: 0.9em; }
  @media print {
    body { background: #fff; color: #111; }
    h1, h2, h3, h4, h5, h6 { color: #111; }
    a { color: #0b57d0; }
  }
</style>
</head>
<body>
<main class="markdown-body">
{{.Body}}</main>
</body>
</html>
`))
