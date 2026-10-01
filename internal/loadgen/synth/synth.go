// Package synth generates synthetic cluster data for load tests and
// benchmarks (ADR-0006): resource summaries with a realistic kind mix,
// steady churn, findings, and a fake SubjectAccessReview policy for
// simulated users with different RBAC.
//
// Everything is deterministic: row i of a cluster is a pure function of the
// cluster index, i and the row's generation, so a fake agent holds almost
// nothing in memory and the hub's heap can be measured in the same process.
package synth

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
)

// Spec sizes one synthetic cluster.
type Spec struct {
	// Resources is the number of rows per cluster.
	Resources int
	// Namespaces defaults to max(20, Resources/75).
	Namespaces int
	// JobShare is the share of Jobs among the rows (default 0.5, like the
	// owner's stage cluster with thousands of finished Jobs).
	JobShare float64
}

func (s Spec) withDefaults() Spec {
	if s.Namespaces <= 0 {
		s.Namespaces = max(20, s.Resources/75)
	}
	if s.JobShare <= 0 {
		s.JobShare = 0.5
	}
	return s
}

// Teams is the number of teams namespaces are spread over. A team user
// (see Allow) may see the namespaces of one team only.
const Teams = 20

// Namespace returns the name of namespace n of a cluster.
func Namespace(n int) string {
	switch n {
	case 0:
		return "flux-system"
	case 1:
		return "kube-system"
	}
	apps := [...]string{"billing", "checkout", "search", "ledger", "payments", "identity", "catalog", "ingest", "reports", "notify"}
	return fmt.Sprintf("team-%02d-%s-%d", n%Teams, apps[(n/Teams)%len(apps)], n/(Teams*len(apps)))
}

// kindMix is the share (per mille) of every non-Job kind among the
// remaining rows. Jobs take Spec.JobShare first.
var kindMix = []struct {
	kind   string
	permil int
}{
	{flux.KindPod, 400},
	{flux.KindDeployment, 90},
	{flux.KindService, 90},
	{flux.KindHelmRelease, 60},
	{flux.KindKustomization, 40},
	{flux.KindCronJob, 40},
	{flux.KindStatefulSet, 25},
	{flux.KindIngress, 30},
	{flux.KindHPA, 25},
	{flux.KindPVC, 25},
	{flux.KindServiceAccount, 25},
	{flux.KindGitRepository, 10},
	{flux.KindHelmRepository, 10},
	{flux.KindOCIRepository, 10},
	{flux.KindHelmChart, 30},
	{"ConfigMap", 90}, // inventory-only rows
}

// Cluster is one synthetic cluster: its rows, their generations (bumped by
// churn) and the changes not yet drained. It implements the agent's Source
// and FindingSource.
type Cluster struct {
	Name  string
	Index int
	spec  Spec
	base  time.Time
	jobAt int // rows [0, jobAt) are Jobs

	mu      sync.Mutex
	gen     []uint32
	dirty   map[int]struct{}
	deleted []string
	rng     *rand.Rand
}

// NewCluster returns cluster number idx (named name) of spec s.
func NewCluster(name string, idx int, s Spec) *Cluster {
	s = s.withDefaults()
	return &Cluster{
		Name:  name,
		Index: idx,
		spec:  s,
		base:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		jobAt: int(float64(s.Resources) * s.JobShare),
		gen:   make([]uint32, s.Resources),
		dirty: map[int]struct{}{},
		rng:   rand.New(rand.NewPCG(uint64(idx)+1, 0x9e3779b97f4a7c15)),
	}
}

// Len is the number of rows.
func (c *Cluster) Len() int { return c.spec.Resources }

// hash is a cheap deterministic per-row hash (independent of the seed, so
// rows are the same across runs).
func hash(parts ...int) uint64 {
	h := uint64(1469598103934665603)
	for _, p := range parts {
		h ^= uint64(p)
		h *= 1099511628211
		h ^= h >> 29
	}
	return h
}

