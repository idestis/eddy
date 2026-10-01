package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/redact"
	"github.com/idestis/eddy/internal/store"
)

// Log limits for pod logs requested through the hub.
const (
	DefaultTailLines = 500
	MaxTailLines     = 5000
)

// fleetService implements fleet.Service over the registry, the agent
// sessions and the authorizer. It is the only path from a user request to a
// cluster, so every rule lives here:
//
//   - the principal is validated (no system:* user or group, no denied user
//     prefix) before anything is sent to an agent, which checks again;
//   - every read is filtered by the caller's SubjectAccessReview answers;
//   - every agent request carries the caller as the identity to impersonate;
//   - writes are not pre-checked (the API server decides) but need a typed
//     confirmation on protected clusters, and are always audited.
type fleetService struct {
	reg          *Registry
	agents       *agents
	authz        *authorizer
	rec          *audit.Recorder
	denyPrefixes []string
	log          *slog.Logger

	// Created on first use (lazyInit): the visible tuple sets of index
	// lists and the hub-wide search scan slots.
	lazy  sync.Once
	vis   *visibleCache
	scans *scanSlots
}

var _ fleet.Service = (*fleetService)(nil)

// validPrincipal refuses identities an agent must never impersonate.
func (f *fleetService) validPrincipal(p identity.Principal) error {
	u := strings.ToLower(p.User)
	if p.User == "" {
		return fmt.Errorf("%w: no user", fleet.ErrForbidden)
	}
	if strings.HasPrefix(u, "system:") {
		return fmt.Errorf("%w: system users cannot be impersonated", fleet.ErrForbidden)
	}
	for _, d := range f.denyPrefixes {
		if d != "" && strings.HasPrefix(u, strings.ToLower(d)) {
			return fmt.Errorf("%w: user is denied", fleet.ErrForbidden)
		}
	}
	for _, g := range p.Groups {
		if strings.HasPrefix(strings.ToLower(g), "system:") {
			return fmt.Errorf("%w: system groups cannot be impersonated", fleet.ErrForbidden)
		}
	}
	return nil
}

func protoIdentity(p identity.Principal) protocol.Identity {
	return protocol.Identity{User: p.User, Groups: slices.Clone(p.Groups)}
}

// session returns what serves reads of a registered cluster: its primary
// session (a local agent session, or a relay to the replica that holds
// one), or the stale view of a disconnected cluster, whose requests fail
// with ErrDisconnected.
func (f *fleetService) session(cluster string) (ClusterSpec, clusterSession, error) {
	spec, ok := f.reg.Get(cluster)
	if !ok {
		return ClusterSpec{}, nil, fmt.Errorf("%w: cluster %q", fleet.ErrNotFound, cluster)
	}
	s := f.agents.reader(cluster)
	if s == nil {
		return spec, nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, cluster)
	}
	return spec, s, nil
}

// canonicalRef resolves ref.Kind through the kind table and fills in the
// API group, so callers may pass "kustomization" and no group.
func canonicalRef(ref model.Ref) (model.Ref, flux.Kind, error) {
	ref, _ = requestedGroup(ref)
	k, ok := flux.KindByName(ref.Kind)
	if !ok {
		return model.Ref{}, flux.Kind{}, fmt.Errorf("%w: unknown kind %q", fleet.ErrNotFound, truncate(ref.Kind, 64))
	}
	if ref.Group != "" && ref.Group != k.Group {
		return model.Ref{}, flux.Kind{}, fmt.Errorf("%w: kind %s is in group %q, not %q", fleet.ErrNotFound, k.Kind, k.Group, truncate(ref.Group, 64))
	}
	if ref.Name == "" || (k.Namespaced && ref.Namespace == "") {
		return model.Ref{}, flux.Kind{}, badRequest("%s needs a namespace and a name", k.Kind)
	}
	ref.Kind, ref.Group = k.Kind, k.Group
	return ref, k, nil
}

// coreGroupAlias names the core API group ("") where an empty group means
// "any group": the ?group= query parameter and refs passed to the fleet
// service.
const coreGroupAlias = "core"

