// Package version holds build metadata, set with -ldflags at build time.
package version

// Version is overridden with -ldflags "-X .../internal/version.Version=...".
var Version = "dev"
