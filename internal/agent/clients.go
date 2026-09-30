package agent

import (
	"container/list"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/eddy-gitops/eddy/internal/protocol"
)

// Clients talk to the API server as one identity.
type Clients struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
}

// ClientFactory builds clients that impersonate an identity. The identity has
// already been validated by Policy.
type ClientFactory func(id protocol.Identity) (Clients, error)

// ImpersonatingFactory returns a ClientFactory that copies base and sets
// rest.ImpersonationConfig to exactly the identity's user and groups; nothing
// else (no UID, no extra) is ever impersonated. Clients are cached per
// identity in a small LRU so repeated requests reuse connections.
func ImpersonatingFactory(base *rest.Config, size int, ttl time.Duration) ClientFactory {
	c := newClientCache(size, ttl, time.Now)
	return func(id protocol.Identity) (Clients, error) {
		key := identityKey(id)
		if cl, ok := c.get(key); ok {
			return cl, nil
		}
		cfg := rest.CopyConfig(base)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: id.User, Groups: slices.Clone(id.Groups)}
		dyn, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return Clients{}, fmt.Errorf("agent: build impersonating dynamic client: %w", err)
		}
		kube, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return Clients{}, fmt.Errorf("agent: build impersonating client: %w", err)
		}
		cl := Clients{Dynamic: dyn, Kube: kube}
		c.put(key, cl)
		return cl, nil
	}
}

// identityKey is order-insensitive in groups.
func identityKey(id protocol.Identity) string {
	g := slices.Clone(id.Groups)
	slices.Sort(g)
	return id.User + "\x00" + strings.Join(g, "\x00")
}

// clientCache is an LRU with a TTL per entry.
type clientCache struct {
	mu    sync.Mutex
	size  int
	ttl   time.Duration
	now   func() time.Time
	order *list.List // front = most recent; values are *cacheEntry
	items map[string]*list.Element
}

type cacheEntry struct {
	key     string
	clients Clients
	expires time.Time
}

func newClientCache(size int, ttl time.Duration, now func() time.Time) *clientCache {
	return &clientCache{size: max(size, 1), ttl: ttl, now: now, order: list.New(), items: map[string]*list.Element{}}
}

func (c *clientCache) get(key string) (Clients, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return Clients{}, false
	}
	e := el.Value.(*cacheEntry)
	if c.now().After(e.expires) {
		c.order.Remove(el)
		delete(c.items, key)
		return Clients{}, false
	}
	c.order.MoveToFront(el)
	return e.clients, true
}

func (c *clientCache) put(key string, cl Clients) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.Remove(el)
	}
	c.items[key] = c.order.PushFront(&cacheEntry{key: key, clients: cl, expires: c.now().Add(c.ttl)})
	for c.order.Len() > c.size {
		old := c.order.Back()
		c.order.Remove(old)
		delete(c.items, old.Value.(*cacheEntry).key)
	}
}
