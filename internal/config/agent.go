package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Agent is configured entirely from environment variables (set by the
// eddy-agent chart), so no file mount is needed.
type Agent struct {
	Cluster              string   // EDDY_CLUSTER, must match the Cluster CR name
	HubURL               string   // EDDY_HUB_URL, e.g. wss://eddy-agents.internal.example.com/agent/v1/connect
	Token                string   // EDDY_AGENT_TOKEN
	CAFile               string   // EDDY_HUB_CA_FILE, optional
	Namespaces           []string // EDDY_WATCH_NAMESPACES, comma-separated; empty = all
	AllowedGroupPrefixes []string // EDDY_ALLOWED_GROUP_PREFIXES, default "eddy:"
	AllowedGroups        []string // EDDY_ALLOWED_GROUPS, optional exact allowlist
	DenyUserPrefixes     []string // EDDY_DENY_USER_PREFIXES, default "system:,eks:,kubernetes-admin"
	AllowInsecure        bool     // EDDY_ALLOW_INSECURE=1 permits ws:// (dev only)
	HealthAddr           string   // EDDY_HEALTH_ADDR, default ":8081"
}

// LoadAgent reads the agent configuration from the environment.
func LoadAgent() (*Agent, error) {
	list := func(k string) []string {
		var out []string
		for _, s := range strings.Split(os.Getenv(k), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	a := &Agent{
		Cluster:              os.Getenv("EDDY_CLUSTER"),
		HubURL:               os.Getenv("EDDY_HUB_URL"),
		Token:                os.Getenv("EDDY_AGENT_TOKEN"),
		CAFile:               os.Getenv("EDDY_HUB_CA_FILE"),
		Namespaces:           list("EDDY_WATCH_NAMESPACES"),
		AllowedGroupPrefixes: list("EDDY_ALLOWED_GROUP_PREFIXES"),
		AllowedGroups:        list("EDDY_ALLOWED_GROUPS"),
		DenyUserPrefixes:     list("EDDY_DENY_USER_PREFIXES"),
		HealthAddr:           os.Getenv("EDDY_HEALTH_ADDR"),
	}
	a.AllowInsecure, _ = strconv.ParseBool(os.Getenv("EDDY_ALLOW_INSECURE"))
	if len(a.AllowedGroupPrefixes) == 0 {
		a.AllowedGroupPrefixes = []string{"eddy:"}
	}
	if len(a.DenyUserPrefixes) == 0 {
		a.DenyUserPrefixes = []string{"system:", "eks:", "kubernetes-admin"}
	}
	if a.HealthAddr == "" {
		a.HealthAddr = ":8081"
	}
	var errs []error
	if a.Cluster == "" {
		errs = append(errs, errors.New("EDDY_CLUSTER is required"))
	}
	if a.Token == "" {
		errs = append(errs, errors.New("EDDY_AGENT_TOKEN is required"))
	}
	switch {
	case strings.HasPrefix(a.HubURL, "wss://"):
	case strings.HasPrefix(a.HubURL, "ws://") && a.AllowInsecure:
	default:
		errs = append(errs, fmt.Errorf("EDDY_HUB_URL must be wss:// (ws:// needs EDDY_ALLOW_INSECURE=1), got %q", a.HubURL))
	}
	for _, p := range a.AllowedGroupPrefixes {
		if strings.HasPrefix(p, "system:") {
			errs = append(errs, errors.New(`EDDY_ALLOWED_GROUP_PREFIXES must not include the "system:" prefix`))
		}
	}
	return a, errors.Join(errs...)
}
