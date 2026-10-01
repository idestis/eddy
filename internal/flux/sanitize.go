package flux

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/idestis/eddy/internal/model"
)

// Redacted replaces sensitive values in sanitized YAML.
const Redacted = "[REDACTED]"

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// ErrKindNotAllowed is returned by SanitizeYAML for Secrets.
var ErrKindNotAllowed = errors.New("flux: kind is not allowed for yaml")

// SanitizeYAML renders an object for the yaml operation, for any kind
// except Secrets (YAMLAllowed), which it refuses. On a copy of the object it:
//   - drops metadata.managedFields and the kubectl last-applied-configuration
//     annotation;
//   - drops any top-level data, stringData and binaryData, so a ConfigMap
//     (or any other kind that carries them) keeps only its metadata;
//   - replaces every container env[].value with "[REDACTED]" wherever a pod
//     spec sits: spec (Pods), spec.template.spec (Deployments, Jobs and any
//     custom workload shaped like them) and spec.jobTemplate.spec.template.spec
//     (CronJobs). env[].valueFrom is kept: it only names the Secret,
//     ConfigMap or field a value comes from;
//   - redacts known inline credentials of preset kinds: an EC2NodeClass's
//     spec.userData and the data of an External Secrets "fake" provider.
//
// The hub runs its own redactor (redact.YAML) on the result as well.
func SanitizeYAML(u *unstructured.Unstructured) ([]byte, error) {
	if !YAMLAllowed(u.GetKind()) {
		return nil, fmt.Errorf("%w: %s", ErrKindNotAllowed, u.GroupVersionKind())
	}
	obj := u.DeepCopy().Object
	stripMetadata(obj)
	delete(obj, "data")
	delete(obj, "stringData")
	delete(obj, "binaryData")
	for _, path := range podSpecPaths {
		if spec := mapping(obj, path...); spec != nil {
			redactEnv(spec)
		}
	}
	redactInline(u.GroupVersionKind().Group, u.GetKind(), obj)
	out, err := yaml.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("flux: marshal %s %s/%s: %w", u.GetKind(), u.GetNamespace(), u.GetName(), err)
	}
	return out, nil
}

// podSpecPaths are where pod specs sit in built-in and workload-shaped kinds.
var podSpecPaths = [][]string{
	{"spec"},
	{"spec", "template", "spec"},
	{"spec", "jobTemplate", "spec", "template", "spec"},
}

// redactInline replaces inline credentials that some well-known kinds carry
// in their spec.
func redactInline(group, kind string, obj map[string]any) {
	switch {
	case group == GroupKarpenterAWS && kind == KindEC2NodeClass:
		if spec := mapping(obj, "spec"); spec != nil {
			if _, ok := spec["userData"]; ok {
				spec["userData"] = Redacted
			}
		}
	case group == GroupESO && (kind == KindSecretStore || kind == KindClusterSecretStore):
		for _, d := range maps(obj, "spec", "provider", "fake", "data") {
			for _, key := range []string{"value", "valueMap"} {
				if _, ok := d[key]; ok {
					d[key] = Redacted
				}
			}
		}
	}
}

func stripMetadata(obj map[string]any) {
	meta := mapping(obj, "metadata")
	if meta == nil {
		return
	}
	delete(meta, "managedFields")
	if ann := mapping(meta, "annotations"); ann != nil {
		delete(ann, lastAppliedAnnotation)
		if len(ann) == 0 {
			delete(meta, "annotations")
		}
	}
}

// podSpec returns the pod spec of a Pod or of a workload's pod template
// (Deployments, StatefulSets, DaemonSets, ReplicaSets and Jobs, and the job
// template of a CronJob).
func podSpec(k Kind, obj map[string]any) map[string]any {
	switch {
	case k.Kind == KindPod:
		return mapping(obj, "spec")
	case k.Group == GroupApps, k.Kind == KindJob:
		return mapping(obj, "spec", "template", "spec")
	case k.Kind == KindCronJob:
		return mapping(obj, "spec", "jobTemplate", "spec", "template", "spec")
	}
	return nil
}

func redactEnv(spec map[string]any) {
	for _, key := range []string{"initContainers", "containers", "ephemeralContainers"} {
		for _, c := range maps(spec, key) {
			for _, e := range maps(c, "env") {
				if _, ok := e["value"]; ok {
					e["value"] = Redacted
				}
			}
		}
	}
}

