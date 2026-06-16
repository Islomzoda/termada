// Package dashboard serves the embedded local web UI (spec §8.1). The SPA is a
// single dependency-free HTML/JS file compiled into the binary via embed.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets
var assets embed.FS

// Handler serves the dashboard SPA and its assets.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
