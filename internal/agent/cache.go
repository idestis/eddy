package agent

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
)

// podControllerIndex indexes Pods by "<namespace>/<name>" of their
// controlling ReplicaSet, so a ReplicaSet change can re-attribute its Pods.
const podControllerIndex = "eddy.controllerReplicaSet"

// podOwnerIndex indexes Pods by "<Kind>/<namespace>/<name>" of their
// controller, and of their Job by the job-name labels, for workload logs.
const podOwnerIndex = "eddy.owner"

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

	// served decides which inventory entries are watched kinds.
	served flux.Served
	// namespaces, when set, limits the Namespace objects surfaced to the
	// watched namespaces (EDDY_WATCH_NAMESPACES).
	namespaces map[string]bool
	// invByOwner maps a Kustomization id to the inventory-only rows it
	// contributed; invOwner is the reverse. A row listed by two
	// Kustomizations belongs to the first one.
	invByOwner map[string][]string
	invOwner   map[string]string

	// Job policy (jobs.go). jobs holds every watched Job by namespace and
	// id; only the ones the policy surfaces are in resources.
	jobPolicy JobPolicy
	now       func() time.Time
	jobs      map[string]map[string]*jobEntry
	jobsDirty map[string]struct{}
	jobExpiry map[string]time.Time
	// findings holds the job-buildup finding of each namespace with hidden
	// Jobs; findingsVer counts their changes.
	findings    map[string]model.Finding
	findingsVer uint64

	// podSubs are the workload log streams waiting for pod changes.
	subMu   sync.Mutex
	podSubs map[*podSub]struct{}
}

// NewCache creates informers for every served kind, in each of namespaces
// (all namespaces when empty). Call Start, then WaitForSync.
func NewCache(dyn dynamic.Interface, served flux.Served, namespaces []string, logger *slog.Logger) (*Cache, error) {
	c := &Cache{
		logger:    logger,
		resources: map[string]model.Resource{},
		pending:   map[string]struct{}{},
		rsOwners:  map[string]model.Ref{},

		served:     served,
		invByOwner: map[string][]string{},
		invOwner:   map[string]string{},

		jobPolicy: JobPolicy{}.withDefaults(),
		jobs:      map[string]map[string]*jobEntry{},
		jobsDirty: map[string]struct{}{},
		jobExpiry: map[string]time.Time{},
		findings:  map[string]model.Finding{},
		podSubs:   map[*podSub]struct{}{},
	}
	// Namespaced kinds are watched in each namespace; cluster-scoped kinds
	// once, cluster-wide. With a namespace list, Namespaces outside it are
	// not surfaced.
	scoped := len(namespaces) > 0
	if !scoped {
		namespaces = []string{metav1.NamespaceAll}
	} else {
		c.namespaces = map[string]bool{}
		for _, ns := range namespaces {
			c.namespaces[ns] = true
		}
	}
	watch := func(f dynamicinformer.DynamicSharedInformerFactory, clusterScoped bool) error {
		for _, k := range flux.All() {
			version, ok := served[k.Kind]
			if !ok || k.Namespaced == clusterScoped {
				continue
			}
			inf := f.ForResource(k.GVR(version)).Informer()
			if err := inf.SetTransform(trimTransform(k)); err != nil {
				return fmt.Errorf("agent: transform %s: %w", k.Kind, err)
			}
			if k.Kind == flux.KindPod {
				if err := inf.AddIndexers(cache.Indexers{podControllerIndex: podControllerKey, podOwnerIndex: podOwnerKeys}); err != nil {
					return fmt.Errorf("agent: index pods: %w", err)
				}
				c.pods = append(c.pods, inf)
			}
			if _, err := inf.AddEventHandler(c.handler(k)); err != nil {
				return fmt.Errorf("agent: watch %s: %w", k.Kind, err)
			}
			c.synced = append(c.synced, inf.HasSynced)
		}
		return nil
	}
	for i, ns := range namespaces {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, ns, nil)
		c.factories = append(c.factories, f)
		if err := watch(f, false); err != nil {
			return nil, err
		}
		if i == 0 && !scoped {
			if err := watch(f, true); err != nil {
				return nil, err
			}
		}
	}
	if scoped {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, metav1.NamespaceAll, nil)
		c.factories = append(c.factories, f)
		if err := watch(f, true); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// SetJobPolicy replaces the Job policy; zero fields keep their defaults.
