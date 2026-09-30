package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

func testSession(b *bus) *agentSession {
	var seq atomic.Uint64
	return newSession("dev", protocol.Hello{}, [32]byte{}, nil, sessionDeps{bus: b, metrics: newMetrics(), rvSeq: &seq, log: quietLog()})
}

func frame(t *testing.T, typ protocol.FrameType, payload any) protocol.Frame {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Frame{Type: typ, Payload: b}
}

func ids(rs []model.Resource) map[string]bool {
	m := map[string]bool{}
	for _, r := range rs {
		m[r.ID] = true
	}
	return m
}

func TestSnapshotChunksAndDeltas(t *testing.T) {
	b := newBus()
	sub := b.subscribe()
	s := testSession(b)
	a1, a2, a3 := res("Kustomization", "team-a", "a1", model.StatusReady), res("HelmRelease", "team-a", "a2", model.StatusFailed), res("Deployment", "team-b", "a3", model.StatusReady)
	secret := model.Resource{Ref: model.Ref{Kind: "Secret", Namespace: "team-a", Name: "creds"}}
	replicaSet := res("Deployment", "team-a", "rs", model.StatusReady)
	replicaSet.Kind = "ReplicaSet" // watched by agents but never surfaced
	forged := a1
	forged.Name, forged.ID = "forged", "some/other/id"

	steps := []struct {
		name  string
		frame protocol.Frame
		want  []string
	}{
		{"snapshot replaces the view", frame(t, protocol.TypeSnapshot, protocol.Snapshot{Resources: []model.Resource{a1, secret, replicaSet}}), []string{a1.ID}},
		{"snapshot remainder arrives as upsert-only deltas", frame(t, protocol.TypeDelta, protocol.Delta{Upserts: []model.Resource{a2, a3}}), []string{a1.ID, a2.ID, a3.ID}},
		{"ids are recomputed", frame(t, protocol.TypeDelta, protocol.Delta{Upserts: []model.Resource{forged}}), []string{a1.ID, a2.ID, a3.ID, forged.Ref.ID()}},
		{"deletes remove", frame(t, protocol.TypeDelta, protocol.Delta{Deletes: []string{a2.ID, "unknown/X/y/z"}}), []string{a1.ID, a3.ID, forged.Ref.ID()}},
		{"a new snapshot starts over", frame(t, protocol.TypeSnapshot, protocol.Snapshot{Resources: []model.Resource{a3}}), []string{a3.ID}},
	}
	for _, st := range steps {
		if err := s.handle(st.frame); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		view, _ := s.view()
		got := ids(view)
		if len(got) != len(st.want) {
			t.Fatalf("%s: view %v, want %v", st.name, got, st.want)
		}
		for _, id := range st.want {
			if !got[id] {
				t.Fatalf("%s: view %v lacks %s", st.name, got, id)
			}
		}
	}

	var kinds []eventKind
	var deleted []string
	for len(sub.ch) > 0 {
		e := <-sub.ch
		kinds = append(kinds, e.kind)
		deleted = append(deleted, e.deletes...)
	}
	if kinds[0] != evResync {
		t.Fatalf("first event %v, want resync", kinds[0])
	}
	if len(deleted) != 1 || deleted[0] != a2.ID {
		t.Fatalf("published deletes %v, want only the id that existed", deleted)
	}
	if err := s.handle(protocol.Frame{Type: protocol.TypeDelta, Payload: json.RawMessage(`{"upserts":"nope"}`)}); err == nil {
		t.Fatal("malformed delta accepted")
	}
}

