// Package web is overfin-web, Overfin's own web client: plain HTML, CSS and
// JavaScript on the JavaScript SDK, with no build step. Overfin serves it by
// default; an operator can serve their own client instead ([site] in the
// configuration).
package web

import "embed"

// Files are the client's files, served as they are.
//
//go:embed index.html app.js style.css overfin.js vendor
var Files embed.FS
