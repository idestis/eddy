package hub

import (
	"errors"
	"fmt"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/protocol"
)

// ErrBadRequest marks malformed input detected by the hub (HTTP 400).
var ErrBadRequest = errors.New("hub: bad request")

// errUnavailable marks a transient failure such as a request timeout or an
// agent that is too busy (HTTP 503).
var errUnavailable = errors.New("hub: temporarily unavailable")

func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, fmt.Sprintf(format, args...))
}

// AgentError is an error an agent reported for a request. Code follows HTTP
// semantics (protocol.Error). It unwraps to the matching fleet error so that
// callers such as MCP and Ask AI can use errors.Is.
type AgentError struct {
	Cluster string
	Code    int
	Message string
}

func (e *AgentError) Error() string {
	return fmt.Sprintf("cluster %s: %s", e.Cluster, e.Message)
}

// Unwrap maps the code to a fleet or hub sentinel.
func (e *AgentError) Unwrap() error {
	switch e.Code {
	case 400:
		return ErrBadRequest
	case 403:
		return fleet.ErrForbidden
	case 404:
		return fleet.ErrNotFound
	case 503:
		return errUnavailable
	}
	return nil
}

func agentError(cluster string, pe *protocol.Error) error {
	if pe == nil {
		return nil
	}
	return &AgentError{Cluster: cluster, Code: pe.Code, Message: pe.Message}
}
