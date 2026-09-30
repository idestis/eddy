package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// Workload log limits. A workload stream is one request (one slot of
// EDDY_MAX_LOG_STREAMS) that follows up to MaxLogPods pods; every pod ×
// container is its own API server log stream, at most
// workloadStreamsPerPod×MaxLogPods of them.
const (
	defaultWorkloadTail   = 100
	maxWorkloadTail       = 1000
	workloadStreamsPerPod = 3
	// workloadResync re-reads the pod set while following, in case a pod
	// notification was missed, and retries containers that were not
	// started yet.
	workloadResync = 5 * time.Second
	// workloadMinSync spaces pod-set re-reads triggered by pod changes.
	workloadMinSync = 250 * time.Millisecond
	// entryChunk caps a chunk of entries; the byte cap leaves room for JSON
	// escaping within protocol.MaxFrameBytes.
	entryChunkEntries = 500
	entryChunkBytes   = 128 << 10
)

// logOpener opens one container's log stream. Tests replace it.
type logOpener func(ctx context.Context, kube kubernetes.Interface, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)

func openKubeLogs(ctx context.Context, kube kubernetes.Interface, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	return kube.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
}

// isWorkloadKind reports whether logs of kind stream its pods.
func isWorkloadKind(kind string) bool {
	switch kind {
	case flux.KindDeployment, flux.KindStatefulSet, flux.KindDaemonSet, flux.KindReplicaSet, flux.KindJob:
		return true
	}
	return false
}

// workloadLogs streams the logs of every current pod of a workload. The
// user must be able to get the workload; each pod's logs are read as the
// user, and a pod they may not read is skipped with a MarkerForbidden entry.
func (h *Handler) workloadLogs(ctx context.Context, cl Clients, gvr schema.GroupVersionResource, t model.Ref, args protocol.LogsArgs, stream func(protocol.LogChunk) error) error {
	if h.Pods == nil {
		return &protocol.Error{Code: 503, Message: "agent: workload logs are not available"}
	}
	if _, err := cl.Dynamic.Resource(gvr).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("agent: get %s: %w", t.ID(), err)
	}
	tail := args.TailLines
	switch {
	case tail <= 0:
		tail = defaultWorkloadTail
	case tail > maxWorkloadTail:
		tail = maxWorkloadTail
	}
	open := h.openLogs
	if open == nil {
		open = openKubeLogs
	}
	ws := &workloadStream{
		h: h, kube: cl.Kube, open: open, target: t, args: args, tail: tail,
		limit:     orDefault(h.MaxLogPods, config.DefaultMaxLogPods),
		rate:      float64(orDefault(h.LogLineRate, config.DefaultLogLineRate)),
		emit:      stream,
		out:       make(chan protocol.LogEntry, logChunkLines),
		done:      make(chan streamDone, 16),
		pods:      map[string]*podStream{},
		forbidden: map[string]bool{},
		all:       args.AllContainers == nil || *args.AllContainers,
	}
	if len(args.Pods) > 0 {
		ws.only = map[string]bool{}
		for _, p := range args.Pods {
			ws.only[p] = true
		}
	}
	return ws.run(ctx)
}

type workloadStream struct {
	h      *Handler
	kube   kubernetes.Interface
	open   logOpener
	target model.Ref
	args   protocol.LogsArgs
	tail   int64
	limit  int
	rate   float64
	all    bool
	only   map[string]bool
	emit   func(protocol.LogChunk) error

	out  chan protocol.LogEntry
	done chan streamDone
	wg   sync.WaitGroup

	// Owned by the run loop.
	pods      map[string]*podStream
	forbidden map[string]bool
	running   int
	lastSet   string
	batch     []protocol.LogEntry
	size      int
	podsMsg   *protocol.LogPods
	tokens    float64
	refilled  time.Time
	dropped   int
}

type podStream struct {
	cancel     context.CancelFunc
	ctx        context.Context
	containers map[string]containerState
	// stopped is set once the pod's streams were cancelled; their late
	// completions are ignored.
	stopped bool
}

type containerState int

const (
	containerRunning containerState = iota
	containerDone
	containerRetry // not started yet (a 400 from the API server); retried on resync
)

type streamDone struct {
	ps             *podStream
	pod, container string
	err            error
}

