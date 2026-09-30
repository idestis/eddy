package mcp

import (
	"context"
	"encoding/json"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/threads"
)

// CreateInput is threads.CreateInput.
type CreateInput = threads.CreateInput

// ThreadStore is the subset of *threads.Service used by MCP. The threads
// service filters every read by what p may currently get, checks who may
// resolve, and takes the author's subject and channel (Via) from p.
type ThreadStore interface {
	List(ctx context.Context, p identity.Principal, f store.ThreadFilter) ([]store.Thread, string, error)
	Get(ctx context.Context, p identity.Principal, id, cursor string, limit int) (store.Thread, []store.Message, string, error)
	Create(ctx context.Context, p identity.Principal, in CreateInput) (store.Thread, store.Message, error)
	Reply(ctx context.Context, p identity.Principal, id, body string, author store.Author, meta json.RawMessage) (store.Message, error)
	Resolve(ctx context.Context, p identity.Principal, id string) (store.Thread, error)
}

var _ ThreadStore = (*threads.Service)(nil)

// authorFor is the author of a thread write made over MCP: the token owner,
// badged "via mcp". Client is the untrusted MCP clientInfo name.
func authorFor(p identity.Principal) store.Author {
	return threads.AuthorFor(p, store.AuthorHuman)
}
