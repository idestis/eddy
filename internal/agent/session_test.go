package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

type fakeSource struct {
	mu      sync.Mutex
	all     []model.Resource
	upserts []model.Resource
	deletes []string
}

func (f *fakeSource) Snapshot() []model.Resource {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts, f.deletes = nil, nil
	return f.all
}

func (f *fakeSource) Drain() ([]model.Resource, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, d := f.upserts, f.deletes
	f.upserts, f.deletes = nil, nil
	return u, d
}

func (f *fakeSource) change(up model.Resource, del string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, up)
	f.deletes = append(f.deletes, del)
}

// fakeHandler answers yaml with the op name and streams three log chunks,
// or blocks a followed log until cancelled.
type fakeHandler struct{}

func (fakeHandler) Handle(ctx context.Context, req protocol.Request, stream func(protocol.LogChunk) error) (json.RawMessage, *protocol.Error) {
	switch req.Op {
	case protocol.OpYAML:
		b, _ := json.Marshal(protocol.YAMLResult{YAML: "kind: " + req.Target.Kind + "\nuser: " + req.Identity.User})
		return b, nil
	case protocol.OpLogs:
		var args protocol.LogsArgs
		_ = json.Unmarshal(req.Args, &args)
		for i := range 3 {
			if err := stream(protocol.LogChunk{Lines: []string{fmt.Sprintf("line %d", i)}}); err != nil {
				return nil, &protocol.Error{Code: 500, Message: err.Error()}
			}
		}
		if args.Follow {
			<-ctx.Done()
		}
		return nil, nil
	}
	return nil, &protocol.Error{Code: 400, Message: "unsupported"}
}

type hubConn struct {
	t    *testing.T
	conn *websocket.Conn
}

func (h hubConn) read() protocol.Frame {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := h.conn.Read(ctx)
		if err != nil {
			h.t.Fatalf("hub read: %v", err)
		}
		var f protocol.Frame
		if err := json.Unmarshal(data, &f); err != nil {
			h.t.Fatal(err)
		}
		if f.Type != protocol.TypePing {
			return f
		}
	}
}

func (h hubConn) send(typ protocol.FrameType, id string, payload any) {
	h.t.Helper()
	f := protocol.Frame{Type: typ, ID: id}
	if payload != nil {
		f.Payload, _ = json.Marshal(payload)
	}
	b, _ := json.Marshal(f)
	if err := h.conn.Write(h.t.Context(), websocket.MessageText, b); err != nil {
		h.t.Fatal(err)
	}
}

func resource(i int, pad int) model.Resource {
	ref := model.Ref{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: fmt.Sprintf("web-%05d", i)}
	return model.Resource{Ref: ref, ID: ref.ID(), Status: model.StatusReady, Message: strings.Repeat("m", pad)}
}

