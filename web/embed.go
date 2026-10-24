// Package web holds the web UI: the React app in this directory, built into
// dist/ (npm run build) and embedded in the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built app. Without a build it only holds .gitkeep, and the
// server shows a page explaining how to build it.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded: it always exists
	}
	return sub
}
