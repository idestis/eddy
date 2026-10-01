package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

func workload(kind, ns, name string) model.Resource {
	k, _ := flux.KindByName(kind)
	r := model.Resource{Ref: model.Ref{Group: k.Group, Kind: k.Kind, Namespace: ns, Name: name}, Status: model.StatusReady, Version: "v1", ResourceVersion: "1"}
	r.ID = r.Ref.ID()
	return r
}

func buildup(ns string, sev model.Severity, hidden int) model.Finding {
	return model.Finding{
		ID: "job-buildup/" + ns, Kind: model.FindingJobBuildup, Severity: sev, Namespace: ns,
		Message:        "finished Jobs pile up in " + ns,
		Recommendation: "set ttlSecondsAfterFinished",
		Jobs: &model.JobBuildup{Hidden: hidden, Finished: hidden + 5, Succeeded: hidden, Failed: 5, WithoutTTL: hidden + 5, Threshold: 100,
			Groups: []model.JobGroup{{By: "label", Name: flux.LabelPrefectDeployment + "=etl", Label: "prefect deployment etl", Count: hidden + 5}}},
	}
}

// connectAgentFindings is connectAgent with findings in the snapshot.
func (e *testEnv) connectAgentFindings(cluster string, resources []model.Resource, findings []model.Finding) *fakeAgent {
	e.t.Helper()
	a, err := dialAgent(context.Background(), e.agentURL(cluster), cluster, testToken)
	if err != nil {
		e.t.Fatalf("dial agent: %v", err)
	}
	e.t.Cleanup(a.close)
	a.sendFrame(protocol.TypeSnapshot, "", protocol.Snapshot{Resources: resources, Findings: &protocol.FindingSet{Items: findings}, Parts: 1})
	go a.serve()
	waitFor(e.t, func() bool {
		s := e.hub.agents.get(cluster)
		return s != nil && s.size() == len(resources) && (len(findings) == 0 || len(s.findingList()) > 0)
	})
	return a
}

// serveWorkloadLogs answers workload log requests like the agent: a pod
// set, entries of two pods and a dropped marker; it ends unless following.
func serveWorkloadLogs(a *fakeAgent) {
	a.onRequest(func(f protocol.Frame, req protocol.Request) bool {
		if req.Op != protocol.OpLogs || req.Target.Kind == flux.KindPod {
			return false
		}
		var args protocol.LogsArgs
		_ = json.Unmarshal(req.Args, &args)
		go func() {
			a.sendFrame(protocol.TypeStream, f.ID, protocol.LogChunk{Pods: &protocol.LogPods{Total: 2, Limit: 20, Pods: []protocol.LogPod{
				{Name: "web-2", Containers: []string{"app"}, Status: model.StatusReady},
				{Name: "web-1", Containers: []string{"app"}, Status: model.StatusReady},
			}}})
			a.sendFrame(protocol.TypeStream, f.ID, protocol.LogChunk{Entries: []protocol.LogEntry{
				{Pod: "web-1", Container: "app", Line: "hello from 1", TS: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)},
				{Pod: "web-2", Container: "app", Line: "hello from 2"},
				{Marker: protocol.MarkerDropped, Line: "dropped 7 lines: more than 2000 lines per second"},
			}})
			if !args.Follow {
				a.sendFrame(protocol.TypeStreamEnd, f.ID, protocol.Response{})
			}
		}()
		return true
	})
}

type workloadEvents struct {
	pods    []protocol.LogPods
	entries []protocol.LogEntry
	end     string
}

func readWorkloadLogs(t *testing.T, ch <-chan sseEvent, until func(w *workloadEvents) bool) *workloadEvents {
	t.Helper()
	w := &workloadEvents{}
	timeout := time.After(5 * time.Second)
	for !until(w) {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream closed")
			}
			switch ev.name {
			case "pods":
				var p protocol.LogPods
				if err := json.Unmarshal([]byte(ev.data), &p); err != nil {
					t.Fatal(err)
				}
				w.pods = append(w.pods, p)
			case "log":
				var l struct{ Entries []protocol.LogEntry }
				if err := json.Unmarshal([]byte(ev.data), &l); err != nil {
					t.Fatal(err)
				}
				w.entries = append(w.entries, l.Entries...)
			case "end":
				w.end = ev.data
			}
		case <-timeout:
			t.Fatalf("timed out; got %+v", w)
		}
	}
	return w
}

