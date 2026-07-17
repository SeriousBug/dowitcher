// Package web embeds the compiled Vite SPA build so it ships inside the single
// Go binary. The dist directory is produced by `pnpm build` before `go build`.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// DistFS returns the built SPA rooted so index.html is at the root.
func DistFS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