// kindOf picks the kind of row i.
func (c *Cluster) kindOf(i int) string {
	if i < c.jobAt {
		return flux.KindJob
	}
	p := int(hash(c.Index, i, 7) % 1000)
	for _, k := range kindMix {
		if p < k.permil {
			return k.kind
		}
		p -= k.permil
	}
	return flux.KindPod
}

// Counted reports the rows that ClusterInfo.counts include (everything
// but inventory-only rows).
func (c *Cluster) Counted() int {
	n := 0
	for i := range c.spec.Resources {
		if c.kindOf(i) != "ConfigMap" {
			n++
		}
	}
	return n
}

// Snapshot returns every row at its current generation.
func (c *Cluster) Snapshot() []model.Resource {
	c.mu.Lock()
	gens := append([]uint32(nil), c.gen...)
	c.dirty = map[int]struct{}{}
	c.deleted = nil
	c.mu.Unlock()
	out := make([]model.Resource, len(gens))
	for i, g := range gens {
		out[i] = c.row(i, g)
	}
	return out
}

// Churn changes n random rows. A changed Job is replaced by a new Job (a
// delete and an upsert, as CronJobs do); any other row changes status,
// message and resourceVersion.
func (c *Cluster) Churn(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for range n {
		i := c.rng.IntN(len(c.gen))
		if i < c.jobAt {
			old := c.row(i, c.gen[i])
			c.deleted = append(c.deleted, old.ID)
		}
		c.gen[i]++
		c.dirty[i] = struct{}{}
	}
}

// Drain returns the changes since the last Drain or Snapshot.
func (c *Cluster) Drain() ([]model.Resource, []string) {
	c.mu.Lock()
	dirty, deleted := c.dirty, c.deleted
	gens := make(map[int]uint32, len(dirty))
	for i := range dirty {
		gens[i] = c.gen[i]
	}
	c.dirty, c.deleted = map[int]struct{}{}, nil
	c.mu.Unlock()
	ups := make([]model.Resource, 0, len(gens))
	for i, g := range gens {
		ups = append(ups, c.row(i, g))
	}
	return ups, deleted
}

// Findings returns a few Job buildup findings (one per 25 namespaces).
func (c *Cluster) Findings() ([]model.Finding, uint64) {
	var out []model.Finding
	for n := 2; n < c.spec.Namespaces; n += 25 {
		ns := Namespace(n)
		out = append(out, model.Finding{
			ID: string(model.FindingJobBuildup) + "/" + ns, Kind: model.FindingJobBuildup, Severity: model.SeverityWarning,
			Namespace: ns, Message: "812 finished Jobs are piling up", Recommendation: "Set ttlSecondsAfterFinished on the Jobs",
			Jobs: &model.JobBuildup{Hidden: 812, Finished: 840, Succeeded: 800, Failed: 40, WithoutTTL: 812, Threshold: 500,
				Groups: []model.JobGroup{{By: "owner", Name: "nightly-sync", Label: "CronJob nightly-sync", Count: 700}}},
		})
	}
	return out, 1
}

var appNames = [...]string{"api", "worker", "web", "scheduler", "sync", "exporter", "gateway", "consumer", "indexer", "cache", "auth", "mailer"}

// Row returns row i at its current generation (tests).
func (c *Cluster) Row(i int) model.Resource {
	c.mu.Lock()
	g := c.gen[i]
	c.mu.Unlock()
	return c.row(i, g)
}