// requestedGroup resolves the core alias and reports whether ref names a
// group at all.
func requestedGroup(ref model.Ref) (model.Ref, bool) {
	switch ref.Group {
	case "":
		return ref, false
	case coreGroupAlias:
		ref.Group = ""
	}
	return ref, true
}

// locate resolves ref for Get, yaml and events and checks that p may see
// the object, answering ErrNotFound (never ErrForbidden) otherwise:
//   - a kind from the table, in its own group or with no group given: the
//     row of the view if there is one (nil otherwise). An inventory-only row
//     follows the inventory visibility rule; anything else needs list on
//     the kind in the namespace or get on the object.
//   - any other (group, kind): only an inventory-only row of the view. With
//     no group, the Kind (ignoring case), namespace and name must match
//     rows of one API group among those p may see, else 400.
//
// The returned ref is canonical and carries the API group.
func (f *fleetService) locate(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (model.Ref, clusterSession, *model.Resource, error) {
	ref, groupSet := requestedGroup(ref)
	if k, ok := flux.KindByName(ref.Kind); ok && (!groupSet || ref.Group == k.Group) {
		ref, _, err := canonicalRef(model.Ref{Kind: k.Kind, Namespace: ref.Namespace, Name: ref.Name})
		if err != nil {
			return model.Ref{}, nil, nil, err
		}
		_, s, err := f.session(cluster)
		if err != nil {
			return model.Ref{}, nil, nil, err
		}
		r, found := s.lookup(ref.ID())
		if found && r.InventoryOnly {
			if _, err := f.visibleInventoryRow(ctx, p, cluster, r); err != nil {
				return model.Ref{}, nil, nil, err
			}
			return ref, s, &r, nil
		}
		if err := f.requireVisible(ctx, p, cluster, ref); err != nil {
			return model.Ref{}, nil, nil, err
		}
		if !found {
			return ref, s, nil, nil
		}
		return ref, s, &r, nil
	}
	notFound := fmt.Errorf("%w: unknown kind %q", fleet.ErrNotFound, truncate(ref.Kind, 64))
	if ref.Name == "" || ref.Kind == "" || !flux.ValidKindName(strings.ToUpper(ref.Kind[:1])+ref.Kind[1:]) {
		return model.Ref{}, nil, nil, notFound
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return model.Ref{}, nil, nil, err
	}
	all, _ := s.view()
	var cand []model.Resource
	for _, r := range all {
		if r.InventoryOnly && strings.EqualFold(r.Kind, ref.Kind) && r.Namespace == ref.Namespace && r.Name == ref.Name &&
			(!groupSet || r.Group == ref.Group) {
			cand = append(cand, r)
		}
	}
	if len(cand) == 0 {
		return model.Ref{}, nil, nil, notFound
	}
	vis, err := f.authz.filter(ctx, p, cluster, cand)
	if err != nil {
		return model.Ref{}, nil, nil, err
	}
	switch {
	case len(vis) == 0:
		return model.Ref{}, nil, nil, fmt.Errorf("%w: %s", fleet.ErrNotFound, cand[0].ID)
	case len(vis) > 1:
		sortResources(vis)
		for _, r := range vis[1:] {
			if r.Group != vis[0].Group {
				return model.Ref{}, nil, nil, badRequest("%s is served by several API groups (%q, %q): pass ?group=", vis[0].Kind, vis[0].Group, r.Group)
			}
		}
	}
	r := vis[0]
	return r.Ref, s, &r, nil
}

// sendAccess is the authorizer's transport: an OpAccess request.
func (f *fleetService) sendAccess(ctx context.Context, cluster string, id protocol.Identity, checks []protocol.AccessCheck) ([]bool, error) {
	s := f.agents.session(cluster)
	if s == nil {
		return nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, cluster)
	}
	args, err := json.Marshal(protocol.AccessArgs{Checks: checks})
	if err != nil {
		return nil, fmt.Errorf("hub: encode access checks: %w", err)
	}
	raw, err := s.do(ctx, protocol.Request{Op: protocol.OpAccess, Identity: id, Args: args})
	if err != nil {
		return nil, err
	}
	var res protocol.AccessResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("hub: decode access result from %s: %w", cluster, err)
	}
	return res.Allowed, nil
}

