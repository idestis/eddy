package hub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/auth"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// Onboarding (ADR-0005): clusters added from the UI, join tokens, the
// connection checklist and rejected connection attempts.
//
// Every replica can serve every step: Cluster CRs and token Secrets are the
// source of truth (each replica's informers see them), join tokens and
// rejected attempts live in the store, and a store EventConnection tells the
// other replicas' SSE clients to refetch a cluster's checklist.

const (
	phasePending = "Pending"

	// labelManagedBy marks objects the hub created, so it deletes only those.
	labelManagedBy  = "app.kubernetes.io/managed-by"
	managedByHub    = "eddy-hub"
	labelCluster    = "gitops.eddy.dev/cluster"
	clusterGroup    = "gitops.eddy.dev"
	clusterResource = "clusters"

	// mgmtAccessTTL caches management-cluster SubjectAccessReview answers.
	mgmtAccessTTL  = 30 * time.Second
	mgmtAccessMax  = 10_000
	credsTimeout   = 15 * time.Second
	registryCatch  = 5 * time.Second
	maxLabelLength = 63
)

var (
	dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	hexColor     = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

	// errOnboardingDisabled is returned (409) when clusters cannot be added
	// from the UI: static clusters, dev mode, or onboarding.enabled off.
	errOnboardingDisabled = fmt.Errorf("%w: cluster onboarding is disabled on this hub (it needs Cluster resources and onboarding.enabled)", store.ErrConflict)
)

// mgmtAuthorizer asks the management cluster, with SubjectAccessReviews
// created by the hub's ServiceAccount, whether a user may create, update,
// delete or get Cluster resources. Answers are cached briefly; errors are
// not cached and fail closed.
type mgmtAuthorizer struct {
	review func(ctx context.Context, sar *authv1.SubjectAccessReview) (*authv1.SubjectAccessReview, error)
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]accessEntry
}

func (m *mgmtAuthorizer) allowed(ctx context.Context, p identity.Principal, verb, name string) (bool, error) {
	key := subjectKey(p) + "\x00" + verb + "\x00" + name
	now := m.now()
	m.mu.Lock()
	if e, ok := m.cache[key]; ok && now.Before(e.expires) {
		m.mu.Unlock()
		return e.allowed, nil
	}
	m.mu.Unlock()
	res, err := m.review(ctx, &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{
		User:   p.User,
		Groups: slices.Clone(p.Groups),
		ResourceAttributes: &authv1.ResourceAttributes{
			Verb: verb, Group: clusterGroup, Resource: clusterResource, Name: name,
		},
	}})
	if err != nil {
		return false, fmt.Errorf("hub: management-cluster access review: %w", err)
	}
	ok := res.Status.Allowed && !res.Status.Denied
	m.mu.Lock()
	if len(m.cache) >= mgmtAccessMax {
		clear(m.cache)
	}
	m.cache[key] = accessEntry{allowed: ok, expires: now.Add(mgmtAccessTTL)}
	m.mu.Unlock()
	return ok, nil
}

// onboarding implements the cluster wizard on one replica.
type onboarding struct {
	cfg   *config.Hub
	st    store.Store
	reg   *Registry
	kube  *kubeSource // nil with staticClusters
	auth  *auth.Service
	rec   *audit.Recorder
	bus   *bus
	fleet *fleetService
	mgmt  *mgmtAuthorizer
	pod   string
	log   *slog.Logger
	now   func() time.Time
}

func newOnboarding(cfg *config.Hub, st store.Store, reg *Registry, kube *kubeSource, a *auth.Service, rec *audit.Recorder,
	b *bus, f *fleetService, pod string, log *slog.Logger) *onboarding {
	o := &onboarding{cfg: cfg, st: st, reg: reg, kube: kube, auth: a, rec: rec, bus: b, fleet: f, pod: pod,
		log: log.With("component", "onboarding"), now: time.Now}
	if kube != nil {
		o.mgmt = &mgmtAuthorizer{now: time.Now, cache: map[string]accessEntry{}, review: func(ctx context.Context, sar *authv1.SubjectAccessReview) (*authv1.SubjectAccessReview, error) {
			return kube.kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
		}}
	}
	return o
}

// enabled reports whether clusters can be added from the UI.
func (o *onboarding) enabled() bool { return o != nil && o.cfg.Onboarding.Enabled && o.kube != nil }