func TestTupleCountsCachedPerVersion(t *testing.T) {
	s := testSession(newBus())
	_ = s.handle(frame(t, protocol.TypeSnapshot, protocol.Snapshot{Resources: []model.Resource{
		res("Kustomization", "a", "1", model.StatusReady), res("Kustomization", "a", "2", model.StatusFailed),
	}}))
	c := s.tupleCounts()
	tk := accessTuple{Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations", Namespace: "a"}
	if c[tk][model.StatusReady] != 1 || c[tk][model.StatusFailed] != 1 {
		t.Fatalf("counts %v", c)
	}
	_ = s.handle(frame(t, protocol.TypeDelta, protocol.Delta{Deletes: []string{res("Kustomization", "a", "2", "").ID}}))
	if s.tupleCounts()[tk][model.StatusFailed] != 0 {
		t.Fatal("counts not recomputed after a delta")
	}
}

func TestRequestResponseCorrelation(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, nil)
	var mu sync.Mutex
	pending := map[string]protocol.Request{}
	// Answer requests in reverse order once three are in flight.
	a.onRequest(func(f protocol.Frame, req protocol.Request) bool {
		if req.Op != protocol.OpEvents {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		pending[f.ID] = req
		if len(pending) == 3 {
			var order []string
			for id := range pending {
				order = append(order, id)
			}
			for i := len(order) - 1; i >= 0; i-- {
				r := pending[order[i]]
				a.reply(order[i], protocol.EventsResult{Events: []model.Event{{Reason: r.Target.Name}}}, nil)
			}
		}
		return true
	})
	s := e.hub.agents.get("dev")
	var wg sync.WaitGroup
	for _, name := range []string{"x", "y", "z"} {
		wg.Go(func() {
			raw, err := s.do(context.Background(), protocol.Request{Op: protocol.OpEvents, Target: model.Ref{Kind: "Pod", Namespace: "n", Name: name}})
			if err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			var r protocol.EventsResult
			_ = json.Unmarshal(raw, &r)
			if len(r.Events) != 1 || r.Events[0].Reason != name {
				t.Errorf("request %s got the answer %v", name, r.Events)
			}
		})
	}
	wg.Wait()
}

func TestRequestErrorsAndTimeout(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, nil)
	a.onRequest(func(f protocol.Frame, req protocol.Request) bool {
		switch req.Target.Name {
		case "forbidden":
			a.reply(f.ID, nil, &protocol.Error{Code: 403, Message: "no"})
		case "silent":
			// never answer
		default:
			return false
		}
		return true
	})
	s := e.hub.agents.get("dev")
	s.timeout = 200 * time.Millisecond
	_, err := s.do(context.Background(), protocol.Request{Op: protocol.OpEvents, Target: model.Ref{Name: "forbidden"}})
	if !errors.Is(err, fleet.ErrForbidden) {
		t.Fatalf("403 from agent: %v, want fleet.ErrForbidden", err)
	}
	_, err = s.do(context.Background(), protocol.Request{Op: protocol.OpEvents, Target: model.Ref{Name: "silent"}})
	if !errors.Is(err, errUnavailable) {
		t.Fatalf("timeout: %v, want errUnavailable", err)
	}
	select {
	case <-a.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("no cancel sent after a timeout")
	}
}

func TestStreamAndCancel(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, nil)
	s := e.hub.agents.get("dev")

	var lines []string
	collect := func(c protocol.LogChunk) error { lines = append(lines, c.Lines...); return nil }
	args, _ := json.Marshal(protocol.LogsArgs{TailLines: 10})
	if err := s.stream(context.Background(), protocol.Request{Op: protocol.OpLogs, Args: args}, collect); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || lines[2] != "line 3" {
		t.Fatalf("lines %v", lines)
	}

	// A stream ending with an error.
	a.onRequest(func(f protocol.Frame, req protocol.Request) bool {
		if req.Op != protocol.OpLogs {
			return false
		}
		var la protocol.LogsArgs
		_ = json.Unmarshal(req.Args, &la)
		if la.Container == "broken" {
			a.sendFrame(protocol.TypeStreamEnd, f.ID, protocol.Response{Error: &protocol.Error{Code: 403, Message: "pods/log forbidden"}})
			return true
		}
		return false
	})
	args, _ = json.Marshal(protocol.LogsArgs{Container: "broken"})
	if err := s.stream(context.Background(), protocol.Request{Op: protocol.OpLogs, Args: args}, collect); !errors.Is(err, fleet.ErrForbidden) {
		t.Fatalf("stream error %v, want forbidden", err)
	}

	// A follow stream stops when the client goes away, and the agent is told.
	ctx, cancel := context.WithCancel(context.Background())
	args, _ = json.Marshal(protocol.LogsArgs{Follow: true})
	got := make(chan struct{}, 4)
	done := make(chan error, 1)
	go func() {
		done <- s.stream(ctx, protocol.Request{Op: protocol.OpLogs, Args: args}, func(protocol.LogChunk) error {
			got <- struct{}{}
			return nil
		})
	}()
	<-got
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow stream ended with %v", err)
	}
	select {
	case <-a.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("agent never received cancel")
	}
}

func TestDisconnectFailsPending(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, nil)
	a.onRequest(func(protocol.Frame, protocol.Request) bool { return true }) // never answer
	s := e.hub.agents.get("dev")
	done := make(chan error, 1)
	go func() {
		_, err := s.do(context.Background(), protocol.Request{Op: protocol.OpEvents})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	a.close()
	select {
	case err := <-done:
		if !errors.Is(err, fleet.ErrDisconnected) {
			t.Fatalf("pending request failed with %v, want ErrDisconnected", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending request not failed on disconnect")
	}
	waitFor(t, func() bool { return e.hub.agents.get("dev") == nil })
}
