package agent

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
)

// KindResolver maps an API group and Kind outside the watched kind table
// (an object known only from a Kustomization inventory) to the resource the
// cluster serves, for impersonated yaml and events reads.
type KindResolver interface {
	// Resolve returns the preferred GroupVersionResource of (group, kind)
	// and whether it is namespaced. An unknown kind is a meta.NoKindMatchError
	// (meta.IsNoMatchError).
	Resolve(group, kind string) (gvr schema.GroupVersionResource, namespaced bool, err error)
}

// minRediscovery spaces discovery refreshes triggered by unknown kinds, so
// requests for a kind the cluster does not serve cannot hammer discovery.
const minRediscovery = 30 * time.Second

// discoveryResolver is a KindResolver over a RESTMapper backed by an
// in-memory discovery cache. A miss refreshes the cache (at most once per
// minRediscovery) and retries, so CRDs installed after the agent started
// resolve without a restart.
type discoveryResolver struct {
	mapper *restmapper.DeferredDiscoveryRESTMapper
	now    func() time.Time
	every  time.Duration

	mu        sync.Mutex
	lastReset time.Time
}

// NewDiscoveryResolver returns a KindResolver that uses dc, the agent's own
// discovery client (discovery reveals no object data).
func NewDiscoveryResolver(dc discovery.DiscoveryInterface) KindResolver {
	return newDiscoveryResolver(dc, time.Now, minRediscovery)
}

func newDiscoveryResolver(dc discovery.DiscoveryInterface, now func() time.Time, every time.Duration) *discoveryResolver {
	return &discoveryResolver{
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc)),
		now:    now,
		every:  every,
	}
}

func (d *discoveryResolver) Resolve(group, kind string) (schema.GroupVersionResource, bool, error) {
	gk := schema.GroupKind{Group: group, Kind: kind}
	m, err := d.mapper.RESTMapping(gk)
	if err != nil && meta.IsNoMatchError(err) && d.mayRefresh() {
		d.mapper.Reset()
		m, err = d.mapper.RESTMapping(gk)
	}
	if err != nil {
		if meta.IsNoMatchError(err) {
			return schema.GroupVersionResource{}, false, err
		}
		return schema.GroupVersionResource{}, false, fmt.Errorf("agent: map %s: %w", gk, err)
	}
	return m.Resource, m.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// mayRefresh reports whether a refresh is due, and records it.
func (d *discoveryResolver) mayRefresh() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if !d.lastReset.IsZero() && now.Sub(d.lastReset) < d.every {
		return false
	}
	d.lastReset = now
	return true
}