// Call it before Start.
func (c *Cache) SetJobPolicy(p JobPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jobPolicy = p.withDefaults()
}

func (c *Cache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
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

// SyncProgress returns how many informers have listed once, of all of them.
func (c *Cache) SyncProgress() (synced, total int) {
	for _, s := range c.synced {
		if s() {
			synced++
		}
	}
	return synced, len(c.synced)
}

// Snapshot returns every summary, sorted by id, and clears pending changes:
// the snapshot already contains them.
func (c *Cache) Snapshot() []model.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushJobsLocked()
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
	c.flushJobsLocked()
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
		if k.Kind == flux.KindPod {
			defer c.notifyPods(u.GetNamespace())
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if k.Kind == flux.KindReplicaSet {
			c.setReplicaSetOwnerLocked(u.GetNamespace()+"/"+u.GetName(), controllerOf(u))
			return
		}
		if k.Group == flux.GroupCore && k.Kind == flux.KindNamespace && c.namespaces != nil && !c.namespaces[u.GetName()] {
			return
		}
		r := flux.Summarize(k, u, c.lookupLocked)
		if k.Kind == flux.KindJob {
			c.putJobLocked(r, flux.JobFactsOf(u))
			return
		}
		c.putLocked(r)
		if k.Kind == flux.KindKustomization {
			c.setInventoryLocked(r.ID, flux.InventoryOnly(u, c.served.Watches))
		}
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
			if k.Kind == flux.KindPod {
				defer c.notifyPods(u.GetNamespace())
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if k.Kind == flux.KindReplicaSet {
				c.setReplicaSetOwnerLocked(u.GetNamespace()+"/"+u.GetName(), nil)
				return
			}
			id := model.Ref{Group: k.Group, Kind: k.Kind, Namespace: u.GetNamespace(), Name: u.GetName()}.ID()
			if k.Kind == flux.KindJob {
				c.deleteJobLocked(u.GetNamespace(), id)
				return
			}
			if _, ok := c.resources[id]; ok {
				delete(c.resources, id)
				c.pending[id] = struct{}{}
			}
			if k.Kind == flux.KindKustomization {
				c.setInventoryLocked(id, nil)
			}
		},
	}
}

// setInventoryLocked replaces the inventory-only rows of the Kustomization
// owner with rows: new ones are added, rows it no longer lists are deleted.
// A row another Kustomization already contributed, or a real summary with
// the same id, is left alone.
func (c *Cache) setInventoryLocked(owner string, rows []model.Resource) {
	keep := make(map[string]bool, len(rows))
	var mine []string
	for _, r := range rows {
		if o, ok := c.invOwner[r.ID]; ok && o != owner {
			continue
		}
		if cur, ok := c.resources[r.ID]; ok && !cur.InventoryOnly {
			continue
		}
		keep[r.ID] = true
		mine = append(mine, r.ID)
		c.invOwner[r.ID] = owner
		c.putLocked(r)
	}
	for _, id := range c.invByOwner[owner] {
		if keep[id] || c.invOwner[id] != owner {
			continue
		}
		delete(c.invOwner, id)
		if cur, ok := c.resources[id]; ok && cur.InventoryOnly {
			delete(c.resources, id)
			c.pending[id] = struct{}{}
		}
	}
	if len(mine) == 0 {
		delete(c.invByOwner, owner)
		return
	}
	c.invByOwner[owner] = mine
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
