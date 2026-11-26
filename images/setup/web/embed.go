// Package web holds the wizard's pages (plain ES modules, no build step).
package web

import "embed"

//go:embed index.html app.css app.js lib.js flows/*.js
var FS embed.FS
