// Package web holds the panel's built Svelte app (npm run build → dist/).
package web

import "embed"

// Dist is dist/ (index.html and assets/).
//
//go:embed all:dist
var Dist embed.FS