// Trim reduces an object to what Summarize reads, for use as an informer
// transform: it drops managedFields and every annotation (a Job keeps only
// AnnotationHelmHook, which JobFactsOf groups by), pod specs other than
// container names and images, and HelmRelease values. The result must only be
// used for summaries, never for the yaml operation.
func Trim(k Kind, u *unstructured.Unstructured) {
	obj := u.Object
	if meta := mapping(obj, "metadata"); meta != nil {
		delete(meta, "managedFields")
		hook := str(meta, "annotations", AnnotationHelmHook)
		defaultClass := str(meta, "annotations", AnnotationDefaultStorageClass)
		defaultBeta := str(meta, "annotations", annotationDefaultStorageClassBeta)
		delete(meta, "annotations")
		switch {
		case k.Kind == KindJob && hook != "":
			meta["annotations"] = map[string]any{AnnotationHelmHook: OneLine(hook, 100)}
		case k.Kind == KindStorageClass && (defaultClass != "" || defaultBeta != ""):
			ann := map[string]any{}
			if defaultClass != "" {
				ann[AnnotationDefaultStorageClass] = OneLine(defaultClass, 16)
			}
			if defaultBeta != "" {
				ann[annotationDefaultStorageClassBeta] = OneLine(defaultBeta, 16)
			}
			meta["annotations"] = ann
		}
	}
	trimPreset(k, obj)
	if spec := podSpec(k, obj); spec != nil {
		var containers []any
		for _, c := range maps(spec, "containers") {
			containers = append(containers, map[string]any{"name": c["name"], "image": c["image"]})
		}
		for key := range spec {
			delete(spec, key)
		}
		if containers != nil {
			spec["containers"] = containers
		}
	}
	if k.Kind == KindHelmRelease || k.Kind == KindKustomization {
		trimDependsOn(mapping(obj, "spec"))
	}
	if k.Kind == KindHelmRelease {
		if spec := mapping(obj, "spec"); spec != nil {
			delete(spec, "values")
			delete(spec, "valuesFrom")
			delete(spec, "postRenderers")
		}
	}
	if k.Kind == KindKustomization {
		if spec := mapping(obj, "spec"); spec != nil {
			delete(spec, "patches")
			delete(spec, "postBuild")
		}
	}
}

// trimDependsOn keeps spec.dependsOn for Summarize, reduced to the name and
// namespace of at most model.MaxDependsOn entries (readyExpr is dropped).
func trimDependsOn(spec map[string]any) {
	if spec == nil {
		return
	}
	deps := maps(spec, "dependsOn")
	if len(deps) == 0 {
		delete(spec, "dependsOn")
		return
	}
	out := make([]any, 0, min(len(deps), model.MaxDependsOn))
	for _, d := range deps {
		if len(out) == model.MaxDependsOn {
			break
		}
		e := map[string]any{"name": str(d, "name")}
		if ns := str(d, "namespace"); ns != "" {
			e["namespace"] = ns
		}
		out = append(out, e)
	}
	spec["dependsOn"] = out
}

// trimPreset drops the parts of preset kinds that summaries never read and
// that may hold inline data: templates of generated Secrets, provider
// configuration of stores (only the provider name is kept) and node user
// data.
func trimPreset(k Kind, obj map[string]any) {
	spec := mapping(obj, "spec")
	if spec == nil {
		return
	}
	switch k.Kind {
	case KindExternalSecret:
		if t := mapping(spec, "target"); t != nil {
			delete(t, "template")
		}
		delete(spec, "data")
		delete(spec, "dataFrom")
	case KindClusterExternalSecret:
		if es := mapping(spec, "externalSecretSpec"); es != nil {
			if t := mapping(es, "target"); t != nil {
				delete(t, "template")
			}
			delete(es, "data")
			delete(es, "dataFrom")
		}
	case KindPushSecret:
		delete(spec, "template")
		delete(spec, "data")
	case KindSecretStore, KindClusterSecretStore:
		if p := mapping(spec, "provider"); p != nil {
			for name := range p {
				p[name] = map[string]any{}
			}
		}
	case KindEC2NodeClass:
		delete(spec, "userData")
		delete(spec, "blockDeviceMappings")
		delete(spec, "tags")
	}
}
