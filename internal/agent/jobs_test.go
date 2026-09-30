package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// jobSpec describes a synthetic Job.
type jobSpec struct {
	ns, name string
	labels   map[string]string
	owner    string // CronJob name
	finished time.Time
	failed   bool
	active   bool
	ttl      bool
}

func jobObj(s jobSpec) *unstructured.Unstructured {
	j := u("batch/v1", "Job", s.ns, s.name, map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "main", "image": "prefect:3"}}}}})
	j.SetCreationTimestamp(metav1.NewTime(s.finished.Add(-time.Minute)))
	j.SetResourceVersion("1")
	j.SetLabels(s.labels)
	if s.ttl {
		j.Object["spec"].(map[string]any)["ttlSecondsAfterFinished"] = int64(3600)
	}
	if s.owner != "" {
		owned(j, "batch/v1", "CronJob", s.owner)
	}
	status := map[string]any{}
	switch {
	case s.active:
		status["active"] = int64(1)
	case s.failed:
		status["failed"] = int64(1)
		status["conditions"] = []any{map[string]any{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded",
			"lastTransitionTime": s.finished.UTC().Format(time.RFC3339)}}
	default:
		status["succeeded"] = int64(1)
		status["conditions"] = []any{map[string]any{"type": "Complete", "status": "True",
			"lastTransitionTime": s.finished.UTC().Format(time.RFC3339)}}
	}
	j.Object["status"] = status
	return j
}

func prefect(deployment string) map[string]string {
	return map[string]string{
		"prefect.io/flow-run-name":  "hasty-cougar",
		"prefect.io/flow-name":      "expire-applications",
		flux.LabelPrefectDeployment: deployment,
		flux.LabelPrefectWorkPool:   "k8s-pool",
		"prefect.io/worker-name":    "KubernetesWorker 1f2e3d4c",
	}
}

// jobCache is a Cache fed by calling its event handlers directly, with a
// settable clock: fast and deterministic for thousands of Jobs.
type jobCache struct {
	*Cache
	now  time.Time
	jobs cache.ResourceEventHandlerFuncs
	pods cache.ResourceEventHandlerFuncs
}

func newJobCache(t *testing.T, p JobPolicy) *jobCache {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds())
	c, err := NewCache(dyn, testServed, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	c.SetJobPolicy(p)
	jk, _ := flux.KindByName(flux.KindJob)
	pk, _ := flux.KindByName(flux.KindPod)
	jc := &jobCache{Cache: c, now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), jobs: c.handler(jk), pods: c.handler(pk)}
	c.now = func() time.Time { return jc.now }
	return jc
}

func (jc *jobCache) add(s jobSpec) { jc.jobs.AddFunc(jobObj(s)) }

func jobIDs(rs []model.Resource) []string {
	var out []string
	for _, r := range rs {
		if r.Kind == flux.KindJob {
			out = append(out, r.ID)
		}
	}
	return out
}

func jobID(ns, name string) string { return "batch/Job/" + ns + "/" + name }

