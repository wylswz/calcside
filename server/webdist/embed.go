// Package webdist embeds the built console (webdist/dist, written there by
// the web/ Vite build). When the frontend has not been built, dist contains
// only a placeholder and the API serves a "console not built" message
// instead of index.html.
package webdist

import "embed"

//go:embed all:dist
var Dist embed.FS
