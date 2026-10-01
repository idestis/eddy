package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/store"
)

// answerRefs returns meta.refs of the answer in resp.
func answerRefs(t *testing.T, msg store.Message) []store.ResourceRef {
	t.Helper()
	var meta struct {
		Refs *[]store.ResourceRef `json:"refs"`
	}
	if err := json.Unmarshal(msg.Meta, &meta); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if meta.Refs == nil {
		t.Fatalf("meta has no refs: %s", msg.Meta)
	}
	return *meta.Refs
}

func resource(group, kind, ns, name string) model.Resource {
	return model.Resource{Ref: model.Ref{Group: group, Kind: kind, Namespace: ns, Name: name}, Status: model.StatusReady}
}

func TestAnswerRefsComeFromToolResults(t *testing.T) {
	hr := resource("helm.toolkit.fluxcd.io", "HelmRelease", "apps", "podinfo")
	dep := resource("apps", "Deployment", "apps", "podinfo")
	dep.ID = dep.Ref.ID()
	pool := resource("karpenter.sh", "NodePool", "", "apps-amd64")
	secret := resource("helm.toolkit.fluxcd.io", "HelmRelease", "apps", "secret")

	h := newHarness(t, config.AI{},
		Response{StopReason: StopToolUse, Content: []Block{
			toolUse("t1", "get_resource", `{"kind":"HelmRelease","namespace":"apps","name":"podinfo"}`),
			toolUse("t2", "search_resources", `{"cluster":"dev"}`),
			toolUse("t3", "get_events", `{"kind":"HelmRelease","namespace":"apps","name":"podinfo"}`),
			toolUse("t4", "get_resource", `{"kind":"HelmRelease","namespace":"apps","name":"secret"}`),
			toolUse("t5", "get_resource", `{"kind":"HelmRelease","namespace":"apps","name":"missing"}`),
		}},
		// The answer names objects no tool returned and writes its own URL.
		Response{StopReason: StopEndTurn, Content: []Block{TextBlock(
			"`HelmRelease/apps/podinfo` is fine; `HelmRelease/apps/ghost` and `HelmRelease/apps/secret` are not. " +
				"See [ghost](/c/prod/r/HelmRelease/apps/ghost) or `NodePool/apps-amd64`.")}},
	)
	h.fleet.add("prod", hr)
	h.fleet.add("prod", secret)
	h.fleet.forbidden[secret.Ref.ID()] = true
	h.fleet.add("dev", pool)
	h.fleet.children = map[string][]model.Resource{"prod|" + hr.Ref.ID(): {dep}}

	resp, err := h.svc.Ask(context.Background(), alice, AskRequest{Context: refs(ksStoreRef), Question: "what does podinfo run?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	want := []store.ResourceRef{
		ksStoreRef, // visible context first
		{Cluster: "prod", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "apps", Name: "podinfo"},
		{Cluster: "prod", Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "podinfo"},
		{Cluster: "dev", Group: "karpenter.sh", Kind: "NodePool", Name: "apps-amd64"},
	}
	got := answerRefs(t, resp.Message)
	if !slices.Equal(got, want) {
		t.Errorf("refs = %+v\nwant %+v", got, want)
	}
	// The stored message carries the same refs.
	msgs := h.messages(t, resp.Chat.ID)
	if stored := answerRefs(t, msgs[len(msgs)-1]); !slices.Equal(stored, want) {
		t.Errorf("stored refs = %+v", stored)
	}
	for _, r := range got {
		if r.Name == "ghost" || r.Name == "secret" || r.Name == "missing" {
			t.Errorf("ref %+v did not come from a successful tool result", r)
		}
	}
}

func TestAnswerRefsWithoutTools(t *testing.T) {
	h := newHarness(t, config.AI{}, Response{StopReason: StopEndTurn, Content: []Block{TextBlock("`Kustomization/flux-system/other` is broken.")}})
	resp, err := h.svc.Ask(context.Background(), alice, AskRequest{Question: "anything broken?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := answerRefs(t, resp.Message); len(got) != 0 {
		t.Errorf("refs = %+v, want none", got)
	}
}

func TestAnswerRefsExcludeHiddenContext(t *testing.T) {
	hidden := store.ResourceRef{Cluster: "prod", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "apps", Name: "hidden"}
	cluster := store.ResourceRef{Cluster: "prod"}
	h := newHarness(t, config.AI{}, Response{StopReason: StopEndTurn, Content: []Block{TextBlock("ok")}})
	h.fleet.forbidden[toModelRef(hidden).ID()] = true

	resp, err := h.svc.Ask(context.Background(), alice, AskRequest{Context: refs(hidden, ksStoreRef, cluster), Question: "status?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := answerRefs(t, resp.Message); !slices.Equal(got, []store.ResourceRef{ksStoreRef}) {
		t.Errorf("refs = %+v, want only the visible resource", got)
	}
}

func TestAnswerRefsCapped(t *testing.T) {
	h := newHarness(t, config.AI{},
		Response{StopReason: StopToolUse, Content: []Block{
			toolUse("t1", "get_resource", `{"kind":"Kustomization","namespace":"flux-system","name":"apps"}`),
			toolUse("t2", "search_resources", `{"cluster":"prod","limit":100}`),
			toolUse("t3", "search_resources", `{"cluster":"dev","limit":100}`),
		}},
		Response{StopReason: StopEndTurn, Content: []Block{TextBlock("many")}},
	)
	var kids []model.Resource
	for i := range maxChildren {
		k := resource("", "ConfigMap", "apps", fmt.Sprintf("kid-%d", i))
		k.ID = k.Ref.ID()
		kids = append(kids, k)
	}
	h.fleet.children = map[string][]model.Resource{"prod|" + ksRef.ID(): kids}
	for i := range 150 {
		h.fleet.add("prod", resource("apps", "Deployment", "apps", fmt.Sprintf("p-%d", i)))
		h.fleet.add("dev", resource("apps", "Deployment", "apps", fmt.Sprintf("d-%d", i)))
	}

	resp, err := h.svc.Ask(context.Background(), alice, AskRequest{Context: refs(ksStoreRef), Question: "list everything"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := answerRefs(t, resp.Message)
	if len(got) != MaxAnswerRefs {
		t.Fatalf("refs = %d, want %d", len(got), MaxAnswerRefs)
	}
	seen := map[store.ResourceRef]bool{}
	for _, r := range got {
		if seen[r] {
			t.Errorf("duplicate ref %+v", r)
		}
		seen[r] = true
	}
	if got[0] != ksStoreRef {
		t.Errorf("first ref = %+v, want the context", got[0])
	}
}

func TestRefSet(t *testing.T) {
	var s refSet
	if s.items() == nil {
		t.Error("empty set must encode as []")
	}
	a := store.ResourceRef{Cluster: "c", Kind: "Pod", Namespace: "n", Name: "a"}
	s.add(a)
	s.add(a)
	s.add(store.ResourceRef{Cluster: "c"})              // a whole cluster is not a resource
	s.add(store.ResourceRef{Kind: "Pod", Name: "x"})    // no cluster
	s.add(store.ResourceRef{Cluster: "c", Kind: "Pod"}) // no name
	if got := s.items(); len(got) != 1 || got[0] != a {
		t.Errorf("items = %+v", got)
	}
	for i := range MaxAnswerRefs + 10 {
		s.add(store.ResourceRef{Cluster: "c", Kind: "Pod", Name: fmt.Sprint(i)})
	}
	if n := len(s.items()); n != MaxAnswerRefs {
		t.Errorf("len = %d, want %d", n, MaxAnswerRefs)
	}
}
