// Package web embeds the built console (web/dist). When the frontend has
// not been built, dist contains only a placeholder and the API serves a
// "console not built" message instead of index.html.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
