// Package adminui embeds the built admin dashboard so the server stays a
// single self-contained binary. Run `npm run build` in admin-ui/ (or let the
// Dockerfile's node stage do it) to populate dist/ before `go build`. dist/ is
// not committed; a binary built without it serves a hint instead of the UI.
package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built dashboard rooted at its index.html.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// The dist directory is embedded at compile time; its absence cannot
		// happen in a built binary.
		panic(err)
	}
	return sub
}
