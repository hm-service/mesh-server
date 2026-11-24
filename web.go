package main

import (
	"embed"
	"io/fs"
)

// The frontend build is compiled into the binary; frontend/ itself never ships.
//
//go:embed all:frontend/dist
var frontendAssets embed.FS

// frontendFS is the embedded build, rooted at the Vite output directory.
func frontendFS() fs.FS {
	sub, err := fs.Sub(frontendAssets, "frontend/dist")
	if err != nil {
		// The embed pattern above fixes the prefix, so this cannot happen.
		panic(err)
	}
	return sub
}
