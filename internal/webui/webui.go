// Package webui embeds the built SPA (web/ → internal/webui/dist) into the binary.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist/.
func FS() (fs.FS, error) { return fs.Sub(dist, "dist") }
