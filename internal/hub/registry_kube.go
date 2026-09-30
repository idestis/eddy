package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// ClusterGVR is the Cluster custom resource (config/crd).
var ClusterGVR = schema.GroupVersionResource{Group: "gitops.eddy.dev", Version: "v1alpha1", Resource: "clusters"}

// previousTokenKey is the Secret key holding the token being rotated out
// (ADR-0003 §8).
const previousTokenKey = "previousToken"

// kubeRestConfig returns the in-cluster config, falling back to the default
// kubeconfig loading rules (KUBECONFIG, ~/.kube/config) for development.
func kubeRestConfig() (*rest.Config, error) {
	if rc, err := rest.InClusterConfig(); err == nil {
		return rc, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("hub: no in-cluster config and no usable kubeconfig: %w", err)
	}
	return rc, nil
}

// kubeSource keeps a Registry in sync with the Cluster CRs and the token
// Secrets in the hub namespace, and writes Cluster status.
//
// The Secret informer never stores Secret values: a transform replaces each
// data value with its sha256 before the object reaches the cache, and drops
// annotations (kubectl's last-applied-configuration would repeat the data).
type kubeSource struct {
	reg       *Registry
	dyn       dynamic.Interface
	kube      kubernetes.Interface
	namespace string
	log       *slog.Logger

	clusters cache.SharedIndexInformer
	secrets  cache.SharedIndexInformer
	changed  chan struct{}
}

func newKubeSource(reg *Registry, rc *rest.Config, namespace string, log *slog.Logger) (*kubeSource, error) {
	if namespace == "" {
		return nil, errors.New("hub: namespace (or POD_NAMESPACE) is required to read agent token Secrets")
	}
	rc = rest.CopyConfig(rc)
	rc.UserAgent = "eddy-hub"
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("hub: dynamic client: %w", err)
	}
	kube, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("hub: kubernetes client: %w", err)
	}
	return newKubeSourceClients(reg, dyn, kube, namespace, log)
}

// newKubeSourceClients is newKubeSource with ready clients (tests pass fakes).
func newKubeSourceClients(reg *Registry, dyn dynamic.Interface, kube kubernetes.Interface, namespace string, log *slog.Logger) (*kubeSource, error) {
	if namespace == "" {
		return nil, errors.New("hub: namespace (or POD_NAMESPACE) is required to read agent token Secrets")
	}
	s := &kubeSource{reg: reg, dyn: dyn, kube: kube, namespace: namespace, log: log.With("component", "registry"), changed: make(chan struct{}, 1)}

	df := dynamicinformer.NewDynamicSharedInformerFactory(dyn, 0)
	s.clusters = df.ForResource(ClusterGVR).Informer()
	kf := informers.NewSharedInformerFactoryWithOptions(kube, 0, informers.WithNamespace(namespace))
	s.secrets = kf.Core().V1().Secrets().Informer()
	if err := s.secrets.SetTransform(hashSecret); err != nil {
		return nil, fmt.Errorf("hub: secret transform: %w", err)
	}
	h := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { s.kick() },
		UpdateFunc: func(any, any) { s.kick() },
		DeleteFunc: func(any) { s.kick() },
	}
	if _, err := s.clusters.AddEventHandler(h); err != nil {
		return nil, fmt.Errorf("hub: cluster handler: %w", err)
	}
	if _, err := s.secrets.AddEventHandler(h); err != nil {
		return nil, fmt.Errorf("hub: secret handler: %w", err)
	}
	return s, nil
}

// hashSecret keeps only the name and the sha256 of each data value.
func hashSecret(obj any) (any, error) {
	sec, ok := obj.(*corev1.Secret)
	if !ok {
		return obj, nil
	}
	out := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            sec.Name,
			Namespace:       sec.Namespace,
			UID:             sec.UID,
			ResourceVersion: sec.ResourceVersion,
		},
		Data: make(map[string][]byte, len(sec.Data)),
	}
	for k, v := range sec.Data {
		if h, ok := hashToken(v); ok {
			out.Data[k] = h[:]
		}
	}
	return out, nil
}

