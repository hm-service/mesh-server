// Package builtin holds the resources compiled into the binary.
package builtin

import _ "embed"

//go:embed peer.default.toml
var defaultTemplate string

// DefaultTemplate is the EasyTier configuration new devices start from when
// DATA_DIR/peer.default.toml does not exist. The file always wins when present.
func DefaultTemplate() string {
	return defaultTemplate
}
