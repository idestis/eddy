package agent

import (
	"context"
	"log/slog"
	"slices"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/protocol"
)

// selfTestUser is the user of the SubjectAccessReview the agent creates to
// check that it may create reviews at all. The answer does not matter.
const selfTestUser = "eddy-agent-selftest"

// diagnose runs the agent's self-checks for the hub's connection checklist
// (ADR-0005): served kinds, SubjectAccessReview access, whether group
// impersonation is pinned, and informer sync progress.
func diagnose(ctx context.Context, kube kubernetes.Interface, served flux.Served, c *Cache, log *slog.Logger) *protocol.Diagnostics {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := &protocol.Diagnostics{}
	for k := range served {
		d.ServedKinds = append(d.ServedKinds, k)
	}
	slices.Sort(d.ServedKinds)
	if c != nil {
		d.InformersSynced, d.InformersTotal = c.SyncProgress()
	}

	_, err := kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:               selfTestUser,
			ResourceAttributes: &authv1.ResourceAttributes{Verb: "get", Resource: "namespaces"},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		d.SARError = truncateText(err.Error(), 256)
		log.Warn("agent: cannot create SubjectAccessReviews; the hub cannot filter what users see", "error", err)
	} else {
		d.SAROK = true
	}

	// "May I impersonate system:masters?" A yes means the impersonate
	// verb on groups is not pinned with resourceNames (impersonation.groups).
	ssar, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{Verb: "impersonate", Resource: "groups", Name: "system:masters"},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		log.Warn("agent: impersonation self-check failed", "error", err)
	} else {
		pinned := !ssar.Status.Allowed
		d.ImpersonationPinned = &pinned
		if !pinned {
			log.Warn("agent: group impersonation is not pinned; set impersonation.groups in the eddy-agent chart")
		}
	}
	return d
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