func (s *workloadStream) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		s.wg.Wait()
	}()
	var changes <-chan struct{}
	if s.args.Follow {
		ch, stop := s.h.Pods.WatchPods(s.target.Namespace)
		defer stop()
		changes = ch
	}
	s.tokens, s.refilled = 2*s.rate, s.h.now()
	s.sync(ctx)
	if err := s.flush(); err != nil {
		return err
	}
	flushT := time.NewTicker(logFlushEvery)
	defer flushT.Stop()
	var resync <-chan time.Time
	if s.args.Follow {
		t := time.NewTicker(workloadResync)
		defer t.Stop()
		resync = t.C
	}
	lastSync, syncPending := time.Now(), false
	for {
		if !s.args.Follow && s.running == 0 {
			// Entries already read are queued in out; the readers are done.
			for {
				select {
				case e := <-s.out:
					if err := s.add(e); err != nil {
						return err
					}
					continue
				default:
				}
				break
			}
			return s.flush()
		}
		select {
		case e := <-s.out:
			if err := s.add(e); err != nil {
				return err
			}
		case d := <-s.done:
			s.finished(d)
		case <-changes:
			if time.Since(lastSync) >= workloadMinSync {
				s.sync(ctx)
				lastSync = time.Now()
			} else {
				syncPending = true
			}
		case <-resync:
			s.sync(ctx)
			lastSync, syncPending = time.Now(), false
		case <-flushT.C:
			if syncPending {
				s.sync(ctx)
				lastSync, syncPending = time.Now(), false
			}
			if err := s.flush(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// sync compares the workload's current pods with the streamed ones: it ends
// the streams of pods that are gone, starts new pods, and retries
// containers that had not started. It queues a pod-set update on change.
func (s *workloadStream) sync(ctx context.Context) {
	all := s.h.Pods.WorkloadPods(s.target)
	var pods []PodInfo
	for _, p := range all {
		if s.only == nil || s.only[p.Name] {
			pods = append(pods, p)
		}
	}
	slices.SortStableFunc(pods, func(a, b PodInfo) int { return b.CreatedAt.Compare(a.CreatedAt) })
	total := len(pods)
	if len(pods) > s.limit {
		pods = pods[:s.limit]
	}
	want := make(map[string]PodInfo, len(pods))
	for _, p := range pods {
		want[p.Name] = p
	}
	exists := make(map[string]bool, len(all))
	for _, p := range all {
		exists[p.Name] = true
	}
	for name, ps := range s.pods {
		if _, ok := want[name]; ok {
			continue
		}
		forbidden := s.forbidden[name]
		s.stopPod(ps)
		delete(s.pods, name)
		if forbidden {
			continue
		}
		why := "pod deleted"
		if exists[name] {
			why = fmt.Sprintf("pod is no longer among the newest %d pods", s.limit)
		}
		s.marker(protocol.LogEntry{Pod: name, Marker: protocol.MarkerEnded, Line: why})
	}
	for _, p := range pods {
		if s.forbidden[p.Name] {
			continue
		}
		ps := s.pods[p.Name]
		if ps == nil {
			pctx, cancel := context.WithCancel(ctx)
			ps = &podStream{ctx: pctx, cancel: cancel, containers: map[string]containerState{}}
			s.pods[p.Name] = ps
		}
		for _, ct := range s.containersOf(p) {
			st, seen := ps.containers[ct]
			if seen && st != containerRetry {
				continue
			}
			if s.running >= workloadStreamsPerPod*s.limit {
				if !seen {
					ps.containers[ct] = containerDone
					s.marker(protocol.LogEntry{Pod: p.Name, Container: ct, Marker: protocol.MarkerError,
						Line: fmt.Sprintf("not streamed: at most %d container streams per request; pick a container", workloadStreamsPerPod*s.limit)})
				}
				continue
			}
			ps.containers[ct] = containerRunning
			s.running++
			s.wg.Add(1)
			go s.read(ps, p.Name, ct)
		}
	}

	set := protocol.LogPods{Pods: make([]protocol.LogPod, 0, len(pods)), Total: total, Limit: s.limit}
	var fp strings.Builder
	fmt.Fprintf(&fp, "%d|", total)
	for _, p := range pods {
		set.Pods = append(set.Pods, protocol.LogPod{Name: p.Name, Containers: slices.Clone(p.Containers), Status: p.Status, CreatedAt: p.CreatedAt})
		fmt.Fprintf(&fp, "%s:%s:%s;", p.Name, p.Status, strings.Join(p.Containers, ","))
	}
	if fp.String() != s.lastSet {
		s.lastSet = fp.String()
		s.podsMsg = &set
	}
}

func (s *workloadStream) containersOf(p PodInfo) []string {
	if c := s.args.Container; c != "" {
		if slices.Contains(p.Containers, c) {
			return []string{c}
		}
		return nil
	}
	if s.all || len(p.Containers) == 0 {
		return p.Containers
	}
	return p.Containers[:1]
}

// stopPod cancels a pod's streams and stops counting them as running.
func (s *workloadStream) stopPod(ps *podStream) {
	for ct, st := range ps.containers {
		if st == containerRunning {
			s.running--
		}
		ps.containers[ct] = containerDone
	}
	ps.stopped = true
	ps.cancel()
}

// read streams one container's log into s.out.
func (s *workloadStream) read(ps *podStream, pod, container string) {
	defer s.wg.Done()
	ctx := ps.ctx
	finish := func(err error) {
		select {
		case s.done <- streamDone{ps: ps, pod: pod, container: container, err: err}:
		case <-ctx.Done():
		}
	}
	tail := s.tail
	opts := &corev1.PodLogOptions{Container: container, Follow: s.args.Follow, TailLines: &tail, Timestamps: true}
	if s.args.SinceSeconds > 0 {
		since := s.args.SinceSeconds
		opts.SinceSeconds = &since
	}
	rc, err := s.open(ctx, s.kube, s.target.Namespace, pod, opts)
	if err != nil {
		finish(err)
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = rc.Close() })
	defer func() {
		stop()
		_ = rc.Close()
	}()
	br := bufio.NewReaderSize(rc, 64<<10)
	for {
		line, err := readLine(br)
		if err == nil || (errors.Is(err, io.EOF) && line != "") {
			e := protocol.LogEntry{Pod: pod, Container: container}
			e.TS, e.Line = splitTimestamp(line)
			select {
			case s.out <- e:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				err = nil
			}
			finish(err)
			return
		}
	}
}

// finished records the end of one container stream.
func (s *workloadStream) finished(d streamDone) {
	ps := d.ps
	if ps.stopped || ps.containers[d.container] != containerRunning {
		return // stopPod already accounted for it
	}
	s.running--
	ps.containers[d.container] = containerDone
	switch {
	case d.err == nil:
	case apierrors.IsForbidden(d.err) || apierrors.IsUnauthorized(d.err):
		if !s.forbidden[d.pod] {
			s.forbidden[d.pod] = true
			s.marker(protocol.LogEntry{Pod: d.pod, Marker: protocol.MarkerForbidden, Line: "not allowed to read the logs of this pod"})
		}
		s.stopPod(ps)
	case apierrors.IsBadRequest(d.err) && s.args.Follow:
		// Typically "container is waiting to start": try again on resync.
		ps.containers[d.container] = containerRetry
	default:
		s.marker(protocol.LogEntry{Pod: d.pod, Container: d.container, Marker: protocol.MarkerError, Line: flux.OneLine(toProtocolError(d.err).Message, 300)})
	}
}

// add queues one log line, subject to the stream's rate limit: a token
// bucket of rate lines per second holding at most two seconds' worth.
func (s *workloadStream) add(e protocol.LogEntry) error {
	now := s.h.now()
	if el := now.Sub(s.refilled).Seconds(); el > 0 {
		s.tokens = min(2*s.rate, s.tokens+el*s.rate)
		s.refilled = now
	}
	if s.tokens < 1 {
		s.dropped++
		return nil
	}
	s.tokens--
	return s.queue(e)
}

// marker queues an entry that is not a log line; it bypasses the rate limit.
func (s *workloadStream) marker(e protocol.LogEntry) { _ = s.queue(e) }

func (s *workloadStream) queue(e protocol.LogEntry) error {
	s.batch = append(s.batch, e)
	s.size += len(e.Line) + len(e.Pod) + len(e.Container) + 64
	if len(s.batch) >= entryChunkEntries || s.size >= entryChunkBytes {
		return s.flush()
	}
	return nil
}

func (s *workloadStream) flush() error {
	if s.dropped > 0 {
		s.batch = append(s.batch, protocol.LogEntry{Marker: protocol.MarkerDropped,
			Line: fmt.Sprintf("dropped %d lines: more than %d lines per second", s.dropped, int(s.rate))})
		s.dropped = 0
	}
	if len(s.batch) == 0 && s.podsMsg == nil {
		return nil
	}
	chunk := protocol.LogChunk{Entries: s.batch, Pods: s.podsMsg}
	s.batch, s.size, s.podsMsg = nil, 0, nil
	return s.emit(chunk)
}

// splitTimestamp separates the RFC 3339 timestamp the kubelet prefixes to
// each line (PodLogOptions.Timestamps) from the line itself.
func splitTimestamp(line string) (time.Time, string) {
	ts, rest, ok := strings.Cut(line, " ")
	if !ok {
		ts, rest = line, ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, line
	}
	return t.UTC(), rest
}
