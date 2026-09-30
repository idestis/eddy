package hub

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/eddy-gitops/eddy/internal/config"
)

// ClusterSpec is what the hub knows about a registered cluster from its
// Cluster CR (or staticClusters entry). It never holds credentials.
type ClusterSpec struct {
	Name        string
	DisplayName string
	Environment string
	Region      string
	Color       string
	Protected   bool
	Order       int
}

// clusterEntry is a registered cluster with the sha256 hashes of the agent
// tokens it currently accepts: the current token and, during rotation, the
// previous one. Raw tokens are never kept.
type clusterEntry struct {
	spec   ClusterSpec
	tokens [][32]byte
}

// Registry is the set of clusters whose agents may connect. It is filled by
// a source (static config or the Kubernetes watcher) and read by the agent
// endpoint and the fleet service. It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	clusters map[string]clusterEntry
	synced   atomic.Bool

	subMu sync.Mutex
	subs  []func()
}

// NewRegistry returns an empty, unsynced registry.
func NewRegistry() *Registry {
	return &Registry{clusters: map[string]clusterEntry{}}
}

// Get returns the spec of a registered cluster.
func (r *Registry) Get(name string) (ClusterSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.clusters[name]
	return e.spec, ok
}

// List returns every registered cluster ordered by Order, then Name.
func (r *Registry) List() []ClusterSpec {
	r.mu.RLock()
	out := make([]ClusterSpec, 0, len(r.clusters))
	for _, e := range r.clusters {
		out = append(out, e.spec)
	}
	r.mu.RUnlock()
	slices.SortFunc(out, func(a, b ClusterSpec) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// Synced reports whether the source has delivered its initial state.
func (r *Registry) Synced() bool { return r.synced.Load() }

// dummyToken is compared against when the cluster is unknown, so the time
// Verify takes does not reveal which cluster names exist.
var dummyToken = sha256.Sum256([]byte("eddy-hub-unknown-cluster"))

// Verify checks a presented agent token for cluster in constant time. It
// returns the token's sha256, which the session keeps to re-validate itself
// when the cluster's Secret changes.
func (r *Registry) Verify(cluster, token string) ([32]byte, bool) {
	h := sha256.Sum256([]byte(token))
	return h, token != "" && r.Accepts(cluster, h)
}

// Accepts reports whether the token with sha256 h is currently valid for
// cluster. Every stored hash is compared, without an early exit.
func (r *Registry) Accepts(cluster string, h [32]byte) bool {
	r.mu.RLock()
	e, ok := r.clusters[cluster]
	tokens := e.tokens
	r.mu.RUnlock()
	match := 0
	if !ok || len(tokens) == 0 {
		subtle.ConstantTimeCompare(h[:], dummyToken[:])
		return false
	}
	for _, t := range tokens {
		match |= subtle.ConstantTimeCompare(h[:], t[:])
	}
	return match == 1
}

// OnChange registers fn to run (synchronously, outside the registry lock)
// after every change to the cluster set or their tokens.
func (r *Registry) OnChange(fn func()) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	r.subs = append(r.subs, fn)
}

// replace swaps the whole cluster set and marks the registry synced.
func (r *Registry) replace(clusters map[string]clusterEntry) {
	r.mu.Lock()
	r.clusters = clusters
	r.mu.Unlock()
	r.synced.Store(true)
	r.subMu.Lock()
	subs := slices.Clone(r.subs)
	r.subMu.Unlock()
	for _, fn := range subs {
		fn()
	}
}

// hashToken returns the sha256 of a token value read from a Secret or the
// environment. Surrounding whitespace (a trailing newline from `echo`) is
// not part of the token.
func hashToken(v []byte) ([32]byte, bool) {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return [32]byte{}, false
	}
	return sha256.Sum256(v), true
}

// loadStatic fills the registry from hub.yaml's staticClusters, reading each
// token from its environment variable.
func loadStatic(r *Registry, static []config.StaticCluster, log *slog.Logger) {
	m := make(map[string]clusterEntry, len(static))
	for _, c := range static {
		if c.Name == "" {
			log.Warn("static cluster without a name ignored")
			continue
		}
		e := clusterEntry{spec: ClusterSpec{
			Name:        c.Name,
			DisplayName: cmp.Or(c.DisplayName, c.Name),
			Environment: c.Environment,
			Color:       c.Color,
			Protected:   c.Protected,
			Order:       c.Order,
		}}
		if h, ok := hashToken([]byte(os.Getenv(c.TokenEnv))); ok && c.TokenEnv != "" {
			e.tokens = [][32]byte{h}
		} else {
			log.Warn("static cluster has no agent token; its agent cannot connect", "cluster", c.Name, "tokenEnv", c.TokenEnv)
		}
		m[c.Name] = e
	}
	r.replace(m)
}
