package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// jobsTuple is the access tuple a job-buildup finding and the hidden Jobs of
// namespace need: list on batch/jobs there.
func jobsTuple(namespace string) accessTuple {
	return accessTuple{Group: flux.GroupBatch, Resource: "jobs", Namespace: namespace}
}

// visibleFindings returns the findings of s that p may see: a job-buildup
// finding needs list on Jobs in its namespace.
func (f *fleetService) visibleFindings(ctx context.Context, p identity.Principal, s clusterSession) ([]model.Finding, error) {
	all := s.findingList()
	if len(all) == 0 {
		return nil, nil
	}
	tuples := make([]accessTuple, 0, len(all))
	for _, fd := range all {
		tuples = append(tuples, jobsTuple(fd.Namespace))
	}
	allowed, err := f.authz.allowedTuples(ctx, p, s.name(), "list", tuples)
	if err != nil {
		return nil, err
	}
	var out []model.Finding
	for _, fd := range all {
		if allowed[jobsTuple(fd.Namespace)] {
			out = append(out, fd)
		}
	}
	return out, nil
}

// Findings returns the cluster's findings that p may see.
func (f *fleetService) Findings(ctx context.Context, p identity.Principal, cluster string) ([]model.Finding, error) {
	if err := f.validPrincipal(p); err != nil {
		return nil, err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return nil, err
	}
	out, err := f.visibleFindings(ctx, p, s)
	if out == nil {
		out = []model.Finding{}
	}
	return out, err
}

// hiddenJobsPage is a page of Jobs the agent's Job policy hides.
type hiddenJobsPage struct {
	Items []model.Resource
	Total int
	Next  int
}

// HiddenJobs returns a page of the finished Jobs the agent does not list,
// in the namespaces (or the one namespace) where p may list Jobs. Only
// namespaces with a job-buildup finding have hidden Jobs.
func (f *fleetService) HiddenJobs(ctx context.Context, p identity.Principal, cluster, namespace string, offset, limit int) (hiddenJobsPage, error) {
	if err := f.validPrincipal(p); err != nil {
		return hiddenJobsPage{}, err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return hiddenJobsPage{}, err
	}
	visible, err := f.visibleFindings(ctx, p, s)
	if err != nil {
		return hiddenJobsPage{}, err
	}
	var namespaces []string
	for _, fd := range visible {
		if fd.Kind == model.FindingJobBuildup && (namespace == "" || fd.Namespace == namespace) && !slices.Contains(namespaces, fd.Namespace) {
			namespaces = append(namespaces, fd.Namespace)
		}
	}
	if len(namespaces) == 0 {
		return hiddenJobsPage{Items: []model.Resource{}}, nil
	}
	args, err := json.Marshal(protocol.HiddenJobsArgs{Namespaces: namespaces, Offset: offset, Limit: limit})
	if err != nil {
		return hiddenJobsPage{}, fmt.Errorf("hub: encode hidden jobs args: %w", err)
	}
	raw, err := s.do(ctx, protocol.Request{Op: protocol.OpHiddenJobs, Identity: protoIdentity(p), Args: args})
	if err != nil {
		return hiddenJobsPage{}, err
	}
	var res protocol.HiddenJobsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return hiddenJobsPage{}, fmt.Errorf("hub: decode hidden jobs from %s: %w", cluster, err)
	}
	// The agent answered from its cache for the namespaces we asked about;
	// sanitize and filter again, as for every other read.
	items := make([]model.Resource, 0, len(res.Items))
	for _, r := range res.Items {
		if r, ok := sanitizeResource(r); ok && r.Kind == flux.KindJob && slices.Contains(namespaces, r.Namespace) {
			items = append(items, r)
		}
	}
	items, err = f.authz.filter(ctx, p, cluster, items)
	if err != nil {
		return hiddenJobsPage{}, err
	}
	return hiddenJobsPage{Items: items, Total: res.Total, Next: res.Next}, nil
}

// workloadLogKinds are the kinds whose logs the hub streams pod by pod.
var workloadLogKinds = []string{flux.KindDeployment, flux.KindStatefulSet, flux.KindDaemonSet, flux.KindJob}

// workloadLogOptions for WorkloadLogs.
type workloadLogOptions struct {
	Container     string
	Pods          []string
	AllContainers *bool
	TailLines     int64
	SinceSeconds  int64
	Follow        bool
}