// canSee reports whether p may read ref: list on its kind in its namespace
// (what the resource list shows), or get on the object itself.
func (f *fleetService) canSee(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (bool, error) {
	t, ok := tupleOf(ref)
	if !ok {
		return false, nil
	}
	get := t.check("get")
	get.Name = ref.Name
	res, err := f.authz.check(ctx, p, cluster, []protocol.AccessCheck{t.check("list"), get})
	if err != nil {
		return false, err
	}
	return res[0] || res[1], nil
}

// requireVisible returns ErrNotFound (never ErrForbidden) for objects p may
// not read, so names do not leak.
func (f *fleetService) requireVisible(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) error {
	ok, err := f.canSee(ctx, p, cluster, ref)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", fleet.ErrNotFound, ref.ID())
	}
	return nil
}

// Clusters lists every registered cluster. Counts cover only what p may list.
func (f *fleetService) Clusters(ctx context.Context, p identity.Principal) ([]model.ClusterInfo, error) {
	if err := f.validPrincipal(p); err != nil {
		return nil, err
	}
	specs := f.reg.List()
	out := make([]model.ClusterInfo, len(specs))
	var wg sync.WaitGroup
	for i, spec := range specs {
		out[i] = model.ClusterInfo{
			Name: spec.Name, DisplayName: spec.DisplayName, Environment: spec.Environment, Region: spec.Region,
			Color: spec.Color, Protected: spec.Protected, Order: spec.Order,
		}
		ci := &out[i]
		var s clusterSession
		if live := f.agents.session(spec.Name); live != nil {
			s = live
			ci.Connected = true
		} else if sv := f.agents.staleOf(spec.Name); sv != nil {
			s = sv
			ci.Stale = true
		} else {
			continue
		}
		h := s.hello()
		ci.LastSeen = s.lastSeenAt()
		ci.AgentVersion, ci.KubernetesVersion, ci.FluxVersion = h.AgentVersion, h.KubernetesVersion, h.FluxVersion
		ci.Mode, ci.ReadOnly, ci.Context = h.Mode, h.ReadOnly, h.Context
		ci.Presets = slices.Clone(h.Presets)
		wg.Go(func() {
			counts, kinds, err := f.counts(ctx, p, s)
			if err != nil {
				f.log.Debug("cluster counts unavailable", "cluster", s.name(), "err", err)
				return
			}
			ci.Counts, ci.Kinds = counts, kinds
			if ci.Findings, err = f.visibleFindings(ctx, p, s); err != nil {
				f.log.Debug("cluster findings unavailable", "cluster", s.name(), "err", err)
			}
		})
	}
	wg.Wait()
	return out, nil
}

// counts sums the status counts p may list, in total and per kind. A
// kind p may list cluster-wide is counted from its precomputed total; only
// the other kinds are summed per namespace (ADR-0006: O(kinds) for
// cluster-wide readers).
func (f *fleetService) counts(ctx context.Context, p identity.Principal, s clusterSession) (map[model.Status]int, map[string]map[model.Status]int, error) {
	snap := s.counted()
	wide := make([]accessTuple, 0, len(snap.kinds))
	for gr := range snap.kinds {
		wide = append(wide, accessTuple{Group: gr.group, Resource: gr.resource})
	}
	allowedWide, err := f.authz.allowedTuples(ctx, p, s.name(), "list", wide)
	if err != nil {
		return nil, nil, err
	}
	out := map[model.Status]int{}
	kinds := map[string]map[model.Status]int{}
	add := func(gr groupResource, byStatus map[model.Status]int) {
		kind := kindOfTuple(accessTuple{Group: gr.group, Resource: gr.resource})
		kc := kinds[kind]
		if kc == nil {
			kc = map[model.Status]int{}
			kinds[kind] = kc
		}
		for st, n := range byStatus {
			out[st] += n
			kc[st] += n
		}
	}
	var rest []accessTuple
	for _, t := range wide {
		gr := groupResource{t.Group, t.Resource}
		if allowedWide[t] {
			add(gr, snap.kinds[gr])
			continue
		}
		for _, nt := range snap.byKind[gr] {
			if nt.Namespace != "" {
				rest = append(rest, nt)
			}
		}
	}
	if len(rest) > 0 {
		allowed, err := f.authz.allowedTuples(ctx, p, s.name(), "list", rest)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range rest {
			if allowed[t] {
				add(groupResource{t.Group, t.Resource}, snap.tuples[t])
			}
		}
	}
	return out, kinds, nil
}

