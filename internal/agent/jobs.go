package agent

import (
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// JobPolicy decides which Jobs the cache surfaces. Clusters that never clean
// up finished Jobs (no ttlSecondsAfterFinished, no owner with history
// limits) can hold tens of thousands of them, which would flood every list.
// The cache still watches every Job, but surfaces only:
//
//   - Jobs that have not finished;
//   - failed Jobs that finished within FailedMaxAge;
//   - the newest History finished Jobs of each group (see flux.JobFactsOf
//     for how Jobs are grouped), and at most History×jobNamespaceCapFactor
//     such Jobs per namespace.
//
// Every other finished Job is hidden: it is left out of snapshots and deltas
// (the hub fetches such Jobs on demand with OpHiddenJobs) and described by a
// job-buildup model.Finding for its namespace, which is a warning once more
// than BuildupThreshold Jobs are hidden, info otherwise.
type JobPolicy struct {
	History          int
	FailedMaxAge     time.Duration
	BuildupThreshold int
}

// jobNamespaceCapFactor bounds the finished Jobs surfaced per namespace to
// History×jobNamespaceCapFactor, whatever the grouping finds.
const jobNamespaceCapFactor = 10

// maxJobGroups caps JobHistory.Groups.
const maxJobGroups = 5

func (p JobPolicy) withDefaults() JobPolicy {
	if p.History <= 0 {
		p.History = config.DefaultJobHistory
	}
	if p.FailedMaxAge <= 0 {
		p.FailedMaxAge = config.DefaultJobFailedMaxAge
	}
	if p.BuildupThreshold <= 0 {
		p.BuildupThreshold = config.DefaultJobBuildupThreshold
	}
	return p
}

// jobEntry is a watched Job: its summary and the facts the policy needs.
type jobEntry struct {
	r model.Resource
	f flux.JobFacts
}

// putJobLocked records a Job; the namespace is re-evaluated on the next
// Snapshot or Drain.
func (c *Cache) putJobLocked(r model.Resource, f flux.JobFacts) {
	ns := c.jobs[r.Namespace]
	if ns == nil {
		ns = map[string]*jobEntry{}
		c.jobs[r.Namespace] = ns
	}
	ns[r.ID] = &jobEntry{r: r, f: f}
	c.jobsDirty[r.Namespace] = struct{}{}
}

func (c *Cache) deleteJobLocked(namespace, id string) {
	if ns := c.jobs[namespace]; ns != nil {
		delete(ns, id)
		if len(ns) == 0 {
			delete(c.jobs, namespace)
		}
	}
	if _, ok := c.resources[id]; ok {
		delete(c.resources, id)
		c.pending[id] = struct{}{}
	}
	c.jobsDirty[namespace] = struct{}{}
}

// flushJobsLocked re-evaluates every namespace whose Jobs changed or whose
// oldest surfaced recent failure has aged out.
func (c *Cache) flushJobsLocked() {
	now := c.clock()
	for ns, at := range c.jobExpiry {
		if !now.Before(at) {
			c.jobsDirty[ns] = struct{}{}
		}
	}
	for ns := range c.jobsDirty {
		c.evaluateJobsLocked(ns, now)
	}
	clear(c.jobsDirty)
}

// evaluateJobsLocked applies the policy to one namespace: surfaced Jobs are
// put, hidden ones deleted from the summaries, and the history row updated.
func (c *Cache) evaluateJobsLocked(namespace string, now time.Time) {
	p := c.jobPolicy
	entries := c.jobs[namespace]
	show := make(map[string]bool, 64)
	finished := make([]*jobEntry, 0, len(entries))
	var expiry time.Time
	for id, e := range entries {
		if !e.f.Finished {
			show[id] = true
			continue
		}
		finished = append(finished, e)
		if e.f.Failed {
			if until := e.f.FinishedAt.Add(p.FailedMaxAge); now.Before(until) {
				show[id] = true
				if expiry.IsZero() || until.Before(expiry) {
					expiry = until
				}
			}
		}
	}
	if expiry.IsZero() {
		delete(c.jobExpiry, namespace)
	} else {
		c.jobExpiry[namespace] = expiry
	}
	slices.SortFunc(finished, func(a, b *jobEntry) int {
		return cmp.Or(b.f.FinishedAt.Compare(a.f.FinishedAt), strings.Compare(a.r.Name, b.r.Name))
	})
	perGroup := map[string]int{}
	kept, capacity := 0, p.History*jobNamespaceCapFactor
	for _, e := range finished {
		g := e.f.Group.Key()
		if perGroup[g] >= p.History || kept >= capacity {
			continue
		}
		perGroup[g]++
		kept++
		show[e.r.ID] = true
	}

	shownFinished := 0
	for id, e := range entries {
		if show[id] {
			if e.f.Finished {
				shownFinished++
			}
			c.putLocked(e.r)
			continue
		}
		if _, ok := c.resources[id]; ok {
			delete(c.resources, id)
			c.pending[id] = struct{}{}
		}
	}

	hidden := len(finished) - shownFinished
	old, had := c.findings[namespace]
	if hidden <= 0 {
		if had {
			delete(c.findings, namespace)
			c.findingsVer++
		}
		return
	}
	f := jobBuildupFinding(namespace, finished, hidden, p)
	if !had || !reflect.DeepEqual(old, f) {
		c.findings[namespace] = f
		c.findingsVer++
	}
}

// jobBuildupFinding describes a namespace's finished Jobs; finished is
// sorted newest first.
func jobBuildupFinding(namespace string, finished []*jobEntry, hidden int, p JobPolicy) model.Finding {
	h := &model.JobBuildup{Hidden: hidden, Finished: len(finished), Threshold: p.BuildupThreshold}
	groups := map[string]*model.JobGroup{}
	var order []string
	prefect := false
	for _, e := range finished {
		f := e.f
		if f.Failed {
			h.Failed++
		} else {
			h.Succeeded++
		}
		if !f.HasTTL {
			h.WithoutTTL++
			if f.Failed && !f.Owned {
				h.StandaloneFailed++
			}
		}
		if h.Oldest.IsZero() || f.FinishedAt.Before(h.Oldest) {
			h.Oldest = f.FinishedAt
		}
		if f.FinishedAt.After(h.Newest) {
			h.Newest = f.FinishedAt
		}
		k := f.Group.Key()
		g := groups[k]
		if g == nil {
			g = &model.JobGroup{By: f.Group.By, Name: f.Group.Name, Label: f.Group.Label, Owner: f.Group.Owner}
			groups[k] = g
			order = append(order, k)
		}
		g.Count++
		if f.Failed {
			g.Failed++
		}
		if !f.HasTTL {
			g.WithoutTTL++
		}
		if strings.HasPrefix(f.Group.Name, "prefect.io/") {
			prefect = true
		}
	}
	for _, k := range order {
		h.Groups = append(h.Groups, *groups[k])
	}
	slices.SortStableFunc(h.Groups, func(a, b model.JobGroup) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Label, b.Label))
	})
	if len(h.Groups) > maxJobGroups {
		h.Groups = h.Groups[:maxJobGroups]
	}

	f := model.Finding{
		ID:        string(model.FindingJobBuildup) + "/" + namespace,
		Kind:      model.FindingJobBuildup,
		Severity:  model.SeverityInfo,
		Namespace: namespace,
		Jobs:      h,
		Message:   fmt.Sprintf("%s older finished Jobs hidden", thousands(hidden)),
	}
	if hidden <= p.BuildupThreshold {
		return f
	}
	f.Severity = model.SeverityWarning
	f.Recommendation = jobRecommendation(h, prefect)
	msg := fmt.Sprintf("%s finished Jobs in %s (%s hidden), %s without ttlSecondsAfterFinished",
		thousands(h.Finished), namespace, thousands(hidden), thousands(h.WithoutTTL))
	if h.StandaloneFailed > 0 {
		msg += fmt.Sprintf("; %s failed standalone Jobs are never garbage-collected", thousands(h.StandaloneFailed))
	}
	f.Message = flux.OneLine(msg, 300)
	return f
}

