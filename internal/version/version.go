// Package version exposes the build version, set with -ldflags at build time.
package version

// Version is overridden by -X github.com/idestis/eddy/internal/version.Version=v1.0.0.
var Version = "dev"