func TestSessionRoundTrip(t *testing.T) {
	type accepted struct {
		conn   *websocket.Conn
		auth   string
		query  string
		closed chan struct{}
	}
	conns := make(chan accepted, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(protocol.MaxFrameBytes)
		a := accepted{conn: c, auth: r.Header.Get("Authorization"), query: r.URL.RawQuery, closed: make(chan struct{})}
		conns <- a
		<-a.closed
	}))
	defer srv.Close()

	// Enough resources to force a chunked snapshot.
	src := &fakeSource{}
	for i := range 3000 {
		src.all = append(src.all, resource(i, 400))
	}
	s := &Session{
		URL:           "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/v1/connect",
		Cluster:       "prod-eu",
		Token:         "s3cret",
		Hello:         protocol.Hello{AgentVersion: "test", KubernetesVersion: "v1.34.0"},
		Source:        src,
		Handler:       fakeHandler{},
		Logger:        discardLogger(),
		DeltaInterval: 20 * time.Millisecond,
		PingInterval:  50 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	a := <-conns
	defer close(a.closed)
	if a.auth != "Bearer s3cret" || a.query != "cluster=prod-eu" {
		t.Fatalf("auth %q query %q", a.auth, a.query)
	}
	hub := hubConn{t: t, conn: a.conn}

	f := hub.read()
	var hello protocol.Hello
	_ = json.Unmarshal(f.Payload, &hello)
	if f.Type != protocol.TypeHello || hello.Protocol != protocol.Version || hello.Cluster != "prod-eu" || hello.AgentVersion != "test" {
		t.Fatalf("hello %+v %+v", f, hello)
	}
	if hello.Instance == "" || hello.Instance != processInstance || hello.Seq != 1 {
		t.Fatalf("hello instance %q seq %d, want the process instance and seq 1", hello.Instance, hello.Seq)
	}

	f = hub.read()
	if f.Type != protocol.TypeSnapshot {
		t.Fatalf("want snapshot, got %s", f.Type)
	}
	var snap protocol.Snapshot
	_ = json.Unmarshal(f.Payload, &snap)
	got := len(snap.Resources)
	if got == 0 || got == len(src.all) {
		t.Fatalf("snapshot not chunked: %d resources", got)
	}
	for got < len(src.all) {
		f = hub.read()
		var d protocol.Delta
		_ = json.Unmarshal(f.Payload, &d)
		if f.Type != protocol.TypeDelta || len(d.Deletes) != 0 {
			t.Fatalf("want delta upserts, got %s %+v", f.Type, d.Deletes)
		}
		got += len(d.Upserts)
	}
	if got != len(src.all) {
		t.Fatalf("received %d resources, want %d", got, len(src.all))
	}
	waitFor(t, "connected", s.Connected)

	src.change(resource(1, 0), "apps/Deployment/apps/gone")
	f = hub.read()
	var d protocol.Delta
	_ = json.Unmarshal(f.Payload, &d)
	if f.Type != protocol.TypeDelta || len(d.Upserts) != 1 || d.Upserts[0].Name != "web-00001" {
		t.Fatalf("delta upserts %+v", d)
	}
	f = hub.read()
	d = protocol.Delta{}
	_ = json.Unmarshal(f.Payload, &d)
	if f.Type != protocol.TypeDelta || len(d.Deletes) != 1 || d.Deletes[0] != "apps/Deployment/apps/gone" {
		t.Fatalf("delta deletes %+v", d)
	}

	// Request / response.
	target := model.Ref{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web"}
	hub.send(protocol.TypeRequest, "r1", protocol.Request{Op: protocol.OpYAML, Identity: alice, Target: target})
	f = hub.read()
	var resp protocol.Response
	_ = json.Unmarshal(f.Payload, &resp)
	var y protocol.YAMLResult
	_ = json.Unmarshal(resp.Result, &y)
	if f.Type != protocol.TypeResponse || f.ID != "r1" || resp.Error != nil || !strings.Contains(y.YAML, "user: alice@example.com") {
		t.Fatalf("response %+v %+v", f, resp)
	}

	// Followed logs stream until cancelled.
	args, _ := json.Marshal(protocol.LogsArgs{Follow: true})
	hub.send(protocol.TypeRequest, "l1", protocol.Request{Op: protocol.OpLogs, Identity: alice, Target: model.Ref{Kind: "Pod", Namespace: "apps", Name: "p"}, Args: args})
	for i := range 3 {
		f = hub.read()
		var chunk protocol.LogChunk
		_ = json.Unmarshal(f.Payload, &chunk)
		if f.Type != protocol.TypeStream || f.ID != "l1" || chunk.Lines[0] != fmt.Sprintf("line %d", i) {
			t.Fatalf("stream frame %+v", f)
		}
	}
	hub.send(protocol.TypeCancel, "l1", nil)
	f = hub.read()
	resp = protocol.Response{}
	_ = json.Unmarshal(f.Payload, &resp)
	if f.Type != protocol.TypeStreamEnd || f.ID != "l1" || resp.Error != nil {
		t.Fatalf("stream end %+v %+v", f, resp)
	}

	// An unknown op still gets exactly one response.
	hub.send(protocol.TypeRequest, "r2", protocol.Request{Op: "nope", Identity: alice})
	f = hub.read()
	resp = protocol.Response{}
	_ = json.Unmarshal(f.Payload, &resp)
	if f.Type != protocol.TypeResponse || f.ID != "r2" || resp.Error == nil || resp.Error.Code != 400 {
		t.Fatalf("error response %+v", resp)
	}

	// Dropping the connection makes the agent reconnect and resend the snapshot.
	a.conn.CloseNow()
	b := <-conns
	defer close(b.closed)
	hub2 := hubConn{t: t, conn: b.conn}
	f = hub2.read()
	var hello2 protocol.Hello
	_ = json.Unmarshal(f.Payload, &hello2)
	if f.Type != protocol.TypeHello || hello2.Instance != hello.Instance || hello2.Seq != 2 {
		t.Fatalf("after reconnect want hello from the same instance with seq 2, got %s %+v", f.Type, hello2)
	}
	if f := hub2.read(); f.Type != protocol.TypeSnapshot {
		t.Fatalf("after reconnect want snapshot, got %s", f.Type)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop")
	}
}

func TestSplitAndBackoff(t *testing.T) {
	rs := []model.Resource{resource(0, 100), resource(1, 100), resource(2, 5000), resource(3, 100)}
	one, _ := json.Marshal(rs[0])
	chunks, skipped := splitResources(rs, 2*(len(one)+1))
	if skipped != 1 || len(chunks) != 2 || len(chunks[0]) != 2 || len(chunks[1]) != 1 {
		t.Fatalf("chunks %d skipped %d", len(chunks), skipped)
	}
	var groups [][]string
	for g := range splitIDs([]string{"aaaa", "bbbb", "cccc"}, 25) {
		groups = append(groups, g)
	}
	if len(groups) != 2 || len(groups[0]) != 2 {
		t.Fatalf("id groups %v", groups)
	}
	for attempt := range 40 {
		d := backoff(attempt)
		if d <= 0 || d > backoffMax {
			t.Fatalf("backoff(%d) = %s", attempt, d)
		}
	}
	if backoff(0) > backoffBase {
		t.Fatal("first backoff too long")
	}
}

// TestLogStreamCap checks that log streams beyond MaxLogStreams are refused
// with a 503 StreamEnd while other requests still run.
func TestLogStreamCap(t *testing.T) {
	conns := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conns <- c
		<-release
	}))
	defer srv.Close()
	defer close(release)
	s := &Session{
		URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Cluster: "c", Token: "t",
		Source: &fakeSource{}, Handler: fakeHandler{}, Logger: discardLogger(),
		PingInterval: time.Hour, MaxLogStreams: 1,
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	hub := hubConn{t: t, conn: <-conns}
	hub.read() // hello
	hub.read() // snapshot
	pod := model.Ref{Kind: "Pod", Namespace: "apps", Name: "p"}
	args, _ := json.Marshal(protocol.LogsArgs{Follow: true})
	hub.send(protocol.TypeRequest, "l1", protocol.Request{Op: protocol.OpLogs, Identity: alice, Target: pod, Args: args})
	for range 3 {
		if f := hub.read(); f.Type != protocol.TypeStream || f.ID != "l1" {
			t.Fatalf("first stream: %+v", f)
		}
	}
	hub.send(protocol.TypeRequest, "l2", protocol.Request{Op: protocol.OpLogs, Identity: alice, Target: pod, Args: args})
	f := hub.read()
	var resp protocol.Response
	_ = json.Unmarshal(f.Payload, &resp)
	if f.Type != protocol.TypeStreamEnd || f.ID != "l2" || resp.Error == nil || resp.Error.Code != 503 {
		t.Fatalf("second stream: %+v %+v, want a 503 stream end", f, resp)
	}
	hub.send(protocol.TypeRequest, "y1", protocol.Request{Op: protocol.OpYAML, Identity: alice, Target: pod})
	if f := hub.read(); f.Type != protocol.TypeResponse || f.ID != "y1" {
		t.Fatalf("yaml while a stream runs: %+v", f)
	}
	hub.send(protocol.TypeCancel, "l1", nil)
	if f := hub.read(); f.Type != protocol.TypeStreamEnd || f.ID != "l1" {
		t.Fatalf("first stream end: %+v", f)
	}
	// The slot is released just after the stream end is sent, so retry.
	for i := 0; ; i++ {
		id := fmt.Sprintf("l3-%d", i)
		hub.send(protocol.TypeRequest, id, protocol.Request{Op: protocol.OpLogs, Identity: alice, Target: pod, Args: args})
		f := hub.read()
		if f.Type == protocol.TypeStream && f.ID == id {
			break
		}
		if f.Type != protocol.TypeStreamEnd || i > 50 {
			t.Fatalf("a freed slot was not reused: %+v", f)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type findingSource struct {
	fakeSource
	fmu      sync.Mutex
	findings []model.Finding
	ver      uint64
}

func (f *findingSource) Findings() ([]model.Finding, uint64) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	return f.findings, f.ver
}

func (f *findingSource) set(fs ...model.Finding) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.findings = fs
	f.ver++
}

func TestSessionSendsFindings(t *testing.T) {
	conns := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conns <- c
		<-release
	}))
	defer srv.Close()
	defer close(release)
	src := &findingSource{}
	src.set(model.Finding{ID: "job-buildup/prefect", Kind: model.FindingJobBuildup, Severity: model.SeverityWarning, Namespace: "prefect"})
	s := &Session{
		URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/v1/connect", Cluster: "dev", Token: "t",
		Source: src, Handler: fakeHandler{}, Logger: discardLogger(), DeltaInterval: 10 * time.Millisecond, PingInterval: time.Minute,
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	hub := hubConn{t: t, conn: <-conns}
	hub.read() // hello
	f := hub.read()
	var snap protocol.Snapshot
	_ = json.Unmarshal(f.Payload, &snap)
	if f.Type != protocol.TypeSnapshot || snap.Findings == nil || len(snap.Findings.Items) != 1 || snap.Findings.Items[0].Namespace != "prefect" {
		t.Fatalf("snapshot %s %+v", f.Type, snap.Findings)
	}
	// Unchanged findings are not resent; a change travels as a delta, and
	// an empty set clears them.
	src.set()
	f = hub.read()
	var d protocol.Delta
	_ = json.Unmarshal(f.Payload, &d)
	if f.Type != protocol.TypeDelta || d.Findings == nil || len(d.Findings.Items) != 0 || len(d.Upserts)+len(d.Deletes) != 0 {
		t.Fatalf("delta %s %s", f.Type, f.Payload)
	}
	if !strings.Contains(string(f.Payload), `"findings":{"items":[]}`) {
		t.Fatalf("an empty set must be sent explicitly: %s", f.Payload)
	}
}
