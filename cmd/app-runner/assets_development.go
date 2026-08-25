//go:build !production

package main

import "io/fs"

func productionFrontend() fs.FS {
	return nil
}
