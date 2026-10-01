package flux

import (
	"fmt"
	"slices"
	"testing"

	"github.com/idestis/eddy/internal/model"
)

func TestSummarizeDependsOn(t *testing.T) {
	ks := func(ns, name string) model.Ref {
		return model.Ref{Group: GroupKustomize, Kind: KindKustomization, Namespace: ns, Name: name}
	}
	hr := func(ns, name string) model.Ref {
		return model.Ref{Group: GroupHelm, Kind: KindHelmRelease, Namespace: ns, Name: name}
	}
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		deps       []any
		want       []model.Ref
	}{
		{"kustomization, namespace defaults to the object's", "kustomize.toolkit.fluxcd.io/v1", KindKustomization,
			[]any{obj{"name": "infra"}}, []model.Ref{ks("apps", "infra")}},
		{"kustomization, cross-namespace", "kustomize.toolkit.fluxcd.io/v1", KindKustomization,
			[]any{obj{"name": "infra", "namespace": "flux-system"}, obj{"name": "crds"}},
			[]model.Ref{ks("flux-system", "infra"), ks("apps", "crds")}},
		{"helmrelease, same and other namespace", "helm.toolkit.fluxcd.io/v2", KindHelmRelease,
			[]any{obj{"name": "cert-manager", "namespace": "cert-manager"}, obj{"name": "redis"}},
			[]model.Ref{hr("cert-manager", "cert-manager"), hr("apps", "redis")}},
		{"duplicates and empty names are dropped", "helm.toolkit.fluxcd.io/v2", KindHelmRelease,
			[]any{obj{"name": "redis"}, obj{"name": "redis", "namespace": "apps"}, obj{"namespace": "x"}, "junk"},
			[]model.Ref{hr("apps", "redis")}},
		{"none", "kustomize.toolkit.fluxcd.io/v1", KindKustomization, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := obj{}
			if tt.deps != nil {
				spec["dependsOn"] = tt.deps
			}
			k := mustKind(t, tt.kind)
			u := newObj(tt.apiVersion, tt.kind, "apps", "web", spec, nil)
			Trim(k, u)
			r := Summarize(k, u, nil)
			if !slices.Equal(r.DependsOn, tt.want) {
				t.Fatalf("dependsOn = %+v, want %+v", r.DependsOn, tt.want)
			}
		})
	}
}

func TestDependsOnCapAndTrim(t *testing.T) {
	var deps []any
	for i := range model.MaxDependsOn + 8 {
		deps = append(deps, obj{"name": fmt.Sprintf("d%d", i), "readyExpr": "dep.status.x == 'y'"})
	}
	k := mustKind(t, KindKustomization)
	u := newObj("kustomize.toolkit.fluxcd.io/v1", KindKustomization, "apps", "web", obj{"dependsOn": deps}, nil)
	Trim(k, u)
	trimmed := maps(u.Object, "spec", "dependsOn")
	if len(trimmed) != model.MaxDependsOn {
		t.Fatalf("trimmed %d entries, want %d", len(trimmed), model.MaxDependsOn)
	}
	if _, ok := trimmed[0]["readyExpr"]; ok {
		t.Fatal("trim kept readyExpr")
	}
	if r := Summarize(k, u, nil); len(r.DependsOn) != model.MaxDependsOn {
		t.Fatalf("summary has %d entries", len(r.DependsOn))
	}
	// Summarize caps on its own as well (untrimmed objects).
	u = newObj("kustomize.toolkit.fluxcd.io/v1", KindKustomization, "apps", "web", obj{"dependsOn": deps}, nil)
	if r := Summarize(k, u, nil); len(r.DependsOn) != model.MaxDependsOn {
		t.Fatalf("summary has %d entries", len(r.DependsOn))
	}
}

