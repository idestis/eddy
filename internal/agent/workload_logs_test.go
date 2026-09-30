package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// ---- workload → pods resolution through the informer cache ----------------

func podOf(name, kind, owner string, created time.Time, containers ...string) *unstructured.Unstructured {
	var cs []any
	for _, c := range containers {
		cs = append(cs, map[string]any{"name": c, "image": c + ":1"})
	}
	p := u("v1", "Pod", "apps", name, map[string]any{"containers": cs})
	p.SetCreationTimestamp(metav1.NewTime(created))
	p.Object["status"] = map[string]any{"phase": "Running"}
	if kind != "" {
		av := "apps/v1"
		if kind == flux.KindJob {
			av = "batch/v1"
		}
		owned(p, av, kind, owner)
	}
	return p
}

func TestCacheWorkloadPods(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rsOld := owned(u("apps/v1", "ReplicaSet", "apps", "web-old", nil), "apps/v1", "Deployment", "web")
	rsNew := owned(u("apps/v1", "ReplicaSet", "apps", "web-new", nil), "apps/v1", "Deployment", "web")
	rsOther := owned(u("apps/v1", "ReplicaSet", "apps", "api-1", nil), "apps/v1", "Deployment", "api")
	labelled := podOf("legacy-job-pod", "", "", t0.Add(9*time.Hour), "main")
	labelled.SetLabels(map[string]string{labelLegacyJobName: "migrate"})
	c, dyn := startCache(t, testServed, nil,
		rsOld, rsNew, rsOther,
		podOf("web-old-a", flux.KindReplicaSet, "web-old", t0, "app"),
		podOf("web-new-b", flux.KindReplicaSet, "web-new", t0.Add(2*time.Hour), "app", "sidecar"),
		podOf("web-new-c", flux.KindReplicaSet, "web-new", t0.Add(time.Hour), "app", "sidecar"),
		podOf("api-1-x", flux.KindReplicaSet, "api-1", t0, "api"),
		podOf("db-0", flux.KindStatefulSet, "db", t0, "postgres"),
		podOf("agent-n1", flux.KindDaemonSet, "agent", t0, "agent"),
		podOf("migrate-z9", flux.KindJob, "migrate", t0.Add(8*time.Hour), "main"),
		labelled,
		podOf("orphan", "", "", t0, "x"),
	)
	names := func(ps []PodInfo) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	ref := func(kind, name string) model.Ref {
		k, _ := flux.KindByName(kind)
		return model.Ref{Group: k.Group, Kind: kind, Namespace: "apps", Name: name}
	}
	tests := []struct {
		target model.Ref
		want   []string
	}{
		{ref(flux.KindDeployment, "web"), []string{"web-new-b", "web-new-c", "web-old-a"}}, // newest first
		{ref(flux.KindReplicaSet, "web-new"), []string{"web-new-b", "web-new-c"}},
		{ref(flux.KindStatefulSet, "db"), []string{"db-0"}},
		{ref(flux.KindDaemonSet, "agent"), []string{"agent-n1"}},
		{ref(flux.KindJob, "migrate"), []string{"legacy-job-pod", "migrate-z9"}},
		{ref(flux.KindDeployment, "missing"), nil},
		{ref(flux.KindService, "web"), nil},
	}
	var got []PodInfo
	waitFor(t, "deployment pods", func() bool {
		got = c.WorkloadPods(ref(flux.KindDeployment, "web"))
		return len(got) == 3
	})
	if !slices.Equal(got[0].Containers, []string{"app", "sidecar"}) || got[0].Status != model.StatusReconciling && got[0].Status != model.StatusReady {
		t.Fatalf("pod info %+v", got[0])
	}
	for _, tt := range tests {
		if got := names(c.WorkloadPods(tt.target)); !slices.Equal(got, tt.want) {
			t.Errorf("%s: pods %v, want %v", tt.target.ID(), got, tt.want)
		}
	}

	// WatchPods fires for pod changes in its namespace.
	ch, stop := c.WatchPods("apps")
	defer stop()
	podGVR, _ := testServed.GVR(flux.KindPod)
	if _, err := dyn.Resource(podGVR).Namespace("apps").Create(t.Context(), podOf("web-new-d", flux.KindReplicaSet, "web-new", t0.Add(3*time.Hour), "app"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no pod notification")
	}
	waitFor(t, "new pod", func() bool { return len(c.WorkloadPods(ref(flux.KindDeployment, "web"))) == 4 })
}

// ---- workload log streams with fake pods and fake log streams --------------

type fakePods struct {
	mu   sync.Mutex
	pods []PodInfo
	ch   chan struct{}
}

func newFakePods(pods ...PodInfo) *fakePods {
	return &fakePods{pods: pods, ch: make(chan struct{}, 1)}
}

func (f *fakePods) WorkloadPods(model.Ref) []PodInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pods)
}

