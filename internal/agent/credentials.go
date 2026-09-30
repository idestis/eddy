package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/idestis/eddy/internal/protocol"
)

// joinTokenPrefix marks a one-time join token (ADR-0005).
const joinTokenPrefix = "eddy_join_"

func isJoinToken(tok string) bool { return strings.HasPrefix(tok, joinTokenPrefix) }

// secretStore reads and writes the agent's own token Secret. It is the only
// object the agent ever writes, and its RBAC (a Role pinned by
// resourceNames) allows nothing else.
type secretStore interface {
	Load(ctx context.Context) (string, error)
	Save(ctx context.Context, token string) error
}

// kubeSecretStore is a secretStore on one Secret key.
type kubeSecretStore struct {
	kube            kubernetes.Interface
	namespace, name string
	key             string
}

func (k kubeSecretStore) Load(ctx context.Context) (string, error) {
	sec, err := k.kube.CoreV1().Secrets(k.namespace).Get(ctx, k.name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("agent: read token secret %s/%s: %w", k.namespace, k.name, err)
	}
	return strings.TrimSpace(string(sec.Data[k.key])), nil
}

// Save sets the key on the existing Secret, retrying on update conflicts.
func (k kubeSecretStore) Save(ctx context.Context, token string) error {
	var err error
	for range 3 {
		var sec *corev1.Secret
		sec, err = k.kube.CoreV1().Secrets(k.namespace).Get(ctx, k.name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("agent: read token secret %s/%s: %w", k.namespace, k.name, err)
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data[k.key] = []byte(token)
		_, err = k.kube.CoreV1().Secrets(k.namespace).Update(ctx, sec, metav1.UpdateOptions{FieldManager: "eddy-agent"})
		if err == nil || !apierrors.IsConflict(err) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("agent: store token in secret %s/%s: %w", k.namespace, k.name, err)
	}
	return nil
}

// Credentials is the agent's hub credential: a permanent agent token, or a
// join token until the hub has sent one. It is safe for concurrent use.
type Credentials struct {
	mu      sync.Mutex
	token   string // permanent token, if known
	join    string // join token, used while token is empty
	store   secretStore
	lastErr string
}

// NewCredentials starts from the configured tokens. store may be nil (no
// token Secret to read or write).
func NewCredentials(token, join string, store secretStore) *Credentials {
	c := &Credentials{join: join, store: store}
	if !isJoinToken(token) {
		c.token = token
	}
	return c
}

// Current returns the token to present to the hub.
func (c *Credentials) Current() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token
	}
	return c.join
}

// Joining reports whether the agent still has only its join token.
func (c *Credentials) Joining() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token == "" && c.join != ""
}

// LastError is the last failure to store a received token, for Hello
// diagnostics.
func (c *Credentials) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Refresh re-reads the token Secret and adopts a permanent token found
// there, for example one another agent replica stored after it joined with
// the same join token. It reports whether the token changed.
func (c *Credentials) Refresh(ctx context.Context) (bool, error) {
	if c.store == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := c.store.Load(ctx)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v == "" || isJoinToken(v) || v == c.token {
		return false, nil
	}
	c.token = v
	return true, nil
}

// Accept adopts the token from a credentials frame and stores it in the
// token Secret. When the write fails the token is still used from memory
// for the life of this process, and the error is reported to the hub.
func (c *Credentials) Accept(ctx context.Context, token string) protocol.CredentialsResult {
	token = strings.TrimSpace(token)
	if token == "" || isJoinToken(token) || len(token) > 512 {
		return protocol.CredentialsResult{Error: "invalid token"}
	}
	c.mu.Lock()
	c.token = token
	store := c.store
	c.mu.Unlock()
	if store == nil {
		err := "no token Secret configured (EDDY_TOKEN_SECRET); the token is kept in memory only"
		c.setErr(err)
		return protocol.CredentialsResult{Error: err}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := store.Save(ctx, token); err != nil {
		msg := err.Error()
		var se *apierrors.StatusError
		if errors.As(err, &se) {
			msg = fmt.Sprintf("store token secret: %s", se.ErrStatus.Reason)
		}
		c.setErr(msg)
		return protocol.CredentialsResult{Error: msg}
	}
	c.setErr("")
	return protocol.CredentialsResult{Stored: true}
}

func (c *Credentials) setErr(s string) {
	c.mu.Lock()
	c.lastErr = s
	c.mu.Unlock()
}