// clusterCounts is counts for one registered cluster's live or stale
// view; ok is false when it has none or the access checks failed.
func (f *fleetService) clusterCounts(ctx context.Context, p identity.Principal, cluster string) (map[model.Status]int, map[string]map[model.Status]int, bool) {
	s := f.readerOf(cluster)
	if s == nil || f.validPrincipal(p) != nil {
		return nil, nil, false
	}
	counts, kinds, err := f.counts(ctx, p, s)
	if err != nil {
		f.log.Debug("cluster counts unavailable", "cluster", cluster, "err", err)
		return nil, nil, false
	}
	return counts, kinds, true
}

// clusterFindings is visibleFindings for one registered cluster.
func (f *fleetService) clusterFindings(ctx context.Context, p identity.Principal, cluster string) ([]model.Finding, bool) {
	s := f.readerOf(cluster)
	if s == nil || f.validPrincipal(p) != nil {
		return nil, false
	}
	fs, err := f.visibleFindings(ctx, p, s)
	if err != nil {
		f.log.Debug("cluster findings unavailable", "cluster", cluster, "err", err)
		return nil, false
	}
	return fs, true
}

// readerOf returns the session that serves reads of a registered cluster,
// or nil.
func (f *fleetService) readerOf(cluster string) clusterSession {
	if _, ok := f.reg.Get(cluster); !ok {
		return nil
	}
	return f.agents.reader(cluster)
}

// tupleKinds maps (group, plural) to the Kind of the kind table.
var tupleKinds = func() map[[2]string]string {
	m := map[[2]string]string{}
	for _, k := range flux.All() {
		m[[2]string{k.Group, k.Plural}] = k.Kind
	}
	return m
}()

func kindOfTuple(t accessTuple) string { return tupleKinds[[2]string{t.Group, t.Resource}] }

// fleetVersion changes whenever anything /clusters shows may have changed:
// a view (rows or findings), a session or connection state, or the
// registry. It is the data version of the fleet-wide ETags.
func (f *fleetService) fleetVersion() string {
	return strconv.FormatUint(f.agents.rvSeq.Load(), 10)
}

// viewVersion returns the version of the view that serves reads of
// cluster (a different session has a different version), whether it is
// stale, and false when the cluster has none.
func (f *fleetService) viewVersion(cluster string) (string, bool, bool) {
	if _, ok := f.reg.Get(cluster); !ok {
		return "", false, false
	}
	s := f.agents.reader(cluster)
	v := viewOfSession(s)
	if v == nil {
		return "", false, false
	}
	_, stale := s.(*staleView)
	version := v.version()
	if stale {
		version += ".stale"
	}
	return version, stale, true
}

