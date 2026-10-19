package webassets

import (
	"embed"
	"io/fs"
)

// Build the web app and stage its output before compiling the core binary.
//
//go:embed site
var assets embed.FS

func Files() (fs.FS, error) {
	return fs.Sub(assets, "site")
}
