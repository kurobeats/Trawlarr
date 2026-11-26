// Package web embeds the built single-page admin UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the UI files rooted at the build output directory.
func Dist() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}