func TestWorkloadLogsOverSSE(t *testing.T) {
	e := newEnv(t, "")
	agent := e.connectAgent("dev", testToken, []model.Resource{workload("Deployment", "team-a", "web")})
	serveWorkloadLogs(agent)
	alice := e.login("alice")

	ch, stop := alice.openStream("/api/v1/clusters/dev/workloads/deployment/team-a/web/logs?tail=50&since=60&pods=web-1,web-2&container=app&allContainers=false")
	defer stop()
	w := readWorkloadLogs(t, ch, func(w *workloadEvents) bool { return w.end != "" })
	if w.end != "{}" || len(w.pods) != 1 || w.pods[0].Pods[0].Name != "web-2" || w.pods[0].Total != 2 || w.pods[0].Limit != 20 {
		t.Fatalf("events %+v", w)
	}
	if len(w.entries) != 3 || w.entries[0].Pod != "web-1" || w.entries[0].Line != "hello from 1" || w.entries[0].TS.IsZero() || w.entries[2].Marker != protocol.MarkerDropped {
		t.Fatalf("entries %+v", w.entries)
	}
	reqs := agent.recorded(protocol.OpLogs)
	if len(reqs) != 1 {
		t.Fatalf("requests %+v", reqs)
	}
	var args protocol.LogsArgs
	_ = json.Unmarshal(reqs[0].Args, &args)
	if reqs[0].Target != (model.Ref{Group: "apps", Kind: "Deployment", Namespace: "team-a", Name: "web"}) || reqs[0].Identity.User != "local:alice" ||
		args.TailLines != 50 || args.SinceSeconds != 60 || !slices.Equal(args.Pods, []string{"web-1", "web-2"}) || args.Container != "app" ||
		args.AllContainers == nil || *args.AllContainers || args.Follow {
		t.Fatalf("request %+v args %+v", reqs[0], args)
	}

	for _, tc := range []struct {
		who, path string
		want      int
	}{
		{"bob", "/api/v1/clusters/dev/workloads/Deployment/team-a/web/logs", 404},
		{"alice", "/api/v1/clusters/dev/workloads/Deployment/team-a/missing/logs", 404},
		{"alice", "/api/v1/clusters/dev/workloads/Service/team-a/web/logs", 400},
		{"alice", "/api/v1/clusters/dev/workloads/Pod/team-a/web/logs", 400},
		{"alice", "/api/v1/clusters/dev/workloads/ReplicaSet/team-a/web/logs", 400},
		{"alice", "/api/v1/clusters/dev/workloads/Deployment/team-a/web/logs?tail=5000", 400},
		{"alice", "/api/v1/clusters/dev/workloads/Deployment/team-a/web/logs?since=-1", 400},
		{"alice", "/api/v1/clusters/dev/workloads/Deployment/team-a/web/logs?follow=maybe", 400},
	} {
		c := alice
		if tc.who == "bob" {
			c = e.login("bob")
		}
		if st, _ := c.errorCode("GET", tc.path, nil); st != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.who, tc.path, st, tc.want)
		}
	}
	if n := len(agent.recorded(protocol.OpLogs)); n != 1 {
		t.Fatalf("rejected requests reached the agent: %d", n)
	}

	// A Job the agent hides is not in the view but its logs still work.
	chJob, stopJob := alice.openStream("/api/v1/clusters/dev/workloads/Job/team-a/migrate-1/logs")
	defer stopJob()
	if w := readWorkloadLogs(t, chJob, func(w *workloadEvents) bool { return w.end != "" }); w.end != "{}" || len(w.entries) != 3 {
		t.Fatalf("job logs %+v", w)
	}
}

