// Package version exposes the build version, set with -ldflags at build time.
package version

// Version is overridden by -X github.com/eddy-gitops/eddy/internal/version.Version=v0.1.0.
var Version = "dev"