func (f *fakePods) WatchPods(string) (<-chan struct{}, func()) { return f.ch, func() {} }

func (f *fakePods) set(pods ...PodInfo) {
	f.mu.Lock()
	f.pods = pods
	f.mu.Unlock()
	select {
	case f.ch <- struct{}{}:
	default:
	}
}

type fakeStream struct {
	w      *io.PipeWriter
	ctx    context.Context
	closed chan struct{}
}

// fakeLogs opens a pipe per pod/container; tests write lines into it.
type fakeLogs struct {
	mu        sync.Mutex
	streams   map[string]*fakeStream
	forbidden map[string]bool
	static    map[string]string // pod/container → whole log (non-follow)
	opts      map[string]*corev1.PodLogOptions
	opened    chan string
}

func newFakeLogs() *fakeLogs {
	return &fakeLogs{streams: map[string]*fakeStream{}, forbidden: map[string]bool{}, static: map[string]string{},
		opts: map[string]*corev1.PodLogOptions{}, opened: make(chan string, 100)}
}

type closeNotify struct {
	*io.PipeReader
	once sync.Once
	ch   chan struct{}
}

func (c *closeNotify) Close() error {
	c.once.Do(func() { close(c.ch) })
	return c.PipeReader.Close()
}

func (f *fakeLogs) open(ctx context.Context, _ kubernetes.Interface, _, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	key := pod + "/" + opts.Container
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opts[key] = opts
	if f.forbidden[pod] {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, pod, errors.New(`User "alice" cannot get resource "pods/log"`))
	}
	if s, ok := f.static[key]; ok {
		f.opened <- key
		return io.NopCloser(strings.NewReader(s)), nil
	}
	pr, pw := io.Pipe()
	st := &fakeStream{w: pw, ctx: ctx, closed: make(chan struct{})}
	f.streams[key] = st
	f.opened <- key
	return &closeNotify{PipeReader: pr, ch: st.closed}, nil
}

func (f *fakeLogs) stream(t *testing.T, key string) *fakeStream {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		s := f.streams[key]
		f.mu.Unlock()
		if s != nil {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stream %s was never opened", key)
	return nil
}

func (f *fakeLogs) write(t *testing.T, key string, lines ...string) {
	t.Helper()
	s := f.stream(t, key)
	for _, l := range lines {
		if _, err := io.WriteString(s.w, l+"\n"); err != nil {
			t.Fatalf("write %s: %v", key, err)
		}
	}
}

// collector gathers a workload stream's chunks.
type collector struct {
	mu      sync.Mutex
	entries []protocol.LogEntry
	pods    []*protocol.LogPods
}

func (c *collector) emit(ch protocol.LogChunk) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ch.Lines) > 0 {
		return errors.New("workload streams must not send lines")
	}
	c.entries = append(c.entries, ch.Entries...)
	if ch.Pods != nil {
		c.pods = append(c.pods, ch.Pods)
	}
	return nil
}

func (c *collector) wait(t *testing.T, what string, cond func(entries []protocol.LogEntry, pods []*protocol.LogPods) bool) {
	t.Helper()
	waitFor(t, what, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return cond(c.entries, c.pods)
	})
}

func hasEntry(es []protocol.LogEntry, pod, container, line string, marker protocol.LogMarker) bool {
	return slices.ContainsFunc(es, func(e protocol.LogEntry) bool {
		return e.Pod == pod && e.Container == container && e.Line == line && e.Marker == marker
	})
}

func podNames(p *protocol.LogPods) []string {
	var out []string
	for _, x := range p.Pods {
		out = append(out, x.Name)
	}
	return out
}

var webRef = model.Ref{Group: flux.GroupApps, Kind: flux.KindDeployment, Namespace: "apps", Name: "web"}

func workloadFixture(t *testing.T, pods *fakePods, logs *fakeLogs) *opsFixture {
	t.Helper()
	f := newOps(t, u("apps/v1", "Deployment", "apps", "web", nil))
	f.h.Pods = pods
	f.h.openLogs = logs.open
	f.h.Now = time.Now
	return f
}

func pod(name string, age time.Duration, containers ...string) PodInfo {
	return PodInfo{Name: name, Containers: containers, Status: model.StatusReady, CreatedAt: time.Now().Add(-age)}
}

