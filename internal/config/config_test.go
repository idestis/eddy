package config

import (
	"strings"
	"testing"
	"time"
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
		for _, k := range []string{"EDDY_KUBE_QPS", "EDDY_KUBE_BURST", "EDDY_MAX_CONCURRENT", "EDDY_MAX_LOG_STREAMS", "EDDY_MAX_SAR_CONCURRENT",
			"EDDY_MAX_LOG_PODS", "EDDY_LOG_LINE_RATE", "EDDY_JOB_HISTORY", "EDDY_JOB_FAILED_MAX_AGE", "EDDY_JOB_BUILDUP_THRESHOLD"} {
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
		if a.KubeQPS != 20 || a.KubeBurst != 40 || a.MaxConcurrent != 16 || a.MaxLogStreams != 8 || a.MaxSARConcurrent != 8 ||
			a.MaxLogPods != 20 || a.LogLineRate != 2000 || a.JobHistory != 5 || a.JobFailedMaxAge != 24*time.Hour || a.JobBuildupThreshold != 100 {
			t.Fatalf("limits %+v", a)
		}
	})
	t.Run("set", func(t *testing.T) {
		set(t, map[string]string{"EDDY_KUBE_QPS": "10", "EDDY_KUBE_BURST": "20", "EDDY_MAX_CONCURRENT": "8", "EDDY_MAX_LOG_STREAMS": "4", "EDDY_MAX_SAR_CONCURRENT": "2",
			"EDDY_MAX_LOG_PODS": "5", "EDDY_LOG_LINE_RATE": "100", "EDDY_JOB_HISTORY": "3", "EDDY_JOB_FAILED_MAX_AGE": "90m", "EDDY_JOB_BUILDUP_THRESHOLD": "10"})
		a, err := LoadAgent()
		if err != nil {
			t.Fatal(err)
		}
		if a.KubeQPS != 10 || a.KubeBurst != 20 || a.MaxConcurrent != 8 || a.MaxLogStreams != 4 || a.MaxSARConcurrent != 2 ||
			a.MaxLogPods != 5 || a.LogLineRate != 100 || a.JobHistory != 3 || a.JobFailedMaxAge != 90*time.Minute || a.JobBuildupThreshold != 10 {
			t.Fatalf("limits %+v", a)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		set(t, map[string]string{"EDDY_KUBE_QPS": "fast", "EDDY_MAX_CONCURRENT": "0", "EDDY_JOB_FAILED_MAX_AGE": "1 day"})
		_, err := LoadAgent()
		if err == nil || !strings.Contains(err.Error(), "EDDY_KUBE_QPS") || !strings.Contains(err.Error(), "EDDY_MAX_CONCURRENT") || !strings.Contains(err.Error(), "EDDY_JOB_FAILED_MAX_AGE") {
			t.Fatalf("error %v", err)
		}
	})
}

func TestAgentPresets(t *testing.T) {
	for k, v := range map[string]string{"EDDY_CLUSTER": "dev", "EDDY_AGENT_TOKEN": "t", "EDDY_HUB_URL": "wss://hub.example.com/agent/v1/connect"} {
		t.Setenv(k, v)
	}
	for _, tt := range []struct {
		env  string
		want string
		err  bool
	}{
		{"", "", false},
		{"externalSecrets, karpenter", "karpenter,externalSecrets", false},
		{"karpenter,datadog", "", true},
	} {
		t.Setenv("EDDY_WATCH_PRESETS", tt.env)
		a, err := LoadAgent()
		if tt.err {
			if err == nil || !strings.Contains(err.Error(), "EDDY_WATCH_PRESETS") {
				t.Errorf("%q: error %v", tt.env, err)
			}
			continue
		}
		if err != nil || strings.Join(a.Presets, ",") != tt.want {
			t.Errorf("%q: presets %v, %v", tt.env, a.Presets, err)
		}
	}
}

func TestHubStaleAccessTTL(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    time.Duration
		wantErr string
	}{
		{name: "unset is 2m", yaml: "", want: 2 * time.Minute},
		{name: "0 fails closed", yaml: "auth: {staleAccessTTL: 0s}\n", want: 0},
		{name: "set", yaml: "auth: {staleAccessTTL: 30s}\n", want: 30 * time.Second},
		{name: "at most 10m", yaml: "auth: {staleAccessTTL: 11m}\n", wantErr: "auth.staleAccessTTL"},
		{name: "not negative", yaml: "auth: {staleAccessTTL: -1s}\n", wantErr: "auth.staleAccessTTL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := ParseHub([]byte(minimalHub + "store: {driver: memory}\n" + tc.yaml))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := h.Auth.StaleAccessTTLOrDefault(); got != tc.want {
				t.Fatalf("staleAccessTTL %s, want %s", got, tc.want)
			}
		})
	}
}
