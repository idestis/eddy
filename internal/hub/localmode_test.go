package hub

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// A local-mode agent's Hello fields reach /api/v1/clusters, and the hub
// refuses writes to a read-only one without forwarding them.
func TestLocalModeHelloExposedAndReadOnlyEnforced(t *testing.T) {
	e := newEnv(t, "")
	hello := protocol.Hello{
		Protocol: protocol.Version, Cluster: "dev", AgentVersion: "dev", KubernetesVersion: "v1.33.0",
		Mode: protocol.ModeLocal, ReadOnly: true, Context: "arn:aws:eks:eu-west-2:123:cluster/dev\x07",
	}
	a, err := dialAgentHello(context.Background(), e.agentURL("dev"), testToken, hello)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	a.sendFrame(protocol.TypeSnapshot, "", protocol.Snapshot{Resources: []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)}})
	go a.serve()
	waitFor(t, func() bool { s := e.hub.agents.get("dev"); return s != nil && s.size() == 1 })

	alice := e.login("alice")
	var clusters struct{ Items []model.ClusterInfo }
	alice.do("GET", "/api/v1/clusters", nil, &clusters, 200)
	var dev, prod model.ClusterInfo
	for _, c := range clusters.Items {
		switch c.Name {
		case "dev":
			dev = c
		case "prod":
			prod = c
		}
	}
	if dev.Mode != "local" || !dev.ReadOnly || dev.Context != "arn:aws:eks:eu-west-2:123:cluster/dev" {
		t.Fatalf("dev cluster %+v", dev)
	}
	if prod.Mode != "" || prod.ReadOnly || prod.Context != "" {
		t.Fatalf("prod cluster %+v", prod)
	}

	st, code := alice.errorCode("POST", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/reconcile", map[string]any{})
	if st != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("reconcile on read-only: %d %s", st, code)
	}
	if n := len(a.recorded(protocol.OpReconcile)); n != 0 {
		t.Fatalf("hub forwarded %d writes to a read-only agent", n)
	}
}

func TestUnknownAgentModeRejected(t *testing.T) {
	h := protocol.Hello{Protocol: protocol.Version, Cluster: "dev", Mode: "turbo"}
	if err := validateHello(&h, "dev"); err == nil || !strings.Contains(err.Error(), "unknown agent mode") {
		t.Fatalf("got %v", err)
	}
	h.Mode = protocol.ModeLocal
	if err := validateHello(&h, "dev"); err != nil {
		t.Fatal(err)
	}
}
