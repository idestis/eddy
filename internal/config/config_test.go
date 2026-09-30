package config

import (
	"strings"
	"testing"
)

const minimalHub = `publicURL: https://eddy.example.com
dev: {fakeLogin: true}
`

func TestHubStoreDrivers(t *testing.T) {
	t.Setenv("EDDY_DATABASE_URL", "")
	tests := []struct {
		name    string
		yaml    string
		dsn     string
		wantErr string
		check   func(t *testing.T, h *Hub)
	}{
		{name: "postgres is the default and needs a DSN", yaml: "", wantErr: "EDDY_DATABASE_URL"},
		{name: "postgres with a DSN", yaml: "", dsn: "postgres://eddy@db/eddy", check: func(t *testing.T, h *Hub) {
			if h.Store.Driver != "postgres" || h.EphemeralStore() || h.Peer.Listen != "" {
				t.Fatalf("store %+v peer %+v", h.Store, h.Peer)
			}
		}},
		{name: "memory is ephemeral", yaml: "store: {driver: memory}\n", check: func(t *testing.T, h *Hub) {
			if !h.EphemeralStore() {
				t.Fatal("memory store not reported as ephemeral")
			}
		}},
		{name: "sqlite is gone", yaml: "store: {driver: sqlite}\n", wantErr: `store.driver "sqlite" is not supported`},
		{name: "sqlite path key is gone", yaml: "store: {driver: memory, path: /var/lib/eddy/eddy.db}\n", wantErr: "unknown field"},
		{name: "peer listen must be host:port", yaml: "store: {driver: memory}\npeer: {listen: '8444', podName: hub-0}\n", wantErr: "peer.listen"},
		{name: "peer defaults from the pod environment", yaml: "store: {driver: memory}\npeer: {listen: ':8444'}\n", check: func(t *testing.T, h *Hub) {
			if h.Peer.PodName != "eddy-hub-7c9f-abcde" || h.Peer.Advertise != "10.1.2.3:8444" {
				t.Fatalf("peer %+v", h.Peer)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EDDY_DATABASE_URL", tc.dsn)
			t.Setenv("POD_NAME", "eddy-hub-7c9f-abcde")
			t.Setenv("POD_IP", "10.1.2.3")
			h, err := ParseHub([]byte(minimalHub + tc.yaml))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, h)
		})
	}
}

func TestAgentLimits(t *testing.T) {
	base := map[string]string{
		"EDDY_CLUSTER": "dev", "EDDY_AGENT_TOKEN": "t", "EDDY_HUB_URL": "wss://hub.example.com/agent/v1/connect",
	}
	set := func(t *testing.T, extra map[string]string) {
		for _, k := range []string{"EDDY_KUBE_QPS", "EDDY_KUBE_BURST", "EDDY_MAX_CONCURRENT", "EDDY_MAX_LOG_STREAMS", "EDDY_MAX_SAR_CONCURRENT"} {
			t.Setenv(k, "")
		}
		for k, v := range base {
			t.Setenv(k, v)
		}
		for k, v := range extra {
			t.Setenv(k, v)
		}
	}
	t.Run("defaults", func(t *testing.T) {
		set(t, nil)
		a, err := LoadAgent()
		if err != nil {
			t.Fatal(err)
		}
		if a.KubeQPS != 20 || a.KubeBurst != 40 || a.MaxConcurrent != 16 || a.MaxLogStreams != 8 || a.MaxSARConcurrent != 8 {
			t.Fatalf("limits %+v", a)
		}
	})
	t.Run("set", func(t *testing.T) {
		set(t, map[string]string{"EDDY_KUBE_QPS": "10", "EDDY_KUBE_BURST": "20", "EDDY_MAX_CONCURRENT": "8", "EDDY_MAX_LOG_STREAMS": "4", "EDDY_MAX_SAR_CONCURRENT": "2"})
		a, err := LoadAgent()
		if err != nil {
			t.Fatal(err)
		}
		if a.KubeQPS != 10 || a.KubeBurst != 20 || a.MaxConcurrent != 8 || a.MaxLogStreams != 4 || a.MaxSARConcurrent != 2 {
			t.Fatalf("limits %+v", a)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		set(t, map[string]string{"EDDY_KUBE_QPS": "fast", "EDDY_MAX_CONCURRENT": "0"})
		_, err := LoadAgent()
		if err == nil || !strings.Contains(err.Error(), "EDDY_KUBE_QPS") || !strings.Contains(err.Error(), "EDDY_MAX_CONCURRENT") {
			t.Fatalf("error %v", err)
		}
	})
}
