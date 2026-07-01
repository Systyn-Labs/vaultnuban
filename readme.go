// Package vaultnuban embeds repo-root assets that need to ship with the
// compiled binary but don't belong to any internal package.
package vaultnuban

import (
	"bytes"
	_ "embed"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// ReadmeMD is the raw contents of README.md, embedded at build time so the
// API can serve it at GET / without touching the filesystem at runtime.
//
//go:embed README.md
var ReadmeMD string

var (
	readmeMD       = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
	readmeHTML     string
	readmeHTMLOnce sync.Once
)

// ReadmeHTML renders README.md to HTML (GitHub-flavored: tables, strikethrough,
// autolinks, task lists) once, then returns the cached result on every call.
func ReadmeHTML() string {
	readmeHTMLOnce.Do(func() {
		var buf bytes.Buffer
		if err := readmeMD.Convert([]byte(ReadmeMD), &buf); err != nil {
			readmeHTML = "<p>Failed to render README.</p>"
			return
		}
		readmeHTML = buf.String()
	})
	return readmeHTML
}