// can checks verb on clusters.gitops.eddy.dev (name "" for any) as p.
func (o *onboarding) can(ctx context.Context, p identity.Principal, verb, name string) (bool, error) {
	if !o.enabled() {
		return false, nil
	}
	if err := o.fleet.validPrincipal(p); err != nil {
		return false, nil
	}
	return o.mgmt.allowed(ctx, p, verb, name)
}

func (o *onboarding) require(ctx context.Context, p identity.Principal, verb, name string) error {
	if !o.enabled() {
		return errOnboardingDisabled
	}
	if err := o.fleet.validPrincipal(p); err != nil {
		return err
	}
	ok, err := o.mgmt.allowed(ctx, p, verb, name)
	if err != nil {
		return fmt.Errorf("%w: %w", errUnavailable, err)
	}
	if !ok {
		return fmt.Errorf("%w: you may not %s clusters.gitops.eddy.dev in the management cluster", fleet.ErrForbidden, verb)
	}
	return nil
}

// notify tells SSE clients on every replica that cluster's connection
// state changed.
func (o *onboarding) notify(cluster string) {
	o.bus.publish(event{kind: evConnection, cluster: cluster})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		defer cancel()
		if err := o.st.Events().Publish(ctx, store.Event{Kind: store.EventConnection, Cluster: cluster}); err != nil {
			o.log.Debug("publishing a connection change failed", "cluster", cluster, "err", err)
		}
	}()
}

// reject records a rejected agent connection, only for registered
// clusters, so unknown names cannot fill the table.
func (o *onboarding) reject(cluster, reason, detail, peer string) {
	if o == nil || cluster == "" {
		return
	}
	if _, ok := o.reg.Get(cluster); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	err := o.st.ConnectionAttempts().Record(ctx, store.ConnectionAttempt{
		Cluster: cluster, At: o.now(), Reason: reason, Detail: cleanText(detail, 300), Peer: peer, HubPod: o.pod,
	})
	if err != nil {
		o.log.Warn("recording a rejected agent connection failed", "cluster", cluster, "err", err)
		return
	}
	o.notify(cluster)
}

// ---- cluster CRUD ----

// clusterInput is the body of POST /api/v1/clusters and PATCH
// /api/v1/clusters/{c}. On PATCH every field is optional.
type clusterInput struct {
	Name        string  `json:"name"`
	DisplayName *string `json:"displayName"`
	Environment *string `json:"environment"`
	Region      *string `json:"region"`
	Color       *string `json:"color"`
	Protected   *bool   `json:"protected"`
	Order       *int    `json:"order"`
	// TTL of the join token, e.g. "1h" (POST only; default onboarding.joinTokenTTL).
	TTL string `json:"ttl"`
	// Confirm must equal the cluster name to turn protection off (PATCH).
	Confirm string `json:"confirm"`
}

func validText(field string, v *string) error {
	if v == nil {
		return nil
	}
	if len(*v) > maxLabelLength || cleanText(*v, maxLabelLength) != *v {
		return badRequest("%s must be at most %d printable characters", field, maxLabelLength)
	}
	return nil
}

func (in clusterInput) validate(create bool) error {
	if create && (len(in.Name) > 63 || !dns1123Label.MatchString(in.Name)) {
		return badRequest("name must be a DNS-1123 label: lower-case letters, digits and '-', at most 63 characters")
	}
	for _, f := range []struct {
		n string
		v *string
	}{{"displayName", in.DisplayName}, {"environment", in.Environment}, {"region", in.Region}} {
		if err := validText(f.n, f.v); err != nil {
			return err
		}
	}
	if in.Color != nil && *in.Color != "" && !hexColor.MatchString(*in.Color) {
		return badRequest("color must be #RRGGBB")
	}
	if in.Order != nil && (*in.Order < -100000 || *in.Order > 100000) {
		return badRequest("order must be between -100000 and 100000")
	}
	return nil
}

