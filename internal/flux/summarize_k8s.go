package flux

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/model"
)

// Summaries of the plain Kubernetes kinds that Flux commonly manages:
// Services, Ingresses, Jobs, CronJobs, HorizontalPodAutoscalers and
// PersistentVolumeClaims. Like the other summarizers they read only
// metadata, descriptive spec fields and status.

// maxListed caps the ports and hosts copied into a summary.
const maxListed = 16

// lbAddresses lists status.loadBalancer.ingress[].ip or .hostname.
func lbAddresses(obj map[string]any) []string {
	var out []string
	for _, in := range maps(obj, "status", "loadBalancer", "ingress") {
		if a := firstNonEmpty(str(in, "ip"), str(in, "hostname")); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// intOrString renders an IntOrString field (a port number or name).
func intOrString(m map[string]any, key string) string {
	if n, ok := integer(m, key); ok {
		return strconv.FormatInt(n, 10)
	}
	return str(m, key)
}

// summarizeService: type, cluster IP, ports and, for LoadBalancers, the
// address. A LoadBalancer without an address is still being provisioned.
func summarizeService(obj map[string]any, r *model.Resource) {
	typ := firstNonEmpty(str(obj, "spec", "type"), "ClusterIP")
	for _, p := range maps(obj, "spec", "ports") {
		if len(r.Ports) == maxListed {
			break
		}
		port, _ := integer(p, "port")
		s := fmt.Sprintf("%d/%s", port, firstNonEmpty(str(p, "protocol"), "TCP"))
		if t := intOrString(p, "targetPort"); t != "" && t != strconv.FormatInt(port, 10) {
			s += " → " + t
		}
		r.Ports = append(r.Ports, s)
	}
	switch typ {
	case "ExternalName":
		r.Status, r.Message = model.StatusReady, "ExternalName "+str(obj, "spec", "externalName")
	case "LoadBalancer":
		if addrs := lbAddresses(obj); len(addrs) > 0 {
			r.Status, r.Message = model.StatusReady, "LoadBalancer "+strings.Join(addrs, ", ")
		} else {
			r.Status, r.Message = model.StatusReconciling, "LoadBalancer: waiting for an address"
		}
	default:
		ip := str(obj, "spec", "clusterIP")
		switch {
		case ip == "None":
			r.Message = typ + " (headless)"
		case ip != "":
			r.Message = typ + " " + ip
		default:
			r.Message = typ
		}
		r.Status = model.StatusReady
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// summarizeIngress: class, hosts, TLS and address. An Ingress is ready as
// soon as it exists; many controllers never publish an address.
func summarizeIngress(obj map[string]any, r *model.Resource) {
	for _, rule := range maps(obj, "spec", "rules") {
		if h := str(rule, "host"); h != "" && !slices.Contains(r.Hosts, h) && len(r.Hosts) < maxListed {
			r.Hosts = append(r.Hosts, h)
		}
	}
	var parts []string
	if c := str(obj, "spec", "ingressClassName"); c != "" {
		parts = append(parts, c)
	}
	switch len(r.Hosts) {
	case 0:
		parts = append(parts, "any host")
	case 1:
		parts = append(parts, r.Hosts[0])
	default:
		parts = append(parts, fmt.Sprintf("%s +%d", r.Hosts[0], len(r.Hosts)-1))
	}
	if len(list(obj, "spec", "tls")) > 0 {
		parts = append(parts, "TLS")
	}
	if addrs := lbAddresses(obj); len(addrs) > 0 {
		parts = append(parts, strings.Join(addrs, ", "))
	}
	r.Status, r.Message = model.StatusReady, OneLine(strings.Join(parts, " · "), maxMessage)
}

// summarizeJob: the Failed and Complete conditions first, then suspended,
// then active pods. Completions is "succeeded/completions"; Replicas carries
// the same only while the Job has not finished, so a finished Job does not
// look like it has live pods. A finished Job's LastChanged is its finish time.
func summarizeJob(obj map[string]any, r *model.Resource) {
	r.Images = podSpecImages(mapping(obj, "spec", "template", "spec"))
	completions, ok := integer(obj, "spec", "completions")
	if !ok {
		completions = 1
	}
	succeeded, _ := integer(obj, "status", "succeeded")
	failed, _ := integer(obj, "status", "failed")
	active, _ := integer(obj, "status", "active")
	r.Completions = fmt.Sprintf("%d/%d", succeeded, completions)
	r.Suspended = boolean(obj, "spec", "suspend")
	complete := condition(r.Conditions, "Complete")
	failedCond := condition(r.Conditions, "Failed")
	switch {
	case isTrue(failedCond):
		msg := firstNonEmpty(failedCond.Reason, "Failed")
		if failedCond.Message != "" {
			msg += ": " + failedCond.Message
		}
		r.Status, r.Message = model.StatusFailed, msg
	case isTrue(complete):
		msg := "Completed"
		start, done := timestamp(obj, "status", "startTime"), timestamp(obj, "status", "completionTime")
		if !start.IsZero() && !done.IsZero() && !done.Before(start) {
			msg += " in " + done.Sub(start).Round(time.Second).String()
		}
		r.Status, r.Message = model.StatusCompleted, msg
	case r.Suspended:
		r.Replicas = r.Completions
		r.Status, r.Message = model.StatusSuspended, "Job suspended"
	case active > 0:
		r.Replicas = r.Completions
		msg := fmt.Sprintf("Running, %d active", active)
		if completions > 1 {
			msg += fmt.Sprintf(", %d/%d succeeded", succeeded, completions)
		}
		if failed > 0 {
			msg += fmt.Sprintf(", %d failed", failed)
		}
		r.Status, r.Message = model.StatusReconciling, msg
	default:
		r.Replicas = r.Completions
		r.Status, r.Message = model.StatusReconciling, "Waiting for pods"
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// summarizeCronJob: suspended or ready, with the schedule and the last run.
func summarizeCronJob(obj map[string]any, r *model.Resource) {
	r.Images = podSpecImages(mapping(obj, "spec", "jobTemplate", "spec", "template", "spec"))
	r.Schedule = OneLine(str(obj, "spec", "schedule"), 100)
	r.Suspended = boolean(obj, "spec", "suspend")
	last := timestamp(obj, "status", "lastScheduleTime").UTC()
	if last.After(r.LastChanged) {
		r.LastChanged = last
	}
	active := len(list(obj, "status", "active"))
	msg := "Not scheduled yet"
	if !last.IsZero() {
		msg = "Last scheduled " + last.Format(time.RFC3339)
	}
	if active > 0 {
		msg += fmt.Sprintf(", %d active", active)
	}
	if r.Suspended {
		r.Status, r.Message = model.StatusSuspended, OneLine("Suspended. "+msg, maxMessage)
		return
	}
	r.Status, r.Message = model.StatusReady, OneLine(msg, maxMessage)
}

// summarizeHPA: failing conditions first, then whether the current replica
// count matches the desired one. Replicas is "current/desired".
func summarizeHPA(obj map[string]any, r *model.Resource) {
	current, _ := integer(obj, "status", "currentReplicas")
	desired, _ := integer(obj, "status", "desiredReplicas")
	minR, ok := integer(obj, "spec", "minReplicas")
	if !ok {
		minR = 1
	}
	maxR, _ := integer(obj, "spec", "maxReplicas")
	r.Replicas = fmt.Sprintf("%d/%d", current, desired)
	summary := fmt.Sprintf("%d replicas (min %d, max %d)", current, minR, maxR)
	if cpu := hpaCPU(obj); cpu != "" {
		summary += ", " + cpu
	}
	able := condition(r.Conditions, "AbleToScale")
	active := condition(r.Conditions, "ScalingActive")
	switch {
	case isFalse(able):
		r.Status, r.Message = model.StatusFailed, firstNonEmpty(able.Message, able.Reason)
	case isFalse(active) && active.Reason != "ScalingDisabled":
		r.Status, r.Message = model.StatusFailed, firstNonEmpty(active.Message, active.Reason)
	case isFalse(active):
		r.Status, r.Message = model.StatusSuspended, "Scaling disabled (target scaled to zero)"
	case current != desired:
		r.Status, r.Message = model.StatusReconciling, fmt.Sprintf("Scaling from %d to %d replicas", current, desired)
	default:
		r.Status, r.Message = model.StatusReady, summary
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// hpaCPU renders "CPU 40% of 80%" from a CPU utilization metric, if any.
func hpaCPU(obj map[string]any) string {
	var target int64
	for _, m := range maps(obj, "spec", "metrics") {
		if str(m, "type") == "Resource" && str(m, "resource", "name") == "cpu" {
			target, _ = integer(m, "resource", "target", "averageUtilization")
		}
	}
	if target == 0 {
		return ""
	}
	for _, m := range maps(obj, "status", "currentMetrics") {
		if str(m, "type") == "Resource" && str(m, "resource", "name") == "cpu" {
			if cur, ok := integer(m, "resource", "current", "averageUtilization"); ok {
				return fmt.Sprintf("CPU %d%% of %d%%", cur, target)
			}
		}
	}
	return fmt.Sprintf("CPU target %d%%", target)
}

// summarizePVC: the phase, with capacity and storage class.
func summarizePVC(obj map[string]any, r *model.Resource) {
	phase := firstNonEmpty(str(obj, "status", "phase"), "Pending")
	parts := []string{phase}
	if c := firstNonEmpty(str(obj, "status", "capacity", "storage"), str(obj, "spec", "resources", "requests", "storage")); c != "" {
		parts = append(parts, c)
	}
	if sc := str(obj, "spec", "storageClassName"); sc != "" {
		parts = append(parts, sc)
	}
	msg := strings.Join(parts, " · ")
	switch phase {
	case "Bound":
		r.Status = model.StatusReady
		if c := condition(r.Conditions, "FileSystemResizePending"); isTrue(c) {
			r.Status, msg = model.StatusReconciling, msg+" · resize pending"
		} else if c := condition(r.Conditions, "Resizing"); isTrue(c) {
			r.Status, msg = model.StatusReconciling, msg+" · resizing"
		}
	case "Lost":
		r.Status = model.StatusFailed
	default:
		r.Status = model.StatusReconciling
	}
	r.Message = OneLine(msg, maxMessage)
}