// checkWorkload validates a workload log target and that p may see it. A
// Job the agent hides is not in the view; it only needs to be readable.
func (f *fleetService) checkWorkload(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (model.Ref, clusterSession, error) {
	if err := f.validPrincipal(p); err != nil {
		return model.Ref{}, nil, err
	}
	ref, k, err := canonicalRef(ref)
	if err != nil {
		return model.Ref{}, nil, err
	}
	if !slices.Contains(workloadLogKinds, k.Kind) {
		return model.Ref{}, nil, badRequest("logs are available for Deployments, StatefulSets, DaemonSets and Jobs, not %s", k.Kind)
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return model.Ref{}, nil, err
	}
	if _, ok := s.lookup(ref.ID()); !ok && k.Kind != flux.KindJob {
		return model.Ref{}, nil, fmt.Errorf("%w: %s", fleet.ErrNotFound, ref.ID())
	}
	if err := f.requireVisible(ctx, p, cluster, ref); err != nil {
		return model.Ref{}, nil, err
	}
	return ref, s, nil
}

// WorkloadLogs streams the logs of a workload's pods, read as p: the agent
// impersonates p for every pod's logs. onChunk gets pod sets and entries;
// pods p may not see (neither list pods in the namespace nor get the pod)
// are removed from both, so names do not leak.
func (f *fleetService) WorkloadLogs(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, o workloadLogOptions, onChunk func(protocol.LogChunk) error) error {
	ref, s, err := f.checkWorkload(ctx, p, cluster, ref)
	if err != nil {
		return err
	}
	args, err := json.Marshal(protocol.LogsArgs{
		Container: o.Container, Follow: o.Follow, TailLines: o.TailLines, SinceSeconds: o.SinceSeconds,
		Pods: o.Pods, AllContainers: o.AllContainers,
	})
	if err != nil {
		return fmt.Errorf("hub: encode log args: %w", err)
	}
	pv := &podVisibility{f: f, p: p, cluster: cluster, namespace: ref.Namespace, seen: map[string]bool{}}
	return s.stream(ctx, protocol.Request{Op: protocol.OpLogs, Identity: protoIdentity(p), Target: ref, Args: args},
		func(c protocol.LogChunk) error {
			c, err := pv.filter(ctx, c)
			if err != nil {
				return err
			}
			if c.Pods == nil && len(c.Entries) == 0 {
				return nil
			}
			return onChunk(c)
		})
}

// podVisibility filters one workload log stream by pod visibility.
type podVisibility struct {
	f         *fleetService
	p         identity.Principal
	cluster   string
	namespace string
	listAll   *bool
	seen      map[string]bool
}

func (v *podVisibility) visible(ctx context.Context, pod string) (bool, error) {
	if v.listAll == nil {
		allowed, err := v.f.authz.allowedTuples(ctx, v.p, v.cluster, "list", []accessTuple{{Group: flux.GroupCore, Resource: "pods", Namespace: v.namespace}})
		if err != nil {
			return false, err
		}
		all := allowed[accessTuple{Group: flux.GroupCore, Resource: "pods", Namespace: v.namespace}]
		v.listAll = &all
	}
	if *v.listAll || pod == "" {
		return true, nil
	}
	if ok, known := v.seen[pod]; known {
		return ok, nil
	}
	ok, err := v.f.canSee(ctx, v.p, v.cluster, model.Ref{Group: flux.GroupCore, Kind: flux.KindPod, Namespace: v.namespace, Name: pod})
	if err != nil {
		return false, err
	}
	v.seen[pod] = ok
	return ok, nil
}

func (v *podVisibility) filter(ctx context.Context, c protocol.LogChunk) (protocol.LogChunk, error) {
	c.Lines = nil // a workload stream carries entries only
	if c.Pods != nil {
		set := *c.Pods
		set.Pods = make([]protocol.LogPod, 0, len(c.Pods.Pods))
		for _, pod := range c.Pods.Pods {
			ok, err := v.visible(ctx, pod.Name)
			if err != nil {
				return c, err
			}
			if ok {
				set.Pods = append(set.Pods, pod)
			}
		}
		if len(set.Pods) < len(c.Pods.Pods) {
			set.Total = len(set.Pods)
		}
		c.Pods = &set
	}
	if len(c.Entries) > 0 {
		out := c.Entries[:0:0]
		for _, e := range c.Entries {
			ok, err := v.visible(ctx, e.Pod)
			if err != nil {
				return c, err
			}
			if ok {
				out = append(out, e)
			}
		}
		c.Entries = out
	}
	return c, nil
}