func TestJobPolicyPrefectBuildup(t *testing.T) {
	jc := newJobCache(t, JobPolicy{})
	now := jc.now
	// 15,000 succeeded runs of one Prefect deployment, one a minute; 50 and
	// 33 runs of two others; 84 failed standalone runs from long ago; two
	// active runs and one failure from an hour ago.
	n := 0
	add := func(s jobSpec) {
		n++
		s.ns = "prefect"
		s.name = fmt.Sprintf("run-%05d-word", n) // no generated suffix
		jc.add(s)
	}
	for i := range 15000 {
		add(jobSpec{labels: prefect("application-domain-expire-applications-job"), finished: now.Add(-time.Duration(i+10) * time.Minute)})
	}
	for i := range 50 {
		add(jobSpec{labels: prefect("sync-ledgers"), finished: now.Add(-time.Duration(i+5) * time.Hour)})
	}
	for i := range 33 {
		add(jobSpec{labels: prefect("reports"), finished: now.Add(-time.Duration(i+2) * time.Hour), ttl: true})
	}
	for i := range 84 {
		add(jobSpec{labels: prefect("application-domain-expire-applications-job"), failed: true, finished: time.Date(2025, 11, 1+i%28, 0, 0, 0, 0, time.UTC)})
	}
	add(jobSpec{labels: prefect("sync-ledgers"), active: true, finished: now})
	add(jobSpec{labels: prefect("reports"), active: true, finished: now})
	recentFail := n + 1
	add(jobSpec{labels: prefect("reports"), failed: true, finished: now.Add(-time.Hour)})

	snap := jc.Snapshot()
	ids := jobIDs(snap)
	// 2 active + 1 recent failure + the newest 5 of each of 3 groups (the
	// recent failure is also the newest finished "reports" run).
	if len(ids) != 2+1+5+5+4 {
		t.Fatalf("surfaced %d jobs: %v", len(ids), ids)
	}
	if !slices.Contains(ids, jobID("prefect", fmt.Sprintf("run-%05d-word", recentFail))) {
		t.Fatal("recent failure not surfaced")
	}
	// The newest five of the big group are runs 1..5.
	for i := 1; i <= 5; i++ {
		if !slices.Contains(ids, jobID("prefect", fmt.Sprintf("run-%05d-word", i))) {
			t.Fatalf("run %d of the big group not surfaced: %v", i, ids)
		}
	}
	fs, ver := jc.Findings()
	if len(fs) != 1 || ver == 0 {
		t.Fatalf("findings %+v", fs)
	}
	f := fs[0]
	h := f.Jobs
	finished := 15000 + 50 + 33 + 84 + 1
	if f.ID != "job-buildup/prefect" || f.Kind != model.FindingJobBuildup || f.Severity != model.SeverityWarning || f.Namespace != "prefect" {
		t.Fatalf("finding %+v", f)
	}
	if h.Finished != finished || h.Hidden != finished-15 || h.Failed != 85 || h.Succeeded != finished-85 ||
		h.WithoutTTL != finished-33 || h.StandaloneFailed != 85 || h.Threshold != 100 {
		t.Fatalf("buildup %+v", h)
	}
	if !h.Oldest.Equal(time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)) || !h.Newest.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("oldest %v newest %v", h.Oldest, h.Newest)
	}
	if len(h.Groups) != 3 || h.Groups[0].Label != "prefect deployment application-domain-expire-applications-job" || h.Groups[0].Count != 15084 || h.Groups[0].Failed != 84 {
		t.Fatalf("groups %+v", h.Groups)
	}
	if !strings.Contains(f.Message, "15,168 finished Jobs in prefect (15,153 hidden), 15,135 without ttlSecondsAfterFinished") ||
		!strings.Contains(f.Message, "85 failed standalone Jobs are never garbage-collected") || !strings.Contains(f.Recommendation, "Prefect") {
		t.Fatalf("finding %q / %q", f.Message, f.Recommendation)
	}
	if again, ver2 := jc.Findings(); ver2 != ver || len(again) != 1 {
		t.Fatal("findings version moved without a change")
	}

	// Hidden Jobs page through, newest first, and fit a frame.
	page := jc.HiddenJobs([]string{"prefect", "other"}, 0, 0, 1<<20)
	if page.Total != finished-15 || len(page.Items) != defaultHiddenJobs || page.Next != defaultHiddenJobs {
		t.Fatalf("page total %d items %d next %d", page.Total, len(page.Items), page.Next)
	}
	if page.Items[0].Name != "run-00006-word" || slices.Contains(ids, page.Items[0].ID) {
		t.Fatalf("first hidden %s", page.Items[0].Name)
	}
	small := jc.HiddenJobs([]string{"prefect"}, page.Next, maxHiddenJobs+5, 4096)
	if len(small.Items) == 0 || len(small.Items) > 10 || small.Next != page.Next+len(small.Items) {
		t.Fatalf("budgeted page %d next %d", len(small.Items), small.Next)
	}
	last := jc.HiddenJobs([]string{"prefect"}, page.Total-3, 10, 1<<20)
	if len(last.Items) != 3 || last.Next != 0 {
		t.Fatalf("last page %d next %d", len(last.Items), last.Next)
	}
	if none := jc.HiddenJobs([]string{"other"}, 0, 10, 1<<20); none.Total != 0 || none.Items == nil {
		t.Fatalf("other namespace %+v", none)
	}
}

