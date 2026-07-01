// Package vaultnuban embeds repo-root assets that need to ship with the
// compiled binary but don't belong to any internal package.
package vaultnuban

import _ "embed"

// ReadmeMD is the raw contents of README.md, embedded at build time so the
// API can serve it at GET / without touching the filesystem at runtime.
//
//go:embed README.md
var ReadmeMD string