func TestSummarizeDependencyNotReady(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		conds      []any
		wantMsg    string
	}{
		{"kustomization waiting", "kustomize.toolkit.fluxcd.io/v1", KindKustomization,
			conds(cond("Ready", "False", "DependencyNotReady", "dependency 'flux-system/infra' is not ready")),
			"Waiting for flux-system/infra"},
		{"kustomization dependency missing", "kustomize.toolkit.fluxcd.io/v1", KindKustomization,
			conds(cond("Ready", "False", "DependencyNotReady", "dependency 'flux-system/infra' not found: kustomizations.kustomize.toolkit.fluxcd.io \"infra\" not found")),
			"Waiting for flux-system/infra (not found)"},
		{"kustomization readyExpr", "kustomize.toolkit.fluxcd.io/v1", KindKustomization,
			conds(cond("Ready", "False", "DependencyNotReady", "dependency 'apps/db' is not ready according to readyExpr eval")),
			"Waiting for apps/db"},
		{"helmrelease keeps Reconciling=True while waiting", "helm.toolkit.fluxcd.io/v2", KindHelmRelease,
			conds(cond("Reconciling", "True", "Progressing", "Fulfilling prerequisites"),
				cond("Ready", "False", "DependencyNotReady", "dependency 'cert-manager/cert-manager' is not ready")),
			"Waiting for cert-manager/cert-manager"},
		{"helmrelease dependency missing", "helm.toolkit.fluxcd.io/v2", KindHelmRelease,
			conds(cond("Ready", "False", "DependencyNotReady", "unable to get 'apps/redis' dependency: helmreleases.helm.toolkit.fluxcd.io \"redis\" not found")),
			"Waiting for apps/redis (not found)"},
		{"unparsable message", "helm.toolkit.fluxcd.io/v2", KindHelmRelease,
			conds(cond("Ready", "False", "DependencyNotReady", "something else")),
			"Waiting for dependencies"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newObj(tt.apiVersion, tt.kind, "apps", "web", nil, obj{"conditions": tt.conds})
			r := Summarize(mustKind(t, tt.kind), u, nil)
			if r.Status != model.StatusReconciling || !r.Blocked || r.Message != tt.wantMsg {
				t.Fatalf("got %s blocked=%v %q, want reconciling blocked %q", r.Status, r.Blocked, r.Message, tt.wantMsg)
			}
		})
	}

	t.Run("suspend and stalled win, other failures are not blocked", func(t *testing.T) {
		k := mustKind(t, KindKustomization)
		waiting := conds(cond("Ready", "False", "DependencyNotReady", "dependency 'a/b' is not ready"))
		u := newObj("kustomize.toolkit.fluxcd.io/v1", KindKustomization, "apps", "web", obj{"suspend": true}, obj{"conditions": waiting})
		if r := Summarize(k, u, nil); r.Status != model.StatusSuspended || r.Blocked {
			t.Fatalf("suspended: got %s blocked=%v", r.Status, r.Blocked)
		}
		u = newObj("kustomize.toolkit.fluxcd.io/v1", KindKustomization, "apps", "web", nil,
			obj{"conditions": conds(cond("Ready", "False", "BuildFailed", "x"))})
		if r := Summarize(k, u, nil); r.Status != model.StatusFailed || r.Blocked {
			t.Fatalf("failed: got %s blocked=%v", r.Status, r.Blocked)
		}
	})
}

func TestHelmReleaseChartRefSource(t *testing.T) {
	k := mustKind(t, KindHelmRelease)
	tests := []struct {
		name string
		spec obj
		want model.Ref
	}{
		{"chartRef OCIRepository, namespace default", obj{"chartRef": obj{"kind": "OCIRepository", "name": "podinfo"}},
			model.Ref{Group: GroupSource, Kind: KindOCIRepository, Namespace: "apps", Name: "podinfo"}},
		{"chartRef HelmChart in another namespace", obj{"chartRef": obj{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmChart", "name": "c", "namespace": "flux-system"}},
			model.Ref{Group: GroupSource, Kind: KindHelmChart, Namespace: "flux-system", Name: "c"}},
		{"chart template", obj{"chart": obj{"spec": obj{"chart": "redis", "sourceRef": obj{"kind": "HelmRepository", "name": "bitnami"}}}},
			model.Ref{Group: GroupSource, Kind: KindHelmRepository, Namespace: "apps", Name: "bitnami"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newObj("helm.toolkit.fluxcd.io/v2", KindHelmRelease, "apps", "web", tt.spec, nil)
			Trim(k, u)
			r := Summarize(k, u, nil)
			if r.Source == nil || *r.Source != tt.want {
				t.Fatalf("source = %+v, want %+v", r.Source, tt.want)
			}
		})
	}
}