func TestJobPolicyDeltasAndAging(t *testing.T) {
	jc := newJobCache(t, JobPolicy{History: 2, FailedMaxAge: time.Hour, BuildupThreshold: 4})
	now := jc.now
	for i := range 6 {
		jc.add(jobSpec{ns: "batch", name: fmt.Sprintf("nightly-%d", 29260000+i), owner: "nightly", finished: now.Add(-time.Duration(10-i) * time.Hour)})
	}
	jc.add(jobSpec{ns: "batch", name: "oneoff-failed", failed: true, finished: now.Add(-30 * time.Minute)})
	snap := byID(jc.Snapshot())
	for _, want := range []string{"nightly-29260005", "nightly-29260004", "oneoff-failed"} {
		if _, ok := snap[jobID("batch", want)]; !ok {
			t.Fatalf("%s not surfaced: %v", want, jobIDs(jc.Snapshot()))
		}
	}
	if len(jobIDs(jc.Snapshot())) != 3 { // 2 of nightly, the failure
		t.Fatalf("surfaced %v", jobIDs(jc.Snapshot()))
	}
	fs, _ := jc.Findings()
	if len(fs) != 1 || fs[0].Severity != model.SeverityInfo || fs[0].Message != "4 older finished Jobs hidden" || fs[0].Jobs.Hidden != 4 || fs[0].Recommendation != "" {
		t.Fatalf("finding %+v", fs)
	}
	if !strings.Contains(jobRecommendation(&model.JobBuildup{WithoutTTL: 1, Groups: fs[0].Jobs.Groups[:1]}, false), "successfulJobsHistoryLimit") {
		t.Fatal("CronJob groups should recommend history limits")
	}

	// A new run arrives: it surfaces and the oldest shown run is hidden.
	jc.add(jobSpec{ns: "batch", name: "nightly-29260006", owner: "nightly", finished: now.Add(-time.Minute)})
	ups, dels := jc.Drain()
	if len(ups) != 1 {
		t.Fatalf("upserts %v", jobIDs(ups))
	}
	if !slices.Contains(jobIDs(ups), jobID("batch", "nightly-29260006")) || !slices.Equal(dels, []string{jobID("batch", "nightly-29260004")}) {
		t.Fatalf("delta upserts %v deletes %v", jobIDs(ups), dels)
	}
	// The finding went over the threshold (5 hidden > 4).
	if fs, _ := jc.Findings(); fs[0].Severity != model.SeverityWarning || fs[0].Jobs.Hidden != 5 {
		t.Fatalf("finding after new run %+v", fs[0])
	}
	if ups, dels := jc.Drain(); len(ups)+len(dels) != 0 {
		t.Fatalf("no change expected, got %v %v", jobIDs(ups), dels)
	}

	// Two newer failures join the namespace group (their names have no
	// generated suffix). Once all three age out of FailedMaxAge only the
	// newest two of the group stay listed.
	jc.add(jobSpec{ns: "batch", name: "adhoc-broken", failed: true, finished: now.Add(-20 * time.Minute)})
	jc.add(jobSpec{ns: "batch", name: "adhoc-crashed", failed: true, finished: now.Add(-10 * time.Minute)})
	jc.Drain()
	jc.now = now.Add(2 * time.Hour)
	_, dels = jc.Drain()
	if !slices.Equal(dels, []string{jobID("batch", "oneoff-failed")}) {
		t.Fatalf("aged-out failure not hidden: deletes %v", dels)
	}

	// Deleting a shown Job surfaces the next one.
	jc.jobs.DeleteFunc(jobObj(jobSpec{ns: "batch", name: "nightly-29260006", owner: "nightly", finished: now.Add(-time.Minute)}))
	ups, dels = jc.Drain()
	if !slices.Equal(dels, []string{jobID("batch", "nightly-29260006")}) || !slices.Contains(jobIDs(ups), jobID("batch", "nightly-29260004")) {
		t.Fatalf("after delete: upserts %v deletes %v", jobIDs(ups), dels)
	}

	// When nothing is hidden any more the finding goes away.
	for i := range 6 {
		jc.jobs.DeleteFunc(jobObj(jobSpec{ns: "batch", name: fmt.Sprintf("nightly-%d", 29260000+i)}))
	}
	jc.jobs.DeleteFunc(jobObj(jobSpec{ns: "batch", name: "oneoff-failed"}))
	jc.Drain()
	if fs, _ := jc.Findings(); len(fs) != 0 {
		t.Fatalf("finding not removed: %+v", fs)
	}
}

