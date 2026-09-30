package agent

import (
	"cmp"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
)

// Job name labels on a Job's pods: the current one and the legacy one.
const (
	labelJobName       = "batch.kubernetes.io/job-name"
	labelLegacyJobName = "job-name"
)

// PodInfo is a pod of a workload, as the cache knows it.
type PodInfo struct {
	Name       string
	Containers []string
	Status     model.Status
	CreatedAt  time.Time
}

// PodSource finds the pods of a workload. The cache implements it from its
// informers; nothing here reads the API server, so it must never be used to
// decide what a user may see, only which pods to ask about as the user.
type PodSource interface {
	// WorkloadPods returns the current pods of a Deployment, StatefulSet,
	// DaemonSet, ReplicaSet or Job, newest first.
	WorkloadPods(t model.Ref) []PodInfo
	// WatchPods returns a channel that receives a value (coalesced) after
	// any pod in namespace changes, and a function that stops the watch.
	WatchPods(namespace string) (<-chan struct{}, func())
}

var _ PodSource = (*Cache)(nil)

func ownerKey(kind, namespace, name string) string { return kind + "/" + namespace + "/" + name }

// podOwnerKeys is the podOwnerIndex function.
func podOwnerKeys(obj any) ([]string, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, nil
	}
	var keys []string
	if ref := metav1.GetControllerOfNoCopy(u); ref != nil {
		keys = append(keys, ownerKey(ref.Kind, u.GetNamespace(), ref.Name))
	}
	l := u.GetLabels()
	for _, lbl := range []string{labelJobName, labelLegacyJobName} {
		if v := l[lbl]; v != "" {
			if k := ownerKey(flux.KindJob, u.GetNamespace(), v); !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	return keys, nil
}

// WorkloadPods resolves a workload to its pods through the ownership chain:
// Deployment → ReplicaSets → Pods, and the controller (or, for Jobs, the
// job-name labels) for the other kinds.
func (c *Cache) WorkloadPods(t model.Ref) []PodInfo {
	var keys []string
	switch t.Kind {
	case flux.KindDeployment:
		want := model.Ref{Group: flux.GroupApps, Kind: flux.KindDeployment, Namespace: t.Namespace, Name: t.Name}
		c.mu.Lock()
		for rs, owner := range c.rsOwners {
			if owner == want {
				ns, name, _ := strings.Cut(rs, "/")
				keys = append(keys, ownerKey(flux.KindReplicaSet, ns, name))
			}
		}
		c.mu.Unlock()
	case flux.KindStatefulSet, flux.KindDaemonSet, flux.KindReplicaSet, flux.KindJob:
		keys = []string{ownerKey(t.Kind, t.Namespace, t.Name)}
	default:
		return nil
	}
	seen := map[string]bool{}
	var out []PodInfo
	for _, inf := range c.pods {
		for _, key := range keys {
			objs, err := inf.GetIndexer().ByIndex(podOwnerIndex, key)
			if err != nil {
				c.logger.Warn("agent: look up pods of workload", "workload", t.ID(), "error", err)
				continue
			}
			for _, o := range objs {
				u, ok := o.(*unstructured.Unstructured)
				if !ok || u.GetNamespace() != t.Namespace || seen[u.GetName()] {
					continue
				}
				seen[u.GetName()] = true
				out = append(out, c.podInfo(u))
			}
		}
	}
	slices.SortFunc(out, func(a, b PodInfo) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.Name, b.Name))
	})
	return out
}

func (c *Cache) podInfo(u *unstructured.Unstructured) PodInfo {
	info := PodInfo{Name: u.GetName(), CreatedAt: u.GetCreationTimestamp().UTC(), Status: model.StatusUnknown}
	containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "containers")
	for _, ct := range containers {
		if m, ok := ct.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				info.Containers = append(info.Containers, n)
			}
		}
	}
	id := model.Ref{Group: flux.GroupCore, Kind: flux.KindPod, Namespace: u.GetNamespace(), Name: u.GetName()}.ID()
	c.mu.Lock()
	if r, ok := c.resources[id]; ok {
		info.Status = r.Status
	}
	c.mu.Unlock()
	return info
}

type podSub struct {
	namespace string
	ch        chan struct{}
}

// WatchPods implements PodSource.
func (c *Cache) WatchPods(namespace string) (<-chan struct{}, func()) {
	s := &podSub{namespace: namespace, ch: make(chan struct{}, 1)}
	c.subMu.Lock()
	c.podSubs[s] = struct{}{}
	c.subMu.Unlock()
	return s.ch, func() {
		c.subMu.Lock()
		delete(c.podSubs, s)
		c.subMu.Unlock()
	}
}

// notifyPods wakes the watchers of namespace without blocking.
func (c *Cache) notifyPods(namespace string) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for s := range c.podSubs {
		if s.namespace != namespace {
			continue
		}
		select {
		case s.ch <- struct{}{}:
		default:
		}
	}
}
