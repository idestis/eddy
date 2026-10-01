package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/idestis/eddy/internal/protocol"
)

// maxRulesError caps the evaluation and create error messages sent back.
const maxRulesError = 512

// rulesReviewer creates one SelfSubjectRulesReview in namespace through cl,
// which acts as the request identity.
type rulesReviewer func(ctx context.Context, cl Clients, namespace string) (*authorizationv1.SubjectRulesReviewStatus, error)

// rules answers OpRules: one SelfSubjectRulesReview per namespace, created
// through the impersonating client. Impersonation makes the API server
// evaluate the identity's own rules (with the system:authenticated group it
// adds to every impersonated user; the hub accounts for that). The agent's
// ServiceAccount rules are never reviewed. Reviews run under the same
// agent-wide semaphore as SubjectAccessReviews.
func (h *Handler) rules(ctx context.Context, id protocol.Identity, args protocol.RulesArgs) (any, error) {
	if len(args.Namespaces) == 0 {
		return protocol.RulesResult{Namespaces: []protocol.NamespaceRules{}}, nil
	}
	if len(args.Namespaces) > protocol.MaxRulesNamespaces {
		return nil, badRequest("at most %d namespaces per rules request", protocol.MaxRulesNamespaces)
	}
	seen := make(map[string]bool, len(args.Namespaces))
	for _, ns := range args.Namespaces {
		if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
			return nil, badRequest("invalid namespace %q", ns)
		}
		if seen[ns] {
			return nil, badRequest("namespace %q is listed twice", ns)
		}
		seen[ns] = true
	}
	cl, err := h.Impersonate(id)
	if err != nil {
		return nil, err
	}
	review := h.rulesReview
	if review == nil {
		review = selfSubjectRulesReview
	}
	out := make([]protocol.NamespaceRules, len(args.Namespaces))
	var wg sync.WaitGroup
	sem := h.sarSlots()
	for i, ns := range args.Namespaces {
		wg.Go(func() {
			out[i].Namespace = ns
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[i].Error = "agent: " + ctx.Err().Error()
				return
			}
			defer func() { <-sem }()
			st, err := review(ctx, cl, ns)
			if err != nil {
				out[i].Error = truncateText(fmt.Sprintf("agent: rules review in %s: %v", ns, err), maxRulesError)
				return
			}
			out[i] = namespaceRules(ns, st)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fitRules(out, yamlBudget)
	return protocol.RulesResult{Namespaces: out}, nil
}

// selfSubjectRulesReview creates a SelfSubjectRulesReview as cl's identity.
func selfSubjectRulesReview(ctx context.Context, cl Clients, namespace string) (*authorizationv1.SubjectRulesReviewStatus, error) {
	r := &authorizationv1.SelfSubjectRulesReview{Spec: authorizationv1.SelfSubjectRulesReviewSpec{Namespace: namespace}}
	res, err := cl.Kube.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, r, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return &res.Status, nil
}

// namespaceRules converts a review status, dropping the rules when there
// are more than MaxRulesPerNamespace.
func namespaceRules(ns string, st *authorizationv1.SubjectRulesReviewStatus) protocol.NamespaceRules {
	nr := protocol.NamespaceRules{
		Namespace:       ns,
		Incomplete:      st.Incomplete,
		EvaluationError: truncateText(st.EvaluationError, maxRulesError),
	}
	if len(st.ResourceRules) > protocol.MaxRulesPerNamespace {
		nr.Truncated = true
		return nr
	}
	nr.Rules = make([]protocol.ResourceRule, len(st.ResourceRules))
	for i, r := range st.ResourceRules {
		nr.Rules[i] = protocol.ResourceRule{
			Verbs:         slices.Clone(r.Verbs),
			APIGroups:     slices.Clone(r.APIGroups),
			Resources:     slices.Clone(r.Resources),
			ResourceNames: slices.Clone(r.ResourceNames),
		}
	}
	return nr
}

// fitRules keeps the encoded result within budget bytes: once the rules
// seen so far exceed it, later namespaces are sent Truncated without rules.
func fitRules(out []protocol.NamespaceRules, budget int) {
	used := 0
	for i := range out {
		b, err := json.Marshal(out[i])
		if err != nil || used+len(b) > budget {
			out[i].Rules = nil
			out[i].Truncated = true
			b, _ = json.Marshal(out[i])
		}
		used += len(b) + 1
	}
}
