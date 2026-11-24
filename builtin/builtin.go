// Package builtin holds the resources compiled into the binary.
package builtin

import _ "embed"

//go:embed peer.default.toml
var defaultTemplate string

// Asset is one resource carried by the binary. Manifest exists so an operator
// can see what a build ships with.
type Asset struct {
	Name string
	Size int
}

// Manifest lists the embedded resources in the order they are declared.
var Manifest = []Asset{
	{Name: "peer.default.toml", Size: len(defaultTemplate)},
}

// DefaultTemplate is the EasyTier configuration new devices start from when
// DATA_DIR/peer.default.toml does not exist. The file always wins when present.
func DefaultTemplate() string {
	return defaultTemplate
}
