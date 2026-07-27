// Package build holds the CLI's identity
package build

// Name is the binary name. Changing it changes the config directory too
const Name = "whg"

// Version is stamped at release time with -ldflags by the goreleaser pipeline.
// Local builds report "dev".
var Version = "dev"
