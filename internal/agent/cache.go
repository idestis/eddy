package agent

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/eddy-gitops/eddy/internal/flux"
	"github.com/eddy-gitops/eddy/internal/model"
)

// podControllerIndex indexes Pods by "<namespace>/<name>" of their
// controlling ReplicaSet, so a ReplicaSet change can re-attribute its Pods.
const podControllerIndex = "eddy.controllerReplicaSet"

// Cache watches every served kind with dynamic informers and keeps the
// summary of each object, plus the set of ids changed since the last Drain.
// Informers store trimmed objects (flux.Trim), never raw ones.
type Cache struct {
	logger    *slog.Logger
	factories []dynamicinformer.DynamicSharedInformerFactory
	synced    []cache.InformerSynced
	pods      []cache.SharedIndexInformer

	mu        sync.Mutex
	resources map[string]model.Resource
	pending   map[string]struct{}
	// rsOwners maps "<namespace>/<name>" of a ReplicaSet to its controller.
	rsOwners map[string]model.Ref
}

// NewCache creates informers for every served kind, in each of namespaces
// (all namespaces when empty). Call Start, then WaitForSync.
func NewCache(dyn dynamic.Interface, served flux.Served, namespaces []string, logger *slog.Logger) (*Cache, error) {
	c := &Cache{
		logger:    logger,
		resources: map[string]model.Resource{},
		pending:   map[string]struct{}{},
		rsOwners:  map[string]model.Ref{},
	}
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	for _, ns := range namespaces {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, ns, nil)
		c.factories = append(c.factories, f)
		for _, k := range flux.All() {
			version, ok := served[k.Kind]
			if !ok {
				continue
			}
			inf := f.ForResource(k.GVR(version)).Informer()
			if err := inf.SetTransform(trimTransform(k)); err != nil {
				return nil, fmt.Errorf("agent: transform %s: %w", k.Kind, err)
			}
			if k.Kind == flux.KindPod {
				if err := inf.AddIndexers(cache.Indexers{podControllerIndex: podControllerKey}); err != nil {
					return nil, fmt.Errorf("agent: index pods: %w", err)
				}
				c.pods = append(c.pods, inf)
			}
			if _, err := inf.AddEventHandler(c.handler(k)); err != nil {
				return nil, fmt.Errorf("agent: watch %s: %w", k.Kind, err)
			}
			c.synced = append(c.synced, inf.HasSynced)
		}
	}
	return c, nil
}

// Start runs the informers until ctx is done.
func (c *Cache) Start(ctx context.Context) {
	for _, f := range c.factories {
		f.Start(ctx.Done())
	}
}

// WaitForSync blocks until every informer has listed once, or ctx is done.
func (c *Cache) WaitForSync(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), c.synced...)
}

// Synced reports whether every informer has listed once.
func (c *Cache) Synced() bool {
	for _, s := range c.synced {
		if !s() {
			return false
		}
	}
	return true
}

// Snapshot returns every summary, sorted by id, and clears pending changes:
// the snapshot already contains them.
func (c *Cache) Snapshot() []model.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]model.Resource, 0, len(c.resources))
	for _, r := range c.resources {
		out = append(out, r)
	}
	clear(c.pending)
	slices.SortFunc(out, func(a, b model.Resource) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// Drain returns the changes since the last Snapshot or Drain.
func (c *Cache) Drain() (upserts []model.Resource, deletes []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.pending {
		if r, ok := c.resources[id]; ok {
			upserts = append(upserts, r)
		} else {
			deletes = append(deletes, id)
		}
	}
	clear(c.pending)
	slices.SortFunc(upserts, func(a, b model.Resource) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(deletes)
	return upserts, deletes
}

func trimTransform(k flux.Kind) cache.TransformFunc {
	return func(obj any) (any, error) {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			flux.Trim(k, u)
		}
		return obj, nil
	}
}

func podControllerKey(obj any) ([]string, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, nil
	}
	if ref := metav1.GetControllerOfNoCopy(u); ref != nil && ref.Kind == flux.KindReplicaSet {
		return []string{u.GetNamespace() + "/" + ref.Name}, nil
	}
	return nil, nil
}

func (c *Cache) handler(k flux.Kind) cache.ResourceEventHandlerFuncs {
	upsert := func(obj any) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if k.Kind == flux.KindReplicaSet {
			c.setReplicaSetOwnerLocked(u.GetNamespace()+"/"+u.GetName(), controllerOf(u))
			return
		}
		c.putLocked(flux.Summarize(k, u, c.lookupLocked))
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    upsert,
		UpdateFunc: func(_, obj any) { upsert(obj) },
		DeleteFunc: func(obj any) {
			if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = t.Obj
			}
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if k.Kind == flux.KindReplicaSet {
				c.setReplicaSetOwnerLocked(u.GetNamespace()+"/"+u.GetName(), nil)
				return
			}
			id := model.Ref{Group: k.Group, Kind: k.Kind, Namespace: u.GetNamespace(), Name: u.GetName()}.ID()
			if _, ok := c.resources[id]; ok {
				delete(c.resources, id)
				c.pending[id] = struct{}{}
			}
		},
	}
}

// putLocked stores r and marks it changed unless only its resourceVersion
// moved, which avoids a delta for every status heartbeat.
func (c *Cache) putLocked(r model.Resource) {
	old, ok := c.resources[r.ID]
	c.resources[r.ID] = r
	if ok {
		old.ResourceVersion = r.ResourceVersion
		if reflect.DeepEqual(old, r) {
			return
		}
	}
	c.pending[r.ID] = struct{}{}
}

// lookupLocked is the flux.OwnerLookup for Pods: it resolves a ReplicaSet to
// the Deployment that controls it.
func (c *Cache) lookupLocked(ref model.Ref) (model.Ref, bool) {
	if ref.Group != flux.GroupApps || ref.Kind != flux.KindReplicaSet {
		return model.Ref{}, false
	}
	o, ok := c.rsOwners[ref.Namespace+"/"+ref.Name]
	return o, ok
}

// setReplicaSetOwnerLocked records a ReplicaSet's controller and, when it
// changes, re-summarizes the Pods of that ReplicaSet.
func (c *Cache) setReplicaSetOwnerLocked(key string, owner *model.Ref) {
	old, had := c.rsOwners[key]
	switch {
	case owner == nil && !had:
		return
	case owner != nil && had && old == *owner:
		return
	case owner == nil:
		delete(c.rsOwners, key)
	default:
		c.rsOwners[key] = *owner
	}
	pod, _ := flux.KindByName(flux.KindPod)
	for _, inf := range c.pods {
		objs, err := inf.GetIndexer().ByIndex(podControllerIndex, key)
		if err != nil {
			c.logger.Warn("agent: look up pods of replicaset", "replicaset", key, "error", err)
			continue
		}
		for _, o := range objs {
			if u, ok := o.(*unstructured.Unstructured); ok {
				c.putLocked(flux.Summarize(pod, u, c.lookupLocked))
			}
		}
	}
}

func controllerOf(u *unstructured.Unstructured) *model.Ref {
	ref := metav1.GetControllerOfNoCopy(u)
	if ref == nil {
		return nil
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return nil
	}
	return &model.Ref{Group: gv.Group, Kind: ref.Kind, Namespace: u.GetNamespace(), Name: ref.Name}
}
