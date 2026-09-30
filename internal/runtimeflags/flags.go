// Package runtimeflags provides kill switches that operators can flip without
// a restart by editing the eddy-runtime ConfigMap, which is mounted as a file.
// Callers must check Current() on every request.
package runtimeflags

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"sigs.k8s.io/yaml"
)

// Flags are the runtime switches. A missing file or key keeps the default.
type Flags struct {
	AIEnabled    bool `json:"aiEnabled"`
	MCPEnabled   bool `json:"mcpEnabled"`
	MCPWrites    bool `json:"mcpWrites"`
	MCPAllowLogs bool `json:"mcpAllowLogs"`
}

// Source returns the current flags.
type Source interface {
	Current() Flags
}

// Static is a fixed Source, for tests and for installs without the file.
type Static Flags

func (s Static) Current() Flags { return Flags(s) }

// File polls a YAML file and serves the last good value.
type File struct {
	path     string
	defaults Flags
	cur      atomic.Pointer[Flags]
	last     []byte
	log      *slog.Logger
}

// NewFile reads path once and returns a Source. Call Run to keep it fresh.
// The runtime file can only turn features off: a flag is on only when both
// the default (from hub.yaml) and the file allow it.
func NewFile(path string, defaults Flags, log *slog.Logger) *File {
	f := &File{path: path, defaults: defaults, log: log}
	f.cur.Store(&defaults)
	f.reload()
	return f
}

func (f *File) Current() Flags { return *f.cur.Load() }

// Run polls every interval until ctx is done. ConfigMap volume updates are
// eventually consistent (about a minute), so polling is sufficient.
func (f *File) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.reload()
		}
	}
}

func (f *File) reload() {
	b, err := os.ReadFile(f.path)
	if err != nil {
		if !os.IsNotExist(err) {
			f.log.Warn("runtime flags: read failed, keeping previous value", "path", f.path, "err", err)
		}
		return
	}
	if bytes.Equal(b, f.last) {
		return
	}
	// Start from "everything allowed", then AND with the defaults below.
	file := Flags{AIEnabled: true, MCPEnabled: true, MCPWrites: true, MCPAllowLogs: true}
	if err := yaml.Unmarshal(b, &file); err != nil {
		f.log.Warn("runtime flags: parse failed, keeping previous value", "path", f.path, "err", err)
		return
	}
	next := Flags{
		AIEnabled:    f.defaults.AIEnabled && file.AIEnabled,
		MCPEnabled:   f.defaults.MCPEnabled && file.MCPEnabled,
		MCPWrites:    f.defaults.MCPWrites && file.MCPWrites,
		MCPAllowLogs: f.defaults.MCPAllowLogs && file.MCPAllowLogs,
	}
	f.last = b
	f.cur.Store(&next)
	f.log.Info("runtime flags loaded", "flags", next)
}