// row builds row i at generation g.
func (c *Cluster) row(i int, g uint32) model.Resource {
	h := hash(c.Index, i)
	ns := Namespace(2 + int(h%uint64(c.spec.Namespaces-2)))
	kind := c.kindOf(i)
	app := appNames[(h>>8)%uint64(len(appNames))]
	inst := app + "-" + strconv.Itoa(int((h>>16)%40))
	created := c.base.Add(time.Duration(h%(30*24*3600)) * time.Second)
	changed := created.Add(time.Duration(g) * time.Minute)
	rv := strconv.FormatUint(100000000+uint64(i)*7+uint64(g)*1_000_003, 10)
	labels := map[string]string{
		"app.kubernetes.io/name":       app,
		"app.kubernetes.io/instance":   inst,
		"app.kubernetes.io/managed-by": "Helm",
	}
	team := ns[:min(len(ns), 7)]
	image := fmt.Sprintf("registry.example.com/%s/%s:1.%d.%d", team, app, (h>>20)%60, (h>>26)%20)

	// One row in 50 fails, more often after churn.
	failing := hash(c.Index, i, int(g), 3)%50 == 0
	r := model.Resource{
		Ref:             model.Ref{Kind: kind, Namespace: ns},
		Version:         "v1",
		Status:          model.StatusReady,
		Labels:          labels,
		CreatedAt:       created,
		LastChanged:     changed,
		ResourceVersion: rv,
	}
	if k, ok := flux.KindByName(kind); ok {
		r.Group = k.Group
		r.Project = flux.ProjectOf(k.Group).ID
	}
	ready := func(msg string) []model.Condition {
		st, reason := "True", "Succeeded"
		if failing {
			st, reason = "False", "ReconciliationFailed"
		}
		return []model.Condition{{Type: "Ready", Status: st, Reason: reason, Message: msg, LastTransitionTime: changed}}
	}
	switch kind {
	case flux.KindJob:
		run := 29_000_000 + i*4096 + int(g%4096)
		r.Name = fmt.Sprintf("%s-%s-nightly-%d", inst, app, run)
		r.Status = model.StatusCompleted
		r.Message = "Job completed"
		r.Completions = "1/1"
		r.Images = []string{image}
		r.Owner = &model.Ref{Group: flux.GroupBatch, Kind: flux.KindCronJob, Namespace: ns, Name: inst + "-" + app + "-nightly"}
		r.Conditions = []model.Condition{
			{Type: "SuccessCriteriaMet", Status: "True", Reason: "CompletionsReached", Message: "Reached expected number of succeeded pods", LastTransitionTime: changed},
			{Type: "Complete", Status: "True", Reason: "CompletionsReached", Message: "Reached expected number of succeeded pods", LastTransitionTime: changed},
		}
		if failing {
			r.Status, r.Message, r.Completions = model.StatusFailed, "BackoffLimitExceeded: Job has reached the specified backoff limit", "0/1"
			r.Conditions = []model.Condition{{Type: "Failed", Status: "True", Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", LastTransitionTime: changed}}
		}
	case flux.KindPod:
		r.Name = fmt.Sprintf("%s-%x-%s", inst, h%0xfffffff, strconv.FormatUint((h>>32)%60466176, 36))
		r.Images = []string{image}
		r.Containers = []string{app}
		r.Owner = &model.Ref{Group: flux.GroupApps, Kind: "ReplicaSet", Namespace: ns, Name: fmt.Sprintf("%s-%x", inst, h%0xfffffff)}
		r.Message = "Running"
		r.Conditions = []model.Condition{{Type: "Ready", Status: "True", LastTransitionTime: changed}}
		if failing {
			r.Status, r.Message = model.StatusFailed, "CrashLoopBackOff: back-off 5m0s restarting failed container "+app
		}
	case flux.KindDeployment, flux.KindStatefulSet:
		r.Name = inst
		r.Replicas = "3/3"
		r.Images = []string{image}
		r.Owner = &model.Ref{Group: flux.GroupHelm, Kind: flux.KindHelmRelease, Namespace: ns, Name: inst}
		r.Message = "Deployment has minimum availability."
		if failing {
			r.Status, r.Replicas, r.Message = model.StatusFailed, "1/3", "ProgressDeadlineExceeded: ReplicaSet has timed out progressing"
		}
	case flux.KindHelmRelease:
		r.Name = inst
		r.Version = "v2"
		r.Chart = app + "@1." + strconv.Itoa(int((h>>20)%60)) + ".0"
		r.Revision = "1." + strconv.Itoa(int((h>>20)%60)) + ".0"
		r.Interval = "10m"
		r.Source = &model.Ref{Group: flux.GroupSource, Kind: flux.KindHelmRepository, Namespace: "flux-system", Name: "charts"}
		r.Owner = &model.Ref{Group: flux.GroupKustomize, Kind: flux.KindKustomization, Namespace: "flux-system", Name: team}
		r.Message = "Helm upgrade succeeded for release " + ns + "/" + inst + " with chart " + r.Chart
		r.Conditions = ready(r.Message)
		if failing {
			r.Status, r.Message = model.StatusFailed, "Helm upgrade failed for release "+ns+"/"+inst+": context deadline exceeded"
		} else if g%7 == 3 {
			r.Status, r.Message = model.StatusReconciling, "Running 'upgrade' action with timeout of 5m0s"
		}
	case flux.KindKustomization:
		r.Name = fmt.Sprintf("%s-%d", app, i)
		r.Revision = fmt.Sprintf("main@sha1:%040x", h)
		r.Interval = "5m"
		r.Inventory = int(h % 120)
		r.Source = &model.Ref{Group: flux.GroupSource, Kind: flux.KindGitRepository, Namespace: "flux-system", Name: "fleet"}
		r.Message = "Applied revision: " + r.Revision
		r.Conditions = ready(r.Message)
		if failing {
			r.Status, r.Message = model.StatusFailed, "kustomize build failed: accumulating resources: missing file"
		} else if h%41 == 0 {
			r.Status, r.Suspended, r.Message = model.StatusSuspended, true, "Reconciliation is suspended"
		}
	case flux.KindGitRepository, flux.KindHelmRepository, flux.KindOCIRepository, flux.KindHelmChart:
		r.Name = fmt.Sprintf("%s-%d", app, i)
		r.URL = "https://git.example.com/" + team + "/" + app + ".git"
		r.Revision = fmt.Sprintf("main@sha1:%040x", h)
		r.Interval = "1m"
		r.Message = "stored artifact for revision '" + r.Revision + "'"
		r.Conditions = ready(r.Message)
		if failing {
			r.Status, r.Message = model.StatusFailed, "failed to checkout and determine revision: unable to clone: authentication required"
		}
	case flux.KindCronJob:
		r.Name = inst + "-" + app + "-nightly"
		r.Schedule = "0 2 * * *"
		r.Images = []string{image}
	case flux.KindService:
		r.Name = inst
		r.Ports = []string{"80/TCP → 8080", "9090/TCP"}
	case flux.KindIngress:
		r.Name = inst
		r.Hosts = []string{inst + "." + team + ".example.com"}
	case flux.KindHPA:
		r.Name = inst
		r.Message = "the HPA was able to successfully calculate a replica count from cpu resource utilization"
	case flux.KindPVC:
		r.Name = "data-" + inst + "-0"
		r.Message = "Bound"
	case flux.KindServiceAccount:
		r.Name = inst
	default: // an inventory-only row
		r = model.Resource{
			Ref:           model.Ref{Kind: kind, Namespace: ns, Name: fmt.Sprintf("%s-config-%d", inst, i)},
			Version:       "v1",
			Status:        model.StatusUnknown,
			InventoryOnly: true,
			Owner:         &model.Ref{Group: flux.GroupKustomize, Kind: flux.KindKustomization, Namespace: "flux-system", Name: team},
		}
	}
	if r.Name == "" {
		r.Name = fmt.Sprintf("%s-%d", kind, i)
	}
	if strings.ContainsAny(r.Name, "/") {
		r.Name = strings.ReplaceAll(r.Name, "/", "-")
	}
	// Names must stay unique per (kind, namespace): suffix rows that would collide.
	if kind != flux.KindJob && kind != flux.KindPod {
		r.Name += "-" + strconv.Itoa(i)
	}
	r.ID = r.Ref.ID()
	return r
}
