// Package devlocal holds the shared pieces of Eddy's "local mode" developer
// workflow: turning kubeconfig contexts into cluster names, deciding which
// contexts stay read-only, and writing the generated .dev/hub.yaml.
//
// Everything except this comment is compiled only with -tags dev, so release
// binaries contain none of it. See docs/development.md.
package devlocal