func (s *kubeSource) kick() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Run starts the informers and rebuilds the registry on every change.
func (s *kubeSource) Run(ctx context.Context) error {
	go s.clusters.RunWithContext(ctx)
	go s.secrets.RunWithContext(ctx)
	if !cache.WaitForCacheSync(ctx.Done(), s.clusters.HasSynced, s.secrets.HasSynced) {
		return fmt.Errorf("hub: cluster and secret informers did not sync: %w", ctx.Err())
	}
	s.rebuild()
	s.log.Info("cluster registry synced", "clusters", len(s.reg.List()))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.changed:
			s.rebuild()
		}
	}
}

func (s *kubeSource) rebuild() {
	m := map[string]clusterEntry{}
	for _, obj := range s.clusters.GetStore().List() {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		spec, ref, key := parseCluster(u)
		phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		e := clusterEntry{spec: spec, meta: clusterMeta{
			secretName: ref, secretKey: key, phase: phase, uid: string(u.GetUID()),
			helm: u.GetLabels()["app.kubernetes.io/managed-by"] == "Helm",
		}}
		if ref == "" {
			s.log.Warn("cluster has no agentTokenSecretRef; its agent cannot connect", "cluster", spec.Name)
		} else if item, exists, _ := s.secrets.GetStore().GetByKey(s.namespace + "/" + ref); exists {
			if sec, ok := item.(*corev1.Secret); ok {
				for _, k := range []string{key, previousTokenKey} {
					if v := sec.Data[k]; len(v) == 32 {
						e.tokens = append(e.tokens, [32]byte(v))
					}
				}
			}
			if len(e.tokens) == 0 {
				s.log.Warn("agent token secret has no usable key", "cluster", spec.Name, "secret", ref, "key", key)
			}
		} else {
			s.log.Warn("agent token secret not found", "cluster", spec.Name, "secret", ref, "namespace", s.namespace)
		}
		m[spec.Name] = e
	}
	s.reg.replace(m)
}

// parseCluster reads the spec of a Cluster CR and its token Secret reference.
func parseCluster(u *unstructured.Unstructured) (spec ClusterSpec, secret, key string) {
	name := u.GetName()
	str := func(f ...string) string {
		v, _, _ := unstructured.NestedString(u.Object, append([]string{"spec"}, f...)...)
		return v
	}
	protected, _, _ := unstructured.NestedBool(u.Object, "spec", "protected")
	order, _, _ := unstructured.NestedInt64(u.Object, "spec", "order")
	spec = ClusterSpec{
		Name:        name,
		DisplayName: cmp.Or(str("displayName"), name),
		Environment: str("environment"),
		Region:      str("region"),
		Color:       str("color"),
		Protected:   protected,
		Order:       int(order),
	}
	return spec, str("agentTokenSecretRef", "name"), cmp.Or(str("agentTokenSecretRef", "key"), "token")
}

// clusterStatus is the status subresource the hub writes.
type clusterStatus struct {
	Phase             string    `json:"phase"`
	LastSeen          time.Time `json:"lastSeen,omitzero"`
	AgentVersion      string    `json:"agentVersion,omitempty"`
	KubernetesVersion string    `json:"kubernetesVersion,omitempty"`
	FluxVersion       string    `json:"fluxVersion,omitempty"`
	Resources         int       `json:"resources"`
}

// patchStatus merge-patches clusters/<name>/status.
func (s *kubeSource) patchStatus(ctx context.Context, name string, st clusterStatus) error {
	if !st.LastSeen.IsZero() {
		st.LastSeen = st.LastSeen.UTC().Truncate(time.Second)
	}
	body, err := json.Marshal(map[string]any{"status": st})
	if err != nil {
		return fmt.Errorf("hub: encode status: %w", err)
	}
	_, err = s.dyn.Resource(ClusterGVR).Patch(ctx, name, types.MergePatchType, body,
		metav1.PatchOptions{FieldManager: "eddy-hub"}, "status")
	if err != nil {
		return fmt.Errorf("hub: patch status of cluster %s: %w", name, err)
	}
	return nil
}
