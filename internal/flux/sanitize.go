package flux

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Redacted replaces sensitive values in sanitized YAML.
const Redacted = "[REDACTED]"

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// ErrKindNotAllowed is returned by SanitizeYAML for kinds outside the table.
var ErrKindNotAllowed = errors.New("flux: kind is not allowed for yaml")

// SanitizeYAML renders an object for the yaml operation. It refuses kinds for
// which YAMLAllowed is false and, on a copy of the object:
//   - drops metadata.managedFields and the kubectl last-applied-configuration
//     annotation;
//   - drops any top-level data, stringData and binaryData;
//   - replaces every container env[].value with "[REDACTED]" in Pods and in
//     workload pod templates. env[].valueFrom is kept: it only names the
//     Secret, ConfigMap or field a value comes from.
func SanitizeYAML(u *unstructured.Unstructured) ([]byte, error) {
	k, ok := KindByName(u.GetKind())
	if !ok || u.GetKind() != k.Kind || u.GroupVersionKind().Group != k.Group {
		return nil, fmt.Errorf("%w: %s", ErrKindNotAllowed, u.GroupVersionKind())
	}
	obj := u.DeepCopy().Object
	stripMetadata(obj)
	delete(obj, "data")
	delete(obj, "stringData")
	delete(obj, "binaryData")
	if spec := podSpec(k, obj); spec != nil {
		redactEnv(spec)
	}
	out, err := yaml.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("flux: marshal %s %s/%s: %w", k.Kind, u.GetNamespace(), u.GetName(), err)
	}
	return out, nil
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

// podSpec returns the pod spec of a Pod or of a workload's pod template.
func podSpec(k Kind, obj map[string]any) map[string]any {
	switch {
	case k.Kind == KindPod:
		return mapping(obj, "spec")
	case k.Group == GroupApps:
		return mapping(obj, "spec", "template", "spec")
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
// transform: it drops managedFields and every annotation, pod specs other than
// container names and images, and HelmRelease values. The result must only be
// used for summaries, never for the yaml operation.
func Trim(k Kind, u *unstructured.Unstructured) {
	obj := u.Object
	if meta := mapping(obj, "metadata"); meta != nil {
		delete(meta, "managedFields")
		delete(meta, "annotations")
	}
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