// TestWorkloadLogsPodVisibility: pods the user may neither list nor get are
// removed from the pod set and their lines are dropped at the hub.
func TestWorkloadLogsPodVisibility(t *testing.T) {
	e := newEnv(t, "")
	agent := e.connectAgent("dev", testToken, []model.Resource{workload("Deployment", "team-a", "web")})
	serveWorkloadLogs(agent)
	agent.mu.Lock()
	agent.allow = func(id protocol.Identity, c protocol.AccessCheck) bool {
		if c.Resource == "pods" {
			return c.Verb == "get" && c.Name == "web-2"
		}
		return defaultAllow(id, c)
	}
	agent.mu.Unlock()
	ch, stop := e.login("alice").openStream("/api/v1/clusters/dev/workloads/Deployment/team-a/web/logs")
	defer stop()
	w := readWorkloadLogs(t, ch, func(w *workloadEvents) bool { return w.end != "" })
	if len(w.pods) != 1 || len(w.pods[0].Pods) != 1 || w.pods[0].Pods[0].Name != "web-2" || w.pods[0].Total != 1 {
		t.Fatalf("pods %+v", w.pods)
	}
	for _, en := range w.entries {
		if en.Pod == "web-1" {
			t.Fatalf("line of an invisible pod: %+v", en)
		}
	}
	if len(w.entries) != 2 {
		t.Fatalf("entries %+v", w.entries)
	}
}

func TestFindingsAndHiddenJobs(t *testing.T) {
	e := newEnv(t, "")
	shown := workload("Job", "team-a", "etl-6")
	shown.Status = model.StatusCompleted
	agent := e.connectAgentFindings("dev", []model.Resource{shown}, []model.Finding{
		buildup("team-a", model.SeverityWarning, 150),
		buildup("team-b", model.SeverityInfo, 3),
		{ID: "x/team-a", Kind: "made-up", Severity: model.SeverityWarning, Namespace: "team-a", Jobs: &model.JobBuildup{}}, // dropped by the hub
	})
	hiddenJob := func(ns, name string) model.Resource {
		r := workload("Job", ns, name)
		r.Status = model.StatusCompleted
		return r
	}
	var asked [][]string
	agent.onRequest(func(f protocol.Frame, req protocol.Request) bool {
		if req.Op != protocol.OpHiddenJobs {
			return false
		}
		var args protocol.HiddenJobsArgs
		_ = json.Unmarshal(req.Args, &args)
		agent.mu.Lock()
		asked = append(asked, args.Namespaces)
		agent.mu.Unlock()
		// An agent that answers outside the namespaces asked is filtered
		// out by the hub.
		items := []model.Resource{hiddenJob("team-c", "intruder")}
		next := 0
		if slices.Contains(args.Namespaces, "team-a") {
			if args.Offset == 0 {
				items, next = append(items, hiddenJob("team-a", "etl-5")), 2
			} else {
				items = append(items, hiddenJob("team-a", "etl-3"))
			}
		}
		agent.reply(f.ID, protocol.HiddenJobsResult{Items: items, Total: 3, Next: next}, nil)
		return true
	})
	alice := e.login("alice")

	var clusters struct{ Items []model.ClusterInfo }
	alice.do("GET", "/api/v1/clusters", nil, &clusters, http.StatusOK)
	dev := clusters.Items[slices.IndexFunc(clusters.Items, func(c model.ClusterInfo) bool { return c.Name == "dev" })]
	if len(dev.Findings) != 1 || dev.Findings[0].ID != "job-buildup/team-a" || dev.Findings[0].Jobs.Hidden != 150 || dev.Counts[model.StatusCompleted] != 1 {
		t.Fatalf("dev for alice: findings %+v counts %v", dev.Findings, dev.Counts)
	}
	var fs struct{ Items []model.Finding }
	e.login("ops").do("GET", "/api/v1/clusters/dev/findings", nil, &fs, http.StatusOK)
	if len(fs.Items) != 2 {
		t.Fatalf("ops findings %+v", fs.Items)
	}

	type page struct {
		Items  []model.Resource
		Hidden struct {
			Total int
			Next  string
		}
	}
	var p1 page
	alice.do("GET", "/api/v1/clusters/dev/resources?kind=Job&includeHidden=1", nil, &p1, http.StatusOK)
	var names []string
	for _, r := range p1.Items {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"etl-6", "etl-5"}) || p1.Hidden.Total != 3 || p1.Hidden.Next != "2" {
		t.Fatalf("first page %v %+v", names, p1.Hidden)
	}
	var p2 page
	alice.do("GET", "/api/v1/clusters/dev/resources?kind=Job&includeHidden=1&cursor=2&limit=100", nil, &p2, http.StatusOK)
	if len(p2.Items) != 1 || p2.Items[0].Name != "etl-3" || p2.Hidden.Next != "" {
		t.Fatalf("second page %+v", p2)
	}
	// Bob may list Jobs in team-b only: the agent is asked about team-b.
	var pb page
	e.login("bob").do("GET", "/api/v1/clusters/dev/resources?kind=Job&includeHidden=true", nil, &pb, http.StatusOK)
	agent.mu.Lock()
	gotAsked := slices.Clone(asked)
	agent.mu.Unlock()
	if len(pb.Items) != 0 || len(gotAsked) != 3 || !slices.Equal(gotAsked[0], []string{"team-a"}) || !slices.Equal(gotAsked[2], []string{"team-b"}) {
		t.Fatalf("bob %+v, agent asked %v", pb, gotAsked)
	}
	// A namespace filter without a finding does not reach the agent.
	e.login("ops").do("GET", "/api/v1/clusters/dev/resources?kind=Job&includeHidden=1&namespace=team-z", nil, &pb, http.StatusOK)
	if len(agent.recorded(protocol.OpHiddenJobs)) != 3 {
		t.Fatal("agent asked about a namespace with nothing hidden")
	}
	for _, path := range []string{
		"/api/v1/clusters/dev/resources?includeHidden=1",
		"/api/v1/clusters/dev/resources?kind=Pod&includeHidden=1",
		"/api/v1/clusters/dev/resources?kind=Job&includeHidden=1&limit=5000",
		"/api/v1/clusters/dev/resources?kind=Job&includeHidden=1&cursor=x",
	} {
		if st, _ := alice.errorCode("GET", path, nil); st != 400 {
			t.Errorf("%s: %d", path, st)
		}
	}
	var byStatus page
	alice.do("GET", "/api/v1/clusters/dev/resources?status=completed", nil, &byStatus, http.StatusOK)
	if len(byStatus.Items) != 1 {
		t.Fatalf("status=completed %+v", byStatus.Items)
	}

	// A findings-only delta replaces the set.
	agent.sendFrame(protocol.TypeDelta, "", protocol.Delta{Findings: &protocol.FindingSet{Items: []model.Finding{buildup("team-a", model.SeverityInfo, 7)}}})
	waitFor(t, func() bool {
		var fs struct{ Items []model.Finding }
		alice.do("GET", "/api/v1/clusters/dev/findings", nil, &fs, http.StatusOK)
		return len(fs.Items) == 1 && fs.Items[0].Severity == model.SeverityInfo && fs.Items[0].Jobs.Hidden == 7
	})
}