func TestWorkloadLogsFollow(t *testing.T) {
	pods := newFakePods(pod("web-a", 2*time.Hour, "app", "sidecar"), pod("web-b", time.Hour, "app", "sidecar"), pod("web-d", 3*time.Hour, "app"))
	logs := newFakeLogs()
	logs.forbidden["web-d"] = true
	f := workloadFixture(t, pods, logs)
	col := &collector{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan *protocol.Error, 1)
	go func() {
		_, perr := f.h.Handle(ctx, request(protocol.OpLogs, webRef, protocol.LogsArgs{Follow: true, TailLines: 50000, SinceSeconds: 60}), col.emit)
		done <- perr
	}()

	col.wait(t, "initial pod set", func(_ []protocol.LogEntry, ps []*protocol.LogPods) bool {
		return len(ps) == 1 && slices.Equal(podNames(ps[0]), []string{"web-b", "web-a", "web-d"}) && ps[0].Total == 3 && ps[0].Limit == 20
	})
	logs.write(t, "web-a/app", "2026-09-30T10:00:00.5Z hello from a")
	logs.write(t, "web-b/sidecar", "no timestamp here")
	col.wait(t, "lines from two pods", func(es []protocol.LogEntry, _ []*protocol.LogPods) bool {
		return hasEntry(es, "web-a", "app", "hello from a", "") && hasEntry(es, "web-b", "sidecar", "no timestamp here", "")
	})
	col.mu.Lock()
	for _, e := range col.entries {
		if e.Line == "hello from a" && !e.TS.Equal(time.Date(2026, 9, 30, 10, 0, 0, 5e8, time.UTC)) {
			t.Errorf("timestamp %v", e.TS)
		}
	}
	col.mu.Unlock()
	col.wait(t, "forbidden pod skipped", func(es []protocol.LogEntry, _ []*protocol.LogPods) bool {
		return hasEntry(es, "web-d", "", "not allowed to read the logs of this pod", protocol.MarkerForbidden)
	})

	// Options: tail capped at 1000 per pod, timestamps on, since passed.
	logs.mu.Lock()
	o := logs.opts["web-a/app"]
	logs.mu.Unlock()
	if *o.TailLines != maxWorkloadTail || !o.Follow || !o.Timestamps || o.SinceSeconds == nil || *o.SinceSeconds != 60 {
		t.Fatalf("log options %+v", o)
	}

	// A new pod appears, web-a goes away.
	aApp := logs.stream(t, "web-a/app")
	pods.set(pod("web-c", 0, "app"), pod("web-b", time.Hour, "app", "sidecar"), pod("web-d", 3*time.Hour, "app"))
	logs.write(t, "web-c/app", "hello from c")
	col.wait(t, "new pod streamed, old pod ended", func(es []protocol.LogEntry, ps []*protocol.LogPods) bool {
		last := ps[len(ps)-1]
		return hasEntry(es, "web-c", "app", "hello from c", "") &&
			hasEntry(es, "web-a", "", "pod deleted", protocol.MarkerEnded) &&
			slices.Equal(podNames(last), []string{"web-c", "web-b", "web-d"})
	})
	select {
	case <-aApp.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("stream of the deleted pod was not closed")
	}
	col.mu.Lock()
	forbidden := 0
	for _, e := range col.entries {
		if e.Marker == protocol.MarkerForbidden {
			forbidden++
		}
	}
	col.mu.Unlock()
	if forbidden != 1 {
		t.Fatalf("forbidden marker sent %d times", forbidden)
	}

	// Cancelling the request closes every open stream and returns.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end on cancel")
	}
	for _, key := range []string{"web-b/app", "web-b/sidecar", "web-c/app"} {
		select {
		case <-logs.stream(t, key).closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s not closed after cancel", key)
		}
	}
}

