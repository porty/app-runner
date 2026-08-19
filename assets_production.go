//go:build production

package main

import (
	"embed"
	"io/fs"
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

func productionFrontend() fs.FS {
	frontend, err := fs.Sub(embeddedFrontend, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return frontend
}
