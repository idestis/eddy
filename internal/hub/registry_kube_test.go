package hub

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseCluster(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "prod-eu"},
		"spec": map[string]any{
			"environment": "Production", "region": "eu-central-1", "color": "#C2410C",
			"protected": true, "order": int64(3),
			"agentTokenSecretRef": map[string]any{"name": "eddy-agent-prod-eu"},
		},
	}}
	spec, secret, key := parseCluster(u)
	want := ClusterSpec{Name: "prod-eu", DisplayName: "prod-eu", Environment: "Production", Region: "eu-central-1", Color: "#C2410C", Protected: true, Order: 3}
	if spec != want || secret != "eddy-agent-prod-eu" || key != "token" {
		t.Fatalf("parseCluster = %+v %q %q", spec, secret, key)
	}
}

func TestHashSecretKeepsNoValues(t *testing.T) {
	in := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "eddy", Annotations: map[string]string{
			"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"token":"c2VjcmV0"}}`,
		}},
		Data: map[string][]byte{"token": []byte("secret\n"), "previousToken": []byte("old")},
	}
	out, err := hashSecret(in)
	if err != nil {
		t.Fatal(err)
	}
	s := out.(*corev1.Secret)
	if len(s.Annotations) != 1 || s.Annotations[hashedAnnotation] == "" {
		t.Fatalf("annotations = %v, want only the hashed marker", s.Annotations)
	}
	if h := sha256.Sum256([]byte("secret")); string(s.Data["token"]) != string(h[:]) {
		t.Fatal("token not replaced by its sha256")
	}
	if h := sha256.Sum256([]byte("old")); string(s.Data["previousToken"]) != string(h[:]) {
		t.Fatal("previousToken not hashed")
	}
}

// client-go may transform an object twice (watch-list initial sync, then
// Replace); a second pass must not hash the hashes.
func TestHashSecretIsIdempotent(t *testing.T) {
	in := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "eddy"},
		Data:       map[string][]byte{"token": []byte("secret")},
	}
	once, err := hashSecret(in)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := hashSecret(once)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("secret"))
	if got := twice.(*corev1.Secret).Data["token"]; string(got) != string(h[:]) {
		t.Fatal("a second transform changed the stored hash")
	}
}

func TestStatusWriterThrottles(t *testing.T) {
	var mu sync.Mutex
	var patches []clusterStatus
	want := map[string]clusterStatus{"dev": {Phase: "Connected", Resources: 1}}
	w := newStatusWriter(func() map[string]clusterStatus {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]clusterStatus{}
		for k, v := range want {
			out[k] = v
		}
		return out
	}, func(_ context.Context, _ string, st clusterStatus) error {
		patches = append(patches, st)
		return nil
	}, quietLog())
	now := time.Unix(1000, 0)
	w.now = func() time.Time { return now }
	set := func(st clusterStatus) {
		mu.Lock()
		want["dev"] = st
		mu.Unlock()
	}

	w.pass(context.Background())
	set(clusterStatus{Phase: "Connected", Resources: 2}) // count change: throttled
	now = now.Add(5 * time.Second)
	w.pass(context.Background())
	if len(patches) != 1 {
		t.Fatalf("patches %d, want 1 (count changes wait %s)", len(patches), statusMinInterval)
	}
	set(clusterStatus{Phase: "Disconnected", Resources: 2}) // phase change: immediate
	w.pass(context.Background())
	if len(patches) != 2 || patches[1].Phase != "Disconnected" {
		t.Fatalf("patches %+v", patches)
	}
	w.pass(context.Background())
	if len(patches) != 2 {
		t.Fatal("unchanged status patched again")
	}
	set(clusterStatus{Phase: "Disconnected", Resources: 5})
	now = now.Add(statusMinInterval)
	w.pass(context.Background())
	if len(patches) != 3 {
		t.Fatal("throttled change never written")
	}
}