// Findings returns the cluster's findings, sorted by id, and a version that
// changes whenever they do.
func (c *Cache) Findings() ([]model.Finding, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushJobsLocked()
	out := make([]model.Finding, 0, len(c.findings))
	for _, f := range c.findings {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b model.Finding) int { return strings.Compare(a.ID, b.ID) })
	return out, c.findingsVer
}

// Hidden Jobs pages.
const (
	defaultHiddenJobs = 500
	maxHiddenJobs     = 1000
)

// HiddenJobs returns a page of the Jobs the policy hides in namespaces,
// ordered by namespace, then newest finished first. budget caps the JSON
// size of the items; the page ends early when it is reached.
func (c *Cache) HiddenJobs(namespaces []string, offset, limit, budget int) protocol.HiddenJobsResult {
	switch {
	case limit <= 0:
		limit = defaultHiddenJobs
	case limit > maxHiddenJobs:
		limit = maxHiddenJobs
	}
	offset = max(offset, 0)
	ns := slices.Clone(namespaces)
	slices.Sort(ns)
	ns = slices.Compact(ns)
	c.mu.Lock()
	c.flushJobsLocked()
	var hidden []*jobEntry
	for _, n := range ns {
		start := len(hidden)
		for id, e := range c.jobs[n] {
			if _, shown := c.resources[id]; !shown {
				hidden = append(hidden, e)
			}
		}
		slices.SortFunc(hidden[start:], func(a, b *jobEntry) int {
			return cmp.Or(b.f.FinishedAt.Compare(a.f.FinishedAt), strings.Compare(a.r.Name, b.r.Name))
		})
	}
	c.mu.Unlock()
	res := protocol.HiddenJobsResult{Items: []model.Resource{}, Total: len(hidden)}
	size := 0
	i := offset
	for ; i < len(hidden) && len(res.Items) < limit; i++ {
		b, err := json.Marshal(hidden[i].r)
		if err != nil {
			continue
		}
		if size+len(b)+1 > budget && len(res.Items) > 0 {
			break
		}
		size += len(b) + 1
		res.Items = append(res.Items, hidden[i].r)
	}
	if i < len(hidden) {
		res.Next = i
	}
	return res
}

// jobRecommendation suggests how to stop finished Jobs from piling up.
func jobRecommendation(h *model.JobBuildup, prefect bool) string {
	if h.WithoutTTL == 0 {
		return "lower ttlSecondsAfterFinished on the Job template or add a cleanup policy"
	}
	fix := "set ttlSecondsAfterFinished on the Job template"
	if prefect {
		fix += " (for Prefect, in the work pool's job variables)"
	}
	allOwned := true
	for _, g := range h.Groups {
		if g.By != flux.JobGroupOwner || g.Owner == nil || g.Owner.Kind != flux.KindCronJob {
			allOwned = false
		}
	}
	if allOwned && len(h.Groups) > 0 {
		fix = "lower the CronJob's successfulJobsHistoryLimit and failedJobsHistoryLimit, or " + fix
	}
	return fix + " or add a cleanup policy"
}

// thousands formats n with comma separators: 15078 → "15,078".
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