// TestRelayWorkloadLogsAndFindings: findings and workload log streams work
// on a replica that relays to the one holding the agent.
func TestRelayWorkloadLogsAndFindings(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	agent := a.dialInstance("dev", "i-1", 1, []model.Resource{workload("Deployment", "team-a", "web")})
	agent.sendFrame(protocol.TypeDelta, "", protocol.Delta{Findings: &protocol.FindingSet{Items: []model.Finding{buildup("team-a", model.SeverityWarning, 500)}}})
	serveWorkloadLogs(agent)
	remote := waitRemote(t, b, "dev", 1)
	waitForLong(t, 10*time.Second, func() bool { return len(remote.findingList()) == 1 })

	alice := b.login("alice")
	var fs struct{ Items []model.Finding }
	alice.do("GET", "/api/v1/clusters/dev/findings", nil, &fs, http.StatusOK)
	if len(fs.Items) != 1 || fs.Items[0].Jobs.Hidden != 500 {
		t.Fatalf("relayed findings %+v", fs.Items)
	}
	agent.sendFrame(protocol.TypeDelta, "", protocol.Delta{Findings: &protocol.FindingSet{Items: []model.Finding{}}})
	waitForLong(t, 10*time.Second, func() bool { return len(remote.findingList()) == 0 })

	ch, stop := alice.openStream("/api/v1/clusters/dev/workloads/Deployment/team-a/web/logs?follow=1")
	w := readWorkloadLogs(t, ch, func(w *workloadEvents) bool { return len(w.pods) == 1 && len(w.entries) == 3 })
	if w.pods[0].Pods[1].Name != "web-1" {
		t.Fatalf("relayed pods %+v", w.pods)
	}
	stop()
	select {
	case <-agent.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never received the relayed cancel")
	}
}