func TestWorkloadLogsCapsAndFilters(t *testing.T) {
	t.Run("pod cap keeps the newest", func(t *testing.T) {
		pods := newFakePods(pod("p1", 3*time.Hour, "app"), pod("p2", 2*time.Hour, "app"), pod("p3", time.Hour, "app"))
		logs := newFakeLogs()
		for _, p := range []string{"p1", "p2", "p3"} {
			logs.static[p+"/app"] = "line of " + p + "\n"
		}
		f := workloadFixture(t, pods, logs)
		f.h.MaxLogPods = 2
		col := &collector{}
		if _, perr := f.h.Handle(t.Context(), request(protocol.OpLogs, webRef, nil), col.emit); perr != nil {
			t.Fatal(perr)
		}
		if len(col.pods) != 1 || col.pods[0].Total != 3 || col.pods[0].Limit != 2 || !slices.Equal(podNames(col.pods[0]), []string{"p3", "p2"}) {
			t.Fatalf("pods %+v", col.pods)
		}
		if hasEntry(col.entries, "p1", "app", "line of p1", "") || !hasEntry(col.entries, "p3", "app", "line of p3", "") {
			t.Fatalf("entries %+v", col.entries)
		}
		logs.mu.Lock()
		defer logs.mu.Unlock()
		if *logs.opts["p3/app"].TailLines != defaultWorkloadTail || logs.opts["p3/app"].Follow {
			t.Fatalf("defaults %+v", logs.opts["p3/app"])
		}
	})
	t.Run("pods, container and allContainers filters", func(t *testing.T) {
		pods := newFakePods(pod("p1", time.Hour, "app", "sidecar"), pod("p2", 2*time.Hour, "app", "sidecar"))
		logs := newFakeLogs()
		for _, k := range []string{"p1/app", "p1/sidecar", "p2/app", "p2/sidecar"} {
			logs.static[k] = k + "\n"
		}
		no := false
		for _, tc := range []struct {
			args protocol.LogsArgs
			want []string
		}{
			{protocol.LogsArgs{Pods: []string{"p2", "unknown"}}, []string{"p2/app", "p2/sidecar"}},
			{protocol.LogsArgs{Container: "sidecar"}, []string{"p1/sidecar", "p2/sidecar"}},
			{protocol.LogsArgs{AllContainers: &no}, []string{"p1/app", "p2/app"}},
		} {
			f := workloadFixture(t, pods, logs)
			col := &collector{}
			if _, perr := f.h.Handle(t.Context(), request(protocol.OpLogs, webRef, tc.args), col.emit); perr != nil {
				t.Fatal(perr)
			}
			var got []string
			for _, e := range col.entries {
				got = append(got, e.Line)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%+v: lines %v, want %v", tc.args, got, tc.want)
			}
		}
	})
	t.Run("rate limit drops with a marker", func(t *testing.T) {
		pods := newFakePods(pod("p1", time.Hour, "app"))
		logs := newFakeLogs()
		var b strings.Builder
		for i := range 500 {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		logs.static["p1/app"] = b.String()
		f := workloadFixture(t, pods, logs)
		f.h.LogLineRate = 10
		col := &collector{}
		if _, perr := f.h.Handle(t.Context(), request(protocol.OpLogs, webRef, protocol.LogsArgs{TailLines: 1000}), col.emit); perr != nil {
			t.Fatal(perr)
		}
		lines, dropped := 0, 0
		for _, e := range col.entries {
			switch e.Marker {
			case "":
				lines++
			case protocol.MarkerDropped:
				var n int
				if _, err := fmt.Sscanf(e.Line, "dropped %d lines", &n); err != nil {
					t.Fatalf("marker %q", e.Line)
				}
				dropped += n
			}
		}
		if lines < 20 || lines > 40 || lines+dropped != 500 {
			t.Fatalf("delivered %d, dropped %d", lines, dropped)
		}
	})
	t.Run("no pods ends at once", func(t *testing.T) {
		f := workloadFixture(t, newFakePods(), newFakeLogs())
		col := &collector{}
		if _, perr := f.h.Handle(t.Context(), request(protocol.OpLogs, webRef, nil), col.emit); perr != nil {
			t.Fatal(perr)
		}
		if len(col.pods) != 1 || col.pods[0].Total != 0 || col.pods[0].Pods == nil {
			t.Fatalf("pods %+v", col.pods)
		}
	})
}

func TestWorkloadLogsNeedsTheWorkload(t *testing.T) {
	f := workloadFixture(t, newFakePods(pod("p1", time.Hour, "app")), newFakeLogs())
	missing := webRef
	missing.Name = "missing"
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpLogs, missing, nil), (&collector{}).emit); perr == nil || perr.Code != 404 {
		t.Fatalf("missing workload: %v", perr)
	}
	f.h.Pods = nil
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpLogs, webRef, nil), (&collector{}).emit); perr == nil || perr.Code != 503 {
		t.Fatalf("no pod source: %v", perr)
	}
}

func TestSplitTimestamp(t *testing.T) {
	ts, line := splitTimestamp("2026-09-30T10:00:00.123456789Z GET /healthz 200")
	if line != "GET /healthz 200" || ts.Nanosecond() != 123456789 {
		t.Fatalf("%v %q", ts, line)
	}
	if ts, line := splitTimestamp("plain line"); !ts.IsZero() || line != "plain line" {
		t.Fatalf("%v %q", ts, line)
	}
	if ts, line := splitTimestamp("2026-09-30T10:00:00Z"); ts.IsZero() || line != "" {
		t.Fatalf("%v %q", ts, line)
	}
}