func (o *onboarding) joinTTL(s string) (time.Duration, error) {
	if s == "" {
		return o.cfg.Onboarding.JoinTokenTTL.Duration, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 5*time.Minute || d > config.MaxJoinTokenTTL {
		return 0, badRequest("ttl must be a duration between 5m and %s", config.MaxJoinTokenTTL)
	}
	return d, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// issuedToken is a join token as returned once to its issuer.
type issuedToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// issue creates a join token for cluster (revoking its unused predecessor).
func (o *onboarding) issue(ctx context.Context, p identity.Principal, cluster string, ttl time.Duration) (issuedToken, error) {
	tok, id, secret, err := auth.NewJoinToken()
	if err != nil {
		return issuedToken{}, err
	}
	now := o.now()
	jt := store.JoinToken{
		ID: id, Cluster: cluster, Hash: o.auth.JoinTokenHash(secret), CreatedBy: p.User,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := o.st.JoinTokens().Create(ctx, jt); err != nil {
		return issuedToken{}, fmt.Errorf("hub: store join token: %w", err)
	}
	return issuedToken{Token: tok, ExpiresAt: jt.ExpiresAt.UTC()}, nil
}

// createdCluster is the response of POST /api/v1/clusters.
type createdCluster struct {
	Cluster   onboardedCluster `json:"cluster"`
	JoinToken issuedToken      `json:"joinToken"`
	Guide     installGuide     `json:"guide"`
}

// onboardedCluster is the cluster as the onboarding API returns it.
type onboardedCluster struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Environment string `json:"environment,omitempty"`
	Region      string `json:"region,omitempty"`
	Color       string `json:"color,omitempty"`
	Protected   bool   `json:"protected"`
	Order       int    `json:"order"`
	Phase       string `json:"phase"`
	// ManagedBy is "helm" for clusters from the eddy-hub chart's clusters[],
	// which the UI cannot change or delete.
	ManagedBy string `json:"managedBy,omitempty"`
}

func viewOf(spec ClusterSpec, meta clusterMeta, connected bool) onboardedCluster {
	v := onboardedCluster{Name: spec.Name, DisplayName: spec.DisplayName, Environment: spec.Environment, Region: spec.Region,
		Color: spec.Color, Protected: spec.Protected, Order: spec.Order, Phase: "Disconnected"}
	switch {
	case connected:
		v.Phase = "Connected"
	case meta.phase == phasePending:
		v.Phase = phasePending
	}
	if meta.helm {
		v.ManagedBy = "helm"
	}
	return v
}

func tokenSecretName(cluster string) string { return "eddy-agent-" + cluster }

func (o *onboarding) create(ctx context.Context, p identity.Principal, in clusterInput) (createdCluster, error) {
	target := store.ResourceRef{Cluster: in.Name}
	fail := func(res store.AuditResult, err error) (createdCluster, error) {
		o.rec.Record(ctx, p, "cluster.create", target, res, map[string]any{"error": truncate(err.Error(), 512)})
		return createdCluster{}, err
	}
	if err := o.require(ctx, p, "create", ""); err != nil {
		return fail(auditResultOf(err), err)
	}
	if err := in.validate(true); err != nil {
		return fail(store.AuditError, err)
	}
	ttl, err := o.joinTTL(in.TTL)
	if err != nil {
		return fail(store.AuditError, err)
	}
	spec := ClusterSpec{
		Name: in.Name, DisplayName: deref(in.DisplayName), Environment: deref(in.Environment), Region: deref(in.Region),
		Color: deref(in.Color),
	}
	if spec.DisplayName == "" {
		spec.DisplayName = spec.Name
	}
	if in.Protected != nil {
		spec.Protected = *in.Protected
	}
	if in.Order != nil {
		spec.Order = *in.Order
	}
	uid, err := o.kube.createCluster(ctx, spec, tokenSecretName(spec.Name))
	if err != nil {
		return fail(store.AuditError, err)
	}
	// A random token nobody knows, so the Secret exists (and is garbage
	// collected with the Cluster) until the agent joins.
	placeholder, err := auth.NewAgentToken()
	if err == nil {
		err = o.kube.writeToken(ctx, spec.Name, uid, tokenSecretName(spec.Name), guideTokenKey, placeholder, true)
	}
	if err != nil {
		if derr := o.kube.deleteCluster(context.WithoutCancel(ctx), spec.Name); derr != nil {
			o.log.Warn("rolling back a half-created cluster failed", "cluster", spec.Name, "err", derr)
		}
		return fail(store.AuditError, err)
	}
	if err := o.kube.patchStatus(ctx, spec.Name, clusterStatus{Phase: phasePending}); err != nil {
		o.log.Warn("setting the new cluster's phase failed", "cluster", spec.Name, "err", err)
	}
	tok, err := o.issue(ctx, p, spec.Name, ttl)
	if err != nil {
		return fail(store.AuditError, err)
	}
	guide, err := renderGuide(o.cfg, spec, tok.Token, tok.ExpiresAt)
	if err != nil {
		return fail(store.AuditError, err)
	}
	o.rec.Record(ctx, p, "cluster.create", target, store.AuditOK, map[string]any{
		"environment": spec.Environment, "protected": spec.Protected,
	})
	o.rec.Record(ctx, p, "cluster.join_token", target, store.AuditOK, map[string]any{"expiresAt": tok.ExpiresAt})
	o.notify(spec.Name)
	return createdCluster{Cluster: viewOf(spec, clusterMeta{phase: phasePending}, false), JoinToken: tok, Guide: guide}, nil
}

func auditResultOf(err error) store.AuditResult {
	if errors.Is(err, fleet.ErrForbidden) || errors.Is(err, fleet.ErrConfirmRequired) {
		return store.AuditDenied
	}
	return store.AuditError
}

func (o *onboarding) registered(name string) (ClusterSpec, clusterMeta, error) {
	spec, ok := o.reg.Get(name)
	if !ok {
		return ClusterSpec{}, clusterMeta{}, fmt.Errorf("%w: cluster %q", fleet.ErrNotFound, truncate(name, 64))
	}
	meta, _ := o.reg.meta(name)
	return spec, meta, nil
}

func errHelmManaged(name string) error {
	return fmt.Errorf("%w: cluster %s is managed by Helm (the eddy-hub chart's clusters[]); change it there", store.ErrConflict, name)
}

func (o *onboarding) update(ctx context.Context, p identity.Principal, name string, in clusterInput) (onboardedCluster, error) {
	target := store.ResourceRef{Cluster: name}
	fail := func(err error) (onboardedCluster, error) {
		o.rec.Record(ctx, p, "cluster.update", target, auditResultOf(err), map[string]any{"error": truncate(err.Error(), 512)})
		return onboardedCluster{}, err
	}
	if err := o.require(ctx, p, "update", name); err != nil {
		return fail(err)
	}
	spec, meta, err := o.registered(name)
	if err != nil {
		return fail(err)
	}
	if meta.helm {
		return fail(errHelmManaged(name))
	}
	if err := in.validate(false); err != nil {
		return fail(err)
	}
	if spec.Protected && in.Protected != nil && !*in.Protected && in.Confirm != name {
		return fail(fmt.Errorf("%w: type the cluster name %q to turn protection off", fleet.ErrConfirmRequired, name))
	}
	patch := map[string]any{}
	set := func(k string, v any, ok bool) {
		if ok {
			patch[k] = v
		}
	}
	set("displayName", deref(in.DisplayName), in.DisplayName != nil)
	set("environment", deref(in.Environment), in.Environment != nil)
	set("region", deref(in.Region), in.Region != nil)
	if in.Color != nil {
		if *in.Color == "" {
			patch["color"] = nil
		} else {
			patch["color"] = *in.Color
		}
	}
	if in.Protected != nil {
		patch["protected"] = *in.Protected
		spec.Protected = *in.Protected
	}
	if in.Order != nil {
		patch["order"] = *in.Order
		spec.Order = *in.Order
	}
	if len(patch) == 0 {
		return viewOf(spec, meta, o.fleet.agents.session(name) != nil), nil
	}
	if err := o.kube.patchSpec(ctx, name, patch); err != nil {
		return fail(err)
	}
	if in.DisplayName != nil {
		spec.DisplayName = cmpOr(*in.DisplayName, name)
	}
	if in.Environment != nil {
		spec.Environment = *in.Environment
	}
	if in.Region != nil {
		spec.Region = *in.Region
	}
	if in.Color != nil {
		spec.Color = *in.Color
	}
	o.rec.Record(ctx, p, "cluster.update", target, store.AuditOK, patch)
	o.notify(name)
	return viewOf(spec, meta, o.fleet.agents.session(name) != nil), nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// reissue issues a new join token for an existing cluster, to install or
// reinstall its agent or rotate its token.
func (o *onboarding) reissue(ctx context.Context, p identity.Principal, name, ttlStr string) (issuedToken, installGuide, error) {
	target := store.ResourceRef{Cluster: name}
	fail := func(err error) (issuedToken, installGuide, error) {
		o.rec.Record(ctx, p, "cluster.join_token", target, auditResultOf(err), map[string]any{"error": truncate(err.Error(), 512)})
		return issuedToken{}, installGuide{}, err
	}
	if err := o.require(ctx, p, "update", name); err != nil {
		return fail(err)
	}
	spec, _, err := o.registered(name)
	if err != nil {
		return fail(err)
	}
	ttl, err := o.joinTTL(ttlStr)
	if err != nil {
		return fail(err)
	}
	tok, err := o.issue(ctx, p, name, ttl)
	if err != nil {
		return fail(err)
	}
	guide, err := renderGuide(o.cfg, spec, tok.Token, tok.ExpiresAt)
	if err != nil {
		return fail(err)
	}
	o.rec.Record(ctx, p, "cluster.join_token", target, store.AuditOK, map[string]any{"expiresAt": tok.ExpiresAt})
	o.notify(name)
	return tok, guide, nil
}

func (o *onboarding) remove(ctx context.Context, p identity.Principal, name, confirm string) error {
	target := store.ResourceRef{Cluster: name}
	fail := func(err error) error {
		o.rec.Record(ctx, p, "cluster.delete", target, auditResultOf(err), map[string]any{"error": truncate(err.Error(), 512)})
		return err
	}
	if err := o.require(ctx, p, "delete", name); err != nil {
		return fail(err)
	}
	spec, meta, err := o.registered(name)
	if err != nil {
		return fail(err)
	}
	if meta.helm {
		return fail(errHelmManaged(name))
	}
	if spec.Protected && confirm != name {
		return fail(fmt.Errorf("%w: type the cluster name %q to confirm", fleet.ErrConfirmRequired, name))
	}
	if err := o.kube.deleteCluster(ctx, name); err != nil {
		return fail(err)
	}
	if meta.secretName != "" {
		if err := o.kube.deleteOwnedSecret(ctx, meta.secretName); err != nil {
			o.log.Warn("deleting the cluster's token secret failed; it is garbage-collected with the Cluster", "cluster", name, "err", err)
		}
	}
	if err := o.st.JoinTokens().RevokeByCluster(ctx, name, o.now()); err != nil {
		o.log.Warn("revoking the cluster's join tokens failed", "cluster", name, "err", err)
	}
	if err := o.st.ConnectionAttempts().DeleteByCluster(ctx, name); err != nil {
		o.log.Debug("forgetting the cluster's connection attempts failed", "cluster", name, "err", err)
	}
	o.rec.Record(ctx, p, "cluster.delete", target, store.AuditOK, nil)
	o.notify(name)
	return nil
}

// ---- join ----

// joinCheck validates a presented join token for cluster without using it.
// It returns the token row and, on rejection, the attempt reason.
func (o *onboarding) joinCheck(ctx context.Context, cluster, tok string) (store.JoinToken, string, string) {
	id, secret, ok := auth.ParseJoinToken(tok)
	if !ok {
		return store.JoinToken{}, store.AttemptBadToken, "malformed join token"
	}
	jt, err := o.st.JoinTokens().Get(ctx, id)
	if err != nil || !o.auth.VerifyJoinTokenHash(secret, jt.Hash) {
		return store.JoinToken{}, store.AttemptBadToken, "unknown join token"
	}
	if jt.Cluster != cluster {
		return jt, store.AttemptWrongCluster, fmt.Sprintf("the join token was issued for cluster %q", jt.Cluster)
	}
	reason, detail := joinStateReason(jt, o.now())
	return jt, reason, detail
}

// joinStateReason explains why a join token cannot be used ("" when it can).
func joinStateReason(jt store.JoinToken, now time.Time) (string, string) {
	switch {
	case jt.UsedAt != nil:
		return store.AttemptJoinUsed, "the join token was already used at " + jt.UsedAt.UTC().Format(time.RFC3339)
	case jt.RevokedAt != nil:
		return store.AttemptJoinExpired, "the join token was replaced by a newer one"
	case !now.Before(jt.ExpiresAt):
		return store.AttemptJoinExpired, "the join token expired at " + jt.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return "", ""
}

// mint consumes the join token and writes a new permanent agent token into
// the cluster's token Secret. It returns the token to send to the agent.
func (o *onboarding) mint(ctx context.Context, cluster string, jt store.JoinToken) (string, error) {
	used, err := o.st.JoinTokens().Consume(ctx, jt.ID, o.now())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			if cur, gerr := o.st.JoinTokens().Get(ctx, jt.ID); gerr == nil {
				reason, detail := joinStateReason(cur, o.now())
				return "", &joinError{reason: cmpOr(reason, store.AttemptJoinUsed), detail: detail}
			}
		}
		return "", fmt.Errorf("hub: consume join token: %w", err)
	}
	meta, ok := o.reg.meta(cluster)
	if !ok {
		return "", &joinError{reason: store.AttemptCredentialsFail, detail: "the cluster was removed"}
	}
	name, key := cmpOr(meta.secretName, tokenSecretName(cluster)), cmpOr(meta.secretKey, guideTokenKey)
	token, err := auth.NewAgentToken()
	if err != nil {
		return "", err
	}
	if err := o.kube.writeToken(ctx, cluster, meta.uid, name, key, token, false); err != nil {
		return "", &joinError{reason: store.AttemptCredentialsFail, detail: "the hub could not write the agent token Secret: " + err.Error()}
	}
	// Wait (briefly) until this replica's informer has the new token, so
	// the agent's reconnect is accepted here; other replicas catch up on
	// the same watch, and the agent retries with backoff meanwhile.
	h := sha256.Sum256([]byte(token))
	deadline := time.Now().Add(registryCatch)
	for !o.reg.Accepts(cluster, h) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	o.rec.Record(ctx, identity.Principal{User: used.CreatedBy, Via: identity.ViaSystem}, "cluster.joined",
		store.ResourceRef{Cluster: cluster}, store.AuditOK, map[string]any{"joinTokenId": used.ID})
	return token, nil
}

type joinError struct{ reason, detail string }

func (e *joinError) Error() string { return e.detail }

// ---- connection checklist ----

// checkState values.
const (
	checkOK      = "ok"
	checkWarn    = "warn"
	checkFail    = "fail"
	checkPending = "pending"
	checkInfo    = "info"
)

type connectionCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Docs   string `json:"docs,omitempty"`
}

type joinTokenInfo struct {
	ID        string     `json:"id"`
	State     string     `json:"state"` // active | used | expired | revoked
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
}

// connectionPermissions says what the viewer may do with the cluster:
// issue join tokens (update), edit its fields (edit: update, and not
// managed by Helm) and delete it (not managed by Helm).
type connectionPermissions struct {
	Update bool `json:"update"`
	Edit   bool `json:"edit"`
	Delete bool `json:"delete"`
}

type connectionResponse struct {
	Cluster     onboardedCluster          `json:"cluster"`
	Checks      []connectionCheck         `json:"checks"`
	Agents      int                       `json:"agents"`
	JoinToken   *joinTokenInfo            `json:"joinToken,omitempty"`
	Attempts    []store.ConnectionAttempt `json:"attempts"`
	Permissions connectionPermissions     `json:"permissions"`
	Guide       installGuide              `json:"guide"`
}

const impersonationDocs = "https://github.com/idestis/eddy/blob/main/docs/agent.md#impersonation-the-one-powerful-permission"

func (o *onboarding) connection(ctx context.Context, p identity.Principal, name string) (connectionResponse, error) {
	if err := o.require(ctx, p, "get", name); err != nil {
		return connectionResponse{}, err
	}
	spec, meta, err := o.registered(name)
	if err != nil {
		return connectionResponse{}, err
	}
	s := o.fleet.agents.session(name)
	connected := s != nil && !s.closed()
	out := connectionResponse{Cluster: viewOf(spec, meta, connected), Attempts: []store.ConnectionAttempt{}}
	if list, err := o.st.ConnectionAttempts().List(ctx, name); err == nil {
		out.Attempts = list
	}
	if rows, err := o.st.AgentSessions().List(ctx, name, o.now().Add(-agentFreshFor)); err == nil {
		out.Agents = len(rows)
	}
	if connected && out.Agents == 0 {
		out.Agents = 1
	}
	if toks, err := o.st.JoinTokens().List(ctx, name); err == nil && len(toks) > 0 {
		t := toks[0]
		info := &joinTokenInfo{ID: t.ID, State: "active", CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, UsedAt: t.UsedAt}
		switch {
		case t.UsedAt != nil:
			info.State = "used"
		case t.RevokedAt != nil:
			info.State = "revoked"
		case !o.now().Before(t.ExpiresAt):
			info.State = "expired"
		}
		out.JoinToken = info
	}
	upd, _ := o.can(ctx, p, "update", name)
	del, _ := o.can(ctx, p, "delete", name)
	out.Permissions = connectionPermissions{Update: upd, Edit: upd && !meta.helm, Delete: del && !meta.helm}
	if out.Guide, err = renderGuide(o.cfg, spec, "", time.Time{}); err != nil {
		return connectionResponse{}, err
	}
	var lastReason, lastDetail string
	if len(out.Attempts) > 0 {
		lastReason, lastDetail = out.Attempts[0].Reason, out.Attempts[0].Detail
	}
	out.Checks = checksFor(s, connected, out.Agents, lastReason, lastDetail)
	return out, nil
}

// checksFor builds the ADR-0005 checklist from the primary session.
func checksFor(s clusterSession, connected bool, agents int, lastReason, lastDetail string) []connectionCheck {
	c := []connectionCheck{
		{ID: "connected", Label: "Agent connected", State: checkPending, Detail: "Waiting for the agent to connect"},
		{ID: "protocol", Label: "Protocol version compatible", State: checkPending},
		{ID: "flux", Label: "Flux detected", State: checkPending},
		{ID: "informers", Label: "Informers synced", State: checkPending},
		{ID: "sar", Label: "SubjectAccessReview works", State: checkPending},
		{ID: "impersonation", Label: "Impersonation pinned", State: checkPending, Docs: impersonationDocs},
		{ID: "namespaces", Label: "Watched namespaces", State: checkPending},
		{ID: "credentials", Label: "Permanent token stored", State: checkPending},
	}
	if lastReason != "" && !connected {
		c[0].State, c[0].Detail = checkFail, "Last attempt rejected: "+lastDetail
		if lastReason == store.AttemptProtocol {
			c[1].State, c[1].Detail = checkFail, lastDetail
		}
	}
	if !connected {
		return c
	}
	h := s.hello()
	c[0].State = checkOK
	c[0].Detail = fmt.Sprintf("%d agent replica(s), version %s", agents, cmpOr(h.AgentVersion, "unknown"))
	c[1].State, c[1].Detail = checkOK, "protocol "+cmpOr(h.Protocol, "1")
	d := h.Diagnostics
	fluxServed := d != nil && slices.Contains(d.ServedKinds, "Kustomization")
	switch {
	case h.FluxVersion != "":
		c[2].State, c[2].Detail = checkOK, "Flux "+h.FluxVersion
	case fluxServed:
		c[2].State, c[2].Detail = checkOK, "Flux kinds served (version unknown)"
	default:
		c[2].State, c[2].Detail = checkWarn, "No Flux kinds are served; Eddy shows workloads only"
	}
	if d != nil && len(d.ServedKinds) > 0 {
		c[2].Detail += fmt.Sprintf(" · %d kinds served", len(d.ServedKinds))
	}
	if n := s.size(); d == nil || d.InformersSynced >= d.InformersTotal {
		c[3].State, c[3].Detail = checkOK, fmt.Sprintf("%d resources", n)
	} else {
		c[3].State, c[3].Detail = checkPending, fmt.Sprintf("%d of %d informers synced", d.InformersSynced, d.InformersTotal)
	}
	switch {
	case d == nil:
		c[4].State, c[4].Detail = checkInfo, "The agent does not report diagnostics (older version)"
		c[5].State, c[5].Detail = checkInfo, "The agent does not report diagnostics (older version)"
		c[7].State, c[7].Detail = checkInfo, "The agent does not report diagnostics (older version)"
	default:
		if d.SAROK {
			c[4].State, c[4].Detail = checkOK, "The agent can create SubjectAccessReviews"
		} else {
			c[4].State, c[4].Detail = checkFail, cmpOr(d.SARError, "The agent cannot create SubjectAccessReviews")
		}
		switch {
		case d.ImpersonationPinned == nil:
			c[5].State, c[5].Detail = checkInfo, "The agent could not check"
		case *d.ImpersonationPinned:
			c[5].State, c[5].Detail = checkOK, "The agent cannot impersonate system:masters"
		default:
			c[5].State, c[5].Detail = checkWarn, "The agent may impersonate any group, including system:masters: set impersonation.groups"
		}
		if d.CredentialsError != "" {
			c[7].State = checkWarn
			c[7].Detail = "The agent could not store its token and uses it from memory until it restarts: " + d.CredentialsError
		} else {
			c[7].State, c[7].Detail = checkOK, "The agent uses a stored token"
		}
	}
	if len(h.Namespaces) == 0 {
		c[6].State, c[6].Detail = checkInfo, "Whole cluster"
	} else {
		c[6].State, c[6].Detail = checkInfo, strings.Join(h.Namespaces, ", ")
	}
	return c
}

// ---- kube writes (management cluster) ----

func (s *kubeSource) createCluster(ctx context.Context, spec ClusterSpec, secret string) (string, error) {
	sp := map[string]any{
		"displayName": spec.DisplayName,
		"protected":   spec.Protected,
		"order":       int64(spec.Order),
		"agentTokenSecretRef": map[string]any{
			"name": secret, "key": guideTokenKey,
		},
	}
	for k, v := range map[string]string{"environment": spec.Environment, "region": spec.Region, "color": spec.Color} {
		if v != "" {
			sp[k] = v
		}
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ClusterGVR.GroupVersion().String(),
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":   spec.Name,
			"labels": map[string]any{labelManagedBy: managedByHub},
		},
		"spec": sp,
	}}
	out, err := s.dyn.Resource(ClusterGVR).Create(ctx, u, metav1.CreateOptions{FieldManager: "eddy-hub"})
	if apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("%w: cluster %s already exists", store.ErrConflict, spec.Name)
	}
	if err != nil {
		return "", fmt.Errorf("hub: create cluster %s: %w", spec.Name, err)
	}
	return string(out.GetUID()), nil
}

