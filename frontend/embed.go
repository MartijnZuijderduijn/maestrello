// Package frontend holds the board's web UI, embedded into the binary.
package frontend

import "embed"

// FS is the UI's static files, rooted at the frontend folder.
//
//go:embed index.html *.css *.svg *.png *.ico js
var FS embed.FS
