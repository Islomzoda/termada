// Package dashboard serves the embedded local web UI (spec §8.1). The SPA is a
// single dependency-free HTML/JS file compiled into the binary via embed.
package dashboard

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
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
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err)
	}
	hash := sha256.New()
	if err := fs.WalkDir(sub, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := fs.ReadFile(sub, path)
		if readErr != nil {
			return readErr
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write(data)
		return nil
	}); err != nil {
		panic(err)
	}
	assetVersion := hex.EncodeToString(hash.Sum(nil))[:12]
	index = bytes.ReplaceAll(index, []byte("__ASSET_VERSION__"), []byte(assetVersion))
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
			if r.URL.Query().Get("v") == assetVersion {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; font-src 'self' data:; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	})
}