// matches applies a fleet.Filter whose kinds are already canonical.
func matches(r model.Resource, kinds []string, fl fleet.Filter) bool {
	if len(kinds) > 0 && !slices.Contains(kinds, r.Kind) {
		return false
	}
	if fl.Namespace != "" && r.Namespace != fl.Namespace {
		return false
	}
	if fl.Status != "" && r.Status != fl.Status {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(fl.Query)); q != "" {
		hay := strings.ToLower(r.Kind + "\x00" + r.Namespace + "\x00" + r.Name + "\x00" + r.Message)
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

func canonicalKinds(kinds []string) ([]string, error) {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if kd, ok := flux.KindByName(k); ok {
			out = append(out, kd.Kind)
			continue
		}
		// Kinds outside the table match inventory-only rows.
		if name, ok := flux.InventoryKind(k); ok {
			out = append(out, name)
			continue
		}
		if !flux.ValidKindName(k) {
			return nil, badRequest("unknown kind %q", truncate(k, 64))
		}
		out = append(out, k)
	}
	return out, nil
}

func sortResources(rs []model.Resource) {
	slices.SortFunc(rs, func(a, b model.Resource) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
}

// List returns the resources of cluster that match fl and that p may list.
func (f *fleetService) List(ctx context.Context, p identity.Principal, cluster string, fl fleet.Filter) ([]model.Resource, error) {
	items, _, err := f.list(ctx, p, cluster, fl)
	return items, err
}

// list is List plus the view's resourceVersion.
func (f *fleetService) list(ctx context.Context, p identity.Principal, cluster string, fl fleet.Filter) ([]model.Resource, string, error) {
	if err := f.validPrincipal(p); err != nil {
		return nil, "", err
	}
	kinds, err := canonicalKinds(fl.Kinds)
	if err != nil {
		return nil, "", err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return nil, "", err
	}
	all, rv := s.view()
	cand := all[:0]
	for _, r := range all {
		if matches(r, kinds, fl) {
			cand = append(cand, r)
		}
	}
	visible, err := f.authz.filter(ctx, p, cluster, cand)
	if err != nil {
		return nil, "", err
	}
	sortResources(visible)
	if fl.Limit > 0 && len(visible) > fl.Limit {
		visible = visible[:fl.Limit]
	}
	return visible, rv, nil
}

// Get returns one resource from the cluster's view: a watched summary or
// an inventory-only row (see locate).
func (f *fleetService) Get(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (model.Resource, error) {
	if err := f.validPrincipal(p); err != nil {
		return model.Resource{}, err
	}
	ref, _, row, err := f.locate(ctx, p, cluster, ref)
	if err != nil {
		return model.Resource{}, err
	}
	if row == nil {
		return model.Resource{}, fmt.Errorf("%w: %s", fleet.ErrNotFound, ref.ID())
	}
	return *row, nil
}

// visibleInventoryRow returns r when p may see it (the list rule), and
// ErrNotFound otherwise so that names do not leak.
func (f *fleetService) visibleInventoryRow(ctx context.Context, p identity.Principal, cluster string, r model.Resource) (model.Resource, error) {
	vis, err := f.authz.filter(ctx, p, cluster, []model.Resource{r})
	if err != nil {
		return model.Resource{}, err
	}
	if len(vis) == 0 {
		return model.Resource{}, fmt.Errorf("%w: %s", fleet.ErrNotFound, r.ID)
	}
	return r, nil
}

// Children returns the visible resources whose Owner is ref (inventory,
// Flux ownership labels and ownerReferences, as summarised by the agent).
func (f *fleetService) Children(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) ([]model.Resource, error) {
	parent, err := f.Get(ctx, p, cluster, ref)
	if err != nil {
		return nil, err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return nil, err
	}
	all, _ := s.view()
	var kids []model.Resource
	for _, r := range all {
		if r.Owner != nil && r.Owner.ID() == parent.Ref.ID() {
			kids = append(kids, r)
		}
	}
	kids, err = f.authz.filter(ctx, p, cluster, kids)
	if err != nil {
		return nil, err
	}
	sortResources(kids)
	if kids == nil {
		kids = []model.Resource{}
	}
	return kids, nil
}

// read sends an impersonated read of a visible object: a watched kind or
// any object named in a Kustomization inventory (locate). The agent reads
// it as p, so the cluster's RBAC decides.
func (f *fleetService) read(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, op protocol.Op) (json.RawMessage, error) {
	if err := f.validPrincipal(p); err != nil {
		return nil, err
	}
	ref, s, _, err := f.locate(ctx, p, cluster, ref)
	if err != nil {
		return nil, err
	}
	return s.do(ctx, protocol.Request{Op: op, Identity: protoIdentity(p), Target: ref})
}

// errSecretYAML refuses Secret YAML whether or not the object exists.
func errSecretYAML() error {
	return fmt.Errorf("%w: Eddy never shows the YAML of Secrets", fleet.ErrForbidden)
}

// YAML returns the sanitised, redacted manifest of ref. Secrets are always
// refused; every other kind the agent sanitises (ConfigMaps lose data and
// binaryData, env values are redacted) and the hub redacts again.
func (f *fleetService) YAML(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (string, error) {
	if !flux.YAMLAllowed(ref.Kind) {
		return "", errSecretYAML()
	}
	raw, err := f.read(ctx, p, cluster, ref, protocol.OpYAML)
	if err != nil {
		return "", err
	}
	var res protocol.YAMLResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("hub: decode yaml from %s: %w", cluster, err)
	}
	// The agent already sanitised the object; redact again before it leaves the hub.
	out, _ := redact.YAML(res.YAML)
	return out, nil
}

// Events returns the object's Kubernetes events, read as p.
func (f *fleetService) Events(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) ([]model.Event, error) {
	raw, err := f.read(ctx, p, cluster, ref, protocol.OpEvents)
	if err != nil {
		return nil, err
	}
	var res protocol.EventsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("hub: decode events from %s: %w", cluster, err)
	}
	if res.Events == nil {
		res.Events = []model.Event{}
	}
	return res.Events, nil
}

// Logs streams a pod's logs, read as p (the API server checks pods/log).
func (f *fleetService) Logs(ctx context.Context, p identity.Principal, cluster string, pod model.Ref, o fleet.LogOptions, w fleet.LineWriter) error {
	if err := f.validPrincipal(p); err != nil {
		return err
	}
	pod.Kind = flux.KindPod
	pod, _, err := canonicalRef(pod)
	if err != nil {
		return err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return err
	}
	if err := f.requireVisible(ctx, p, cluster, pod); err != nil {
		return err
	}
	tail := o.TailLines
	if tail <= 0 {
		tail = DefaultTailLines
	}
	args, err := json.Marshal(protocol.LogsArgs{Container: o.Container, Follow: o.Follow, TailLines: min(tail, MaxTailLines)})
	if err != nil {
		return fmt.Errorf("hub: encode log args: %w", err)
	}
	return s.stream(ctx, protocol.Request{Op: protocol.OpLogs, Identity: protoIdentity(p), Target: pod, Args: args},
		func(c protocol.LogChunk) error { return w.WriteLines(c.Lines) })
}

// Reconcile asks Flux to reconcile ref now.
func (f *fleetService) Reconcile(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, o fleet.ActionOptions) error {
	args, err := json.Marshal(protocol.ReconcileArgs{WithSource: o.WithSource})
	if err != nil {
		return fmt.Errorf("hub: encode reconcile args: %w", err)
	}
	return f.write(ctx, p, cluster, ref, protocol.OpReconcile, args, o)
}

// Suspend sets spec.suspend on ref.
func (f *fleetService) Suspend(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, o fleet.ActionOptions) error {
	return f.write(ctx, p, cluster, ref, protocol.OpSuspend, nil, o)
}

// Resume clears spec.suspend on ref.
func (f *fleetService) Resume(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, o fleet.ActionOptions) error {
	return f.write(ctx, p, cluster, ref, protocol.OpResume, nil, o)
}

func (f *fleetService) write(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, op protocol.Op, args json.RawMessage, o fleet.ActionOptions) error {
	target := store.ResourceRef{Cluster: cluster, Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	detail := map[string]any{}
	if op == protocol.OpReconcile {
		detail["withSource"] = o.WithSource
	}
	fail := func(res store.AuditResult, err error) error {
		detail["error"] = truncate(err.Error(), 512)
		f.rec.Record(ctx, p, string(op), target, res, detail)
		return err
	}
	if err := f.validPrincipal(p); err != nil {
		return fail(store.AuditDenied, err)
	}
	// Ask AI has no write path; a PAT needs the operate scope.
	if p.Via == identity.ViaAskAI || (p.Via == identity.ViaMCP && !p.Has(identity.ScopeOperate)) {
		return fail(store.AuditDenied, fmt.Errorf("%w: %s is not allowed through %s", fleet.ErrForbidden, op, p.Via))
	}
	ref, k, err := canonicalRef(ref)
	if err != nil {
		return fail(store.AuditError, err)
	}
	target.Group, target.Kind = ref.Group, ref.Kind
	if !k.Flux {
		return fail(store.AuditError, badRequest("%s does not support %s", k.Kind, op))
	}
	spec, ok := f.reg.Get(cluster)
	if !ok {
		return fail(store.AuditError, fmt.Errorf("%w: cluster %q", fleet.ErrNotFound, cluster))
	}
	if spec.Protected && o.Confirm != cluster {
		detail["reason"] = "confirmation required"
		f.rec.Record(ctx, p, string(op), target, store.AuditDenied, detail)
		return fmt.Errorf("%w: type the cluster name %q to confirm", fleet.ErrConfirmRequired, cluster)
	}
	s := f.agents.session(cluster)
	if s == nil {
		return fail(store.AuditError, fmt.Errorf("%w: %s", fleet.ErrDisconnected, cluster))
	}
	// The agent refuses these writes itself; the hub does not forward them.
	if s.hello().ReadOnly {
		return fail(store.AuditDenied, fmt.Errorf("%w: %s is read-only (its agent runs in read-only local mode)", fleet.ErrForbidden, cluster))
	}
	_, err = s.do(ctx, protocol.Request{Op: op, Identity: protoIdentity(p), Target: ref, Args: args})
	if err != nil {
		res := store.AuditError
		if errors.Is(err, fleet.ErrForbidden) {
			res = store.AuditDenied
		}
		return fail(res, err)
	}
	f.rec.Record(ctx, p, string(op), target, store.AuditOK, detail)
	return nil
}

// CanGet reports whether p may get ref (SAR, cached).
func (f *fleetService) CanGet(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (bool, error) {
	return f.can(ctx, p, cluster, ref, "get")
}

// CanPatch reports whether p may patch ref (SAR, cached).
func (f *fleetService) CanPatch(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (bool, error) {
	return f.can(ctx, p, cluster, ref, "patch")
}

// can answers verb on ref. For a kind outside the kind table (an
// inventory-only row, such as a thread target), get follows the row's
// visibility rule (locate) and patch is never granted: such objects have
// no Eddy writes, so only a thread's author may resolve it. ref.Group is
// exact there: "" is the core group.
func (f *fleetService) can(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, verb string) (bool, error) {
	if err := f.validPrincipal(p); err != nil {
		return false, err
	}
	if _, ok := f.reg.Get(cluster); !ok {
		return false, fmt.Errorf("%w: cluster %q", fleet.ErrNotFound, cluster)
	}
	k, ok := flux.KindByName(ref.Kind)
	if !ok || (ref.Group != "" && ref.Group != k.Group) {
		if verb != "get" || !flux.ValidKindName(ref.Kind) {
			return false, nil
		}
		if ref.Group == "" {
			ref.Group = coreGroupAlias
		}
		if _, _, _, err := f.locate(ctx, p, cluster, ref); err != nil {
			if errors.Is(err, fleet.ErrNotFound) || errors.Is(err, ErrBadRequest) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	c := protocol.AccessCheck{Verb: verb, Group: k.Group, Resource: k.Plural, Namespace: ref.Namespace, Name: ref.Name}
	res, err := f.authz.check(ctx, p, cluster, []protocol.AccessCheck{c})
	if err != nil {
		return false, err
	}
	return res[0], nil
}

// filterChange keeps the upserts and deletes of e that p may list.
func (f *fleetService) filterChange(ctx context.Context, p identity.Principal, e event) ([]model.Resource, []string, error) {
	ups, err := f.authz.filter(ctx, p, e.cluster, e.upserts)
	if err != nil {
		return nil, nil, err
	}
	if len(e.deletes) == 0 {
		return ups, nil, nil
	}
	seen := map[accessTuple]bool{}
	var tuples []accessTuple
	byID := make(map[string][]accessTuple, len(e.deletes))
	for _, id := range e.deletes {
		ref, err := model.ParseRef(id)
		if err != nil {
			continue
		}
		r := model.Resource{Ref: ref}
		if parent, ok := e.parents[id]; ok {
			r.InventoryOnly, r.Owner = true, &parent
		}
		ts, ok := requiredTuples(r)
		if !ok {
			continue
		}
		byID[id] = ts
		for _, t := range ts {
			if !seen[t] {
				seen[t] = true
				tuples = append(tuples, t)
			}
		}
	}
	allowed, err := f.authz.allowedTuples(ctx, p, e.cluster, "list", tuples)
	if err != nil {
		return nil, nil, err
	}
	var dels []string
	for _, id := range e.deletes {
		ts, ok := byID[id]
		if !ok {
			continue
		}
		visible := true
		for _, t := range ts {
			visible = visible && allowed[t]
		}
		if visible {
			dels = append(dels, id)
		}
	}
	return ups, dels, nil
}