func (s *kubeSource) patchSpec(ctx context.Context, name string, fields map[string]any) error {
	body, err := json.Marshal(map[string]any{"spec": fields})
	if err != nil {
		return fmt.Errorf("hub: encode cluster patch: %w", err)
	}
	_, err = s.dyn.Resource(ClusterGVR).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{FieldManager: "eddy-hub"})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: cluster %s", fleet.ErrNotFound, name)
	}
	if apierrors.IsInvalid(err) {
		return badRequest("the Cluster resource rejected the change: %s", truncate(err.Error(), 200))
	}
	if err != nil {
		return fmt.Errorf("hub: update cluster %s: %w", name, err)
	}
	return nil
}

func (s *kubeSource) deleteCluster(ctx context.Context, name string) error {
	err := s.dyn.Resource(ClusterGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: cluster %s", fleet.ErrNotFound, name)
	}
	if err != nil {
		return fmt.Errorf("hub: delete cluster %s: %w", name, err)
	}
	return nil
}

// writeToken sets key of the token Secret name (creating it, owned by the
// Cluster, when missing) and drops any previousToken, so only the new token
// is accepted. With create set, an existing Secret the hub did not create
// is a conflict.
func (s *kubeSource) writeToken(ctx context.Context, cluster, uid, name, key, token string, create bool) error {
	secrets := s.kube.CoreV1().Secrets(s.namespace)
	for range 3 {
		sec, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			sec = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: name, Namespace: s.namespace,
					Labels: map[string]string{labelManagedBy: managedByHub, labelCluster: cluster},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{key: []byte(token)},
			}
			if uid != "" {
				sec.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: ClusterGVR.GroupVersion().String(), Kind: "Cluster", Name: cluster, UID: types.UID(uid),
				}}
			}
			_, err = secrets.Create(ctx, sec, metav1.CreateOptions{FieldManager: "eddy-hub"})
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("hub: create token secret %s: %w", name, err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("hub: read token secret %s: %w", name, err)
		}
		if create && sec.Labels[labelManagedBy] != managedByHub {
			return fmt.Errorf("%w: Secret %s already exists in namespace %s", store.ErrConflict, name, s.namespace)
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data[key] = []byte(token)
		delete(sec.Data, previousTokenKey)
		_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{FieldManager: "eddy-hub"})
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("hub: update token secret %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("hub: token secret %s kept changing", name)
}

// deleteOwnedSecret deletes a token Secret only if the hub created it.
func (s *kubeSource) deleteOwnedSecret(ctx context.Context, name string) error {
	sec, err := s.kube.CoreV1().Secrets(s.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if sec.Labels[labelManagedBy] != managedByHub {
		return nil
	}
	err = s.kube.CoreV1().Secrets(s.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