func TestJobPolicyNamespaceCapAndActive(t *testing.T) {
	jc := newJobCache(t, JobPolicy{History: 1})
	now := jc.now
	// 200 Jobs with distinct generated prefixes: one group each, but at
	// most History×10 finished Jobs are listed per namespace.
	for i := range 200 {
		jc.add(jobSpec{ns: "ci", name: fmt.Sprintf("build%d-1", i), finished: now.Add(-time.Duration(i) * time.Minute)})
	}
	for i := range 30 {
		jc.add(jobSpec{ns: "ci", name: fmt.Sprintf("live-%d", i), active: true, finished: now})
	}
	snap := jc.Snapshot()
	ids := jobIDs(snap)
	if len(ids) != 10+30 {
		t.Fatalf("surfaced %d", len(ids))
	}
	if !slices.Contains(ids, jobID("ci", "build0-1")) || slices.Contains(ids, jobID("ci", "build10-1")) {
		t.Fatalf("cap must keep the newest: %v", ids)
	}
	if fs, _ := jc.Findings(); len(fs) != 1 || fs[0].Jobs.Hidden != 190 || len(fs[0].Jobs.Groups) != 5 || fs[0].Severity != model.SeverityWarning {
		t.Fatalf("findings %+v", fs)
	}
	// A Job that finishes moves from active to the policy.
	jc.add(jobSpec{ns: "ci", name: "live-0", finished: now.Add(-48 * time.Hour)})
	_, dels := jc.Drain()
	if !slices.Contains(dels, jobID("ci", "live-0")) {
		t.Fatalf("finished old job should be hidden: %v", dels)
	}
}

func TestJobPolicyPodsOfHiddenJobs(t *testing.T) {
	jc := newJobCache(t, JobPolicy{History: 1})
	now := jc.now
	jc.add(jobSpec{ns: "apps", name: "migrate-1", finished: now.Add(-2 * time.Hour)})
	jc.add(jobSpec{ns: "apps", name: "migrate-2", finished: now.Add(-time.Hour)})
	pod := podObj("migrate-1-abcde", "")
	owned(pod, "batch/v1", "Job", "migrate-1")
	pod.Object["status"] = map[string]any{"phase": "Succeeded"}
	jc.pods.AddFunc(pod)
	snap := byID(jc.Snapshot())
	if _, ok := snap[jobID("apps", "migrate-1")]; ok {
		t.Fatal("older job should be hidden")
	}
	p, ok := snap["/Pod/apps/migrate-1-abcde"]
	if !ok || p.Owner == nil || p.Owner.ID() != jobID("apps", "migrate-1") || p.Status != model.StatusCompleted {
		t.Fatalf("pod of hidden job %+v", p)
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 15078: "15,078", 1234567: "1,234,567", -4200: "-4,200"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestJobPolicyThroughInformers runs the policy on trimmed informer objects.
func TestJobPolicyThroughInformers(t *testing.T) {
	now := time.Now()
	var objs []runtime.Object
	for i := range 12 {
		j := jobObj(jobSpec{ns: "prefect", name: fmt.Sprintf("word-%c", 'a'+i), labels: prefect("etl"), finished: now.Add(-time.Duration(i+1) * time.Hour)})
		j.SetAnnotations(map[string]string{"prefect.io/secret-ish": "x"})
		objs = append(objs, j)
	}
	c, _ := startCache(t, testServed, nil, objs...)
	ids := jobIDs(c.Snapshot())
	if len(ids) != 5 || !slices.Contains(ids, jobID("prefect", "word-a")) {
		t.Fatalf("surfaced %v", ids)
	}
	fs, _ := c.Findings()
	if len(fs) != 1 || fs[0].Jobs.Hidden != 7 || fs[0].Jobs.Groups[0].Label != "prefect deployment etl" {
		t.Fatalf("findings %+v", fs)
	}
}

func TestHiddenJobsOp(t *testing.T) {
	jc := newJobCache(t, JobPolicy{History: 1})
	for i := range 3 {
		jc.add(jobSpec{ns: "batch", name: fmt.Sprintf("x-%d", i), finished: jc.now.Add(-time.Duration(i+1) * time.Hour)})
	}
	f := newOps(t)
	f.h.Jobs = jc.Cache
	raw, perr := f.h.Handle(t.Context(), request(protocol.OpHiddenJobs, model.Ref{}, protocol.HiddenJobsArgs{Namespaces: []string{"batch"}}), nil)
	if perr != nil {
		t.Fatal(perr)
	}
	var res protocol.HiddenJobsResult
	_ = json.Unmarshal(raw, &res)
	if res.Total != 2 || len(res.Items) != 2 || res.Items[0].Name != "x-1" {
		t.Fatalf("hidden %+v", res)
	}
	bad := request(protocol.OpHiddenJobs, model.Ref{}, nil)
	bad.Identity = protocol.Identity{User: "system:admin"}
	if _, perr := f.h.Handle(t.Context(), bad, nil); perr == nil || perr.Code != 403 {
		t.Fatalf("system user: %v", perr)
	}
	f.h.Jobs = nil
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpHiddenJobs, model.Ref{}, nil), nil); perr == nil || perr.Code != 503 {
		t.Fatalf("no source: %v", perr)
	}
}
