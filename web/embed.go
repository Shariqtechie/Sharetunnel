// Package web holds the admin panel (plain HTML, CSS and JavaScript, no build step).
// The files are compiled into the ShareTunnel binary.
package web

import "embed"

// Files is the whole panel, served at the root of the web server.
//
//go:embed index.html favicon.svg css js img
var Files embed.FS
