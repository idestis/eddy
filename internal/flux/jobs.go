package flux

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/idestis/eddy/internal/model"
)

// AnnotationHelmHook marks a Helm hook object. Trim keeps it on Jobs so hook
// Jobs can be grouped by their release.
const AnnotationHelmHook = "helm.sh/hook"

// Well-known labels that group Jobs, in the order JobFactsOf tries them.
const (
	LabelCronJobName       = "batch.kubernetes.io/cronjob-name"
	LabelPrefectDeployment = "prefect.io/deployment-name"
	LabelPrefectWorkPool   = "prefect.io/work-pool-name"
	LabelAppInstance       = "app.kubernetes.io/instance"
	LabelAppName           = "app.kubernetes.io/name"
	LabelHelmChart         = "helm.sh/chart"
)

// Ways a Job group is found (model.JobGroup.By).
const (
	JobGroupOwner        = "owner"
	JobGroupLabel        = "label"
	JobGroupGenerateName = "generateName"
	JobGroupPrefix       = "prefix"
	JobGroupNamespace    = "namespace"
)

// JobGroupKey identifies the group a Job belongs to for the agent's Job
// history policy.
type JobGroupKey struct {
	By    string
	Name  string
	Label string
	Owner *model.Ref
}

// Key is a map key unique within a namespace.
func (k JobGroupKey) Key() string { return k.By + "\x00" + k.Name }

// JobFacts is what the agent's Job policy reads from a (trimmed) Job.
type JobFacts struct {
	// Finished is set once the Complete or Failed condition is True.
	Finished bool
	Failed   bool
	// FinishedAt is the transition time of that condition, else
	// status.completionTime, else the creation time.
	FinishedAt time.Time
	// HasTTL reports spec.ttlSecondsAfterFinished.
	HasTTL bool
	// Owned reports any ownerReference (a CronJob, a workflow controller):
	// something other than a TTL may clean the Job up.
	Owned bool
	Group JobGroupKey
}

// JobFactsOf reads the facts of a Job.
func JobFactsOf(u *unstructured.Unstructured) JobFacts {
	obj := u.Object
	f := JobFacts{Owned: len(u.GetOwnerReferences()) > 0, Group: jobGroup(u)}
	_, f.HasTTL = integer(obj, "spec", "ttlSecondsAfterFinished")
	for _, c := range maps(obj, "status", "conditions") {
		typ := str(c, "type")
		if (typ != "Complete" && typ != "Failed") || str(c, "status") != "True" {
			continue
		}
		f.Finished = true
		f.Failed = f.Failed || typ == "Failed"
		if t := timestamp(c, "lastTransitionTime"); t.After(f.FinishedAt) {
			f.FinishedAt = t
		}
	}
	if f.Finished && f.FinishedAt.IsZero() {
		f.FinishedAt = timestamp(obj, "status", "completionTime")
	}
	if f.Finished && f.FinishedAt.IsZero() {
		f.FinishedAt = u.GetCreationTimestamp().Time
	}
	f.FinishedAt = f.FinishedAt.UTC()
	return f
}

// jobGroup applies the grouping precedence: the controlling owner, then
// well-known grouping labels, then metadata.generateName, then the name with
// a random or numeric suffix stripped, then the namespace.
func jobGroup(u *unstructured.Unstructured) JobGroupKey {
	if o := controllerRef(u); o != nil {
		return JobGroupKey{By: JobGroupOwner, Name: o.Kind + "/" + o.Name, Label: o.Kind + " " + o.Name, Owner: o}
	}
	l := u.GetLabels()
	label := func(key, human string) (JobGroupKey, bool) {
		v := OneLine(l[key], 63)
		if v == "" {
			return JobGroupKey{}, false
		}
		return JobGroupKey{By: JobGroupLabel, Name: key + "=" + v, Label: human + " " + v}, true
	}
	if g, ok := label(LabelCronJobName, "CronJob"); ok {
		return g
	}
	if g, ok := label(LabelPrefectDeployment, "prefect deployment"); ok {
		return g
	}
	if g, ok := label(LabelPrefectWorkPool, "prefect work pool"); ok {
		return g
	}
	if u.GetAnnotations()[AnnotationHelmHook] != "" {
		if g, ok := label(LabelAppInstance, "helm hooks of"); ok {
			return g
		}
		if g, ok := label(LabelHelmChart, "helm hooks of chart"); ok {
			return g
		}
	}
	if inst, name := OneLine(l[LabelAppInstance], 63), OneLine(l[LabelAppName], 63); inst != "" && name != "" {
		return JobGroupKey{By: JobGroupLabel, Name: LabelAppInstance + "=" + inst + "," + LabelAppName + "=" + name, Label: "app " + name + " (" + inst + ")"}
	}
	if g, ok := label(LabelAppName, "app"); ok {
		return g
	}
	if g, ok := label(LabelHelmChart, "chart"); ok {
		return g
	}
	if gen := OneLine(u.GetGenerateName(), 253); gen != "" {
		return JobGroupKey{By: JobGroupGenerateName, Name: gen, Label: "generateName " + gen}
	}
	if p, ok := JobNamePrefix(u.GetName()); ok {
		return JobGroupKey{By: JobGroupPrefix, Name: p, Label: "name prefix " + p}
	}
	return JobGroupKey{By: JobGroupNamespace, Label: "other Jobs in " + u.GetNamespace()}
}

// JobNamePrefix strips a trailing "-<suffix>" from a Job name when the
// suffix looks generated: all digits (a CronJob's schedule time, a build
// number) or 4 to 16 lowercase letters and digits with at least one of each
// (a random or hash suffix). Word-like names such as Prefect's "hasty-cougar"
// have no such suffix and report false.
func JobNamePrefix(name string) (string, bool) {
	i := strings.LastIndexByte(name, '-')
	if i <= 0 || i == len(name)-1 {
		return "", false
	}
	suffix := name[i+1:]
	digits, letters := 0, 0
	for _, r := range suffix {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z':
			letters++
		default:
			return "", false
		}
	}
	generated := letters == 0 || (len(suffix) >= 4 && len(suffix) <= 16 && digits > 0)
	if !generated {
		return "", false
	}
	return name[:i], true
}
