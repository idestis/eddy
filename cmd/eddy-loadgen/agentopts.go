//go:build dev

package main

import "github.com/idestis/eddy/internal/agent"

// agentOptions are the fake agents' transport settings.
type agentOptions struct {
	deflate bool
}

func (o agentOptions) apply(s *agent.Session) { s.DisableCompression = !o.deflate }
