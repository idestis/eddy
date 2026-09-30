package flux

import (
	"fmt"
	"strings"

	"github.com/eddy-gitops/eddy/internal/model"
)

// ParseInventoryID parses a Kustomization status.inventory.entries[].id,
// which Flux formats as "<namespace>_<name>_<group>_<kind>". The namespace is
// empty for cluster-scoped objects and the group is empty for the core API.
//
// Namespaces and API groups are DNS names and Kinds are identifiers, so none
// of them contains "_". Some object names (RBAC roles, for example) may, so
// everything between the first and the last two separators is the name.
func ParseInventoryID(id string) (model.Ref, error) {
	p := strings.Split(id, "_")
	if len(p) < 4 {
		return model.Ref{}, fmt.Errorf("flux: invalid inventory id %q", id)
	}
	ref := model.Ref{
		Namespace: p[0],
		Name:      strings.Join(p[1:len(p)-2], "_"),
		Group:     p[len(p)-2],
		Kind:      p[len(p)-1],
	}
	if ref.Name == "" || ref.Kind == "" {
		return model.Ref{}, fmt.Errorf("flux: invalid inventory id %q", id)
	}
	return ref, nil
}
