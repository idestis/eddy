//go:build dev

package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/loadgen/synth"
)

// noRules makes the fake agents answer OpRules like agents that predate
// it, so the hub asks SubjectAccessReviews only (the P1 behaviour).
var noRules = flag.Bool("no-rules", false, "fake agents refuse rules reviews (measure SubjectAccessReviews only, as in P1)")

// rulesReport is the rules reviews per user per minute, by role, between
// two snapshots of the counter.
func rulesReport(e *env, browsers []*browser, before map[string]int64, d time.Duration) []string {
	after := e.sar.rulesSnapshot()
	byRole := map[synth.Role][]float64{}
	for _, b := range browsers {
		var n int64
		for user, v := range after {
			if strings.HasSuffix(user, b.user.Name) {
				n += v - before[user]
			}
		}
		byRole[b.user.Role] = append(byRole[b.user.Role], float64(n)/d.Minutes())
	}
	var out []string
	for _, role := range []synth.Role{synth.RoleAdmin, synth.RoleTeam, synth.RoleMixed} {
		v := byRole[role]
		if len(v) == 0 {
			continue
		}
		var sum float64
		for _, x := range v {
			sum += x
		}
		out = append(out, fmt.Sprintf("- Rules reviews (namespaces) per user per minute, %s: %.0f", role, sum/float64(len(v))))
	}
	var total int64
	for _, v := range after {
		total += v
	}
	out = append(out, fmt.Sprintf("- Rules reviews answered by agents in total: %d (in %d rules requests; baseline user included)", total, e.sar.rulesRequests.Load()))
	return out
}
