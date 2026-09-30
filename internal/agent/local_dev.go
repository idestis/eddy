//go:build dev

package agent

import (
	"context"
	"fmt"
	"log/slog"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/protocol"
)

// LocalOptions configure one local-mode session (dev builds only).
type LocalOptions struct {
	// Context is the kubeconfig context rc was built from.
	Context string
	// ReadOnly, when set, refuses every write with this message.
	ReadOnly string
}

// RunLocal runs the agent for one kubeconfig context on a developer machine.
// Unlike Run, it never impersonates: every read and action runs as rc's own
// identity, and access checks are SelfSubjectAccessReviews of that identity.
// The request identity is still validated by Policy (so system: identities
// are refused) but is otherwise ignored for Kubernetes calls. The caller has
// already checked the activation conditions (EDDY_DEV_MODE=1, --local and a
// loopback hub URL).
func RunLocal(ctx context.Context, cfg *config.Agent, rc *rest.Config, logger *slog.Logger, o LocalOptions) error {
	rc = rest.CopyConfig(rc)
	logger.Warn(fmt.Sprintf("local mode: every Eddy user acts as your kubeconfig identity for context %s", o.Context),
		"readOnly", o.ReadOnly != "")
	return run(ctx, cfg, rc, logger, func(h *Handler, hello *protocol.Hello, kube kubernetes.Interface, dyn dynamic.Interface) {
		configureLocal(h, hello, kube, dyn, o)
	})
}

// configureLocal switches a handler and Hello to local mode.
func configureLocal(h *Handler, hello *protocol.Hello, kube kubernetes.Interface, dyn dynamic.Interface, o LocalOptions) {
	own := Clients{Dynamic: dyn, Kube: kube}
	h.Impersonate = func(protocol.Identity) (Clients, error) { return own, nil }
	h.review = selfSubjectAccessReview(kube)
	h.readOnly = o.ReadOnly
	hello.Mode = protocol.ModeLocal
	hello.ReadOnly = o.ReadOnly != ""
	hello.Context = o.Context
}

// selfSubjectAccessReview answers access checks for the client's own
// identity; the hub-supplied identity is not consulted.
func selfSubjectAccessReview(kube kubernetes.Interface) accessReviewer {
	return func(ctx context.Context, _ protocol.Identity, c protocol.AccessCheck) (bool, error) {
		ssar := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: resourceAttributes(c),
		}}
		res, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Errorf("agent: self subject access review %s %s/%s: %w", c.Verb, c.Group, c.Resource, err)
		}
		return res.Status.Allowed && !res.Status.Denied, nil
	}
}
