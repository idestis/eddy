package agent

import (
	"maps"
	"testing"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
)

// Cluster-scoped kinds are watched once, cluster-wide, even with a
// namespace list; Namespaces outside the list are not surfaced; preset
// kinds are watched only when served (after ForPresets).
func TestCacheClusterScopedAndPresetKinds(t *testing.T) {
	served := maps.Clone(testServed)
	for _, k := range []string{flux.KindNamespace, flux.KindStorageClass, flux.KindNodePool, flux.KindExternalSecret} {
		kd, _ := flux.KindByName(k)
		served[k] = kd.Versions[0]
	}
	nsApps := u("v1", "Namespace", "", "apps", nil)
	nsApps.Object["status"] = map[string]any{"phase": "Active"}
	nsOther := u("v1", "Namespace", "", "other", nil)
	sc := u("storage.k8s.io/v1", "StorageClass", "", "gp3", nil)
	sc.Object["provisioner"] = "ebs.csi.aws.com"
	pool := u("karpenter.sh/v1", "NodePool", "", "default", map[string]any{"limits": map[string]any{"cpu": "100"}})
	pool.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	esApps := u("external-secrets.io/v1", "ExternalSecret", "apps", "db", map[string]any{"refreshInterval": "1h"})
	esOther := u("external-secrets.io/v1", "ExternalSecret", "other", "db", nil)

	c, _ := startCache(t, served.ForPresets([]string{flux.PresetKarpenter}), []string{"apps"}, nsApps, nsOther, sc, pool, esApps, esOther)
	var snap map[string]model.Resource
	waitFor(t, "cluster-scoped kinds", func() bool {
		snap = byID(c.Snapshot())
		_, a := snap["/Namespace//apps"]
		_, b := snap["storage.k8s.io/StorageClass//gp3"]
		_, p := snap["karpenter.sh/NodePool//default"]
		return a && b && p
	})
	if _, ok := snap["/Namespace//other"]; ok {
		t.Error("a namespace outside the watch list is surfaced")
	}
	for id := range snap {
		if r := snap[id]; r.Kind == flux.KindExternalSecret {
			t.Errorf("ExternalSecret watched with its preset off: %s", id)
		}
	}
	if p := snap["karpenter.sh/NodePool//default"]; p.Status != model.StatusReady || p.Project != "karpenter" {
		t.Errorf("NodePool %+v", p)
	}

	c, _ = startCache(t, served.ForPresets(flux.Presets()), nil, esApps, esOther, nsOther)
	waitFor(t, "external secrets in every namespace", func() bool {
		snap = byID(c.Snapshot())
		_, a := snap["external-secrets.io/ExternalSecret/apps/db"]
		_, b := snap["external-secrets.io/ExternalSecret/other/db"]
		_, n := snap["/Namespace//other"]
		return a && b && n
	})
	if es := snap["external-secrets.io/ExternalSecret/apps/db"]; es.Interval != "1h" || es.Project != "external-secrets" {
		t.Errorf("ExternalSecret %+v", es)
	}
}
