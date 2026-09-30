package ai

import (
	"context"
	"encoding/json"

	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/store"
	"github.com/eddy-gitops/eddy/internal/threads"
)

// CreateInput is threads.CreateInput.
type CreateInput = threads.CreateInput

// ThreadStore is the subset of *threads.Service that Ask AI uses. The
// threads service applies visibility and RBAC checks for p, and takes the
// author's subject and channel (Via) from p.
type ThreadStore interface {
	Get(ctx context.Context, p identity.Principal, id, cursor string, limit int) (store.Thread, []store.Message, string, error)
	Create(ctx context.Context, p identity.Principal, in CreateInput) (store.Thread, store.Message, error)
	Reply(ctx context.Context, p identity.Principal, id, body string, author store.Author, meta json.RawMessage) (store.Message, error)
}

var _ ThreadStore = (*threads.Service)(nil)
