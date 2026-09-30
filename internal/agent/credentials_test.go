package agent

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCredentialsJoinAndStore(t *testing.T) {
	kube := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "eddy-system", Name: "eddy-agent-token"},
		Data:       map[string][]byte{"joinToken": []byte("eddy_join_x")},
	})
	store := kubeSecretStore{kube: kube, namespace: "eddy-system", name: "eddy-agent-token", key: "token"}
	c := NewCredentials("", "eddy_join_x", store)
	if !c.Joining() || c.Current() != "eddy_join_x" {
		t.Fatal("starts with the join token")
	}
	if changed, err := c.Refresh(t.Context()); err != nil || changed {
		t.Fatalf("refresh with no token: %v %v", changed, err)
	}
	if res := c.Accept(t.Context(), "eddy_join_nope"); res.Stored || res.Error == "" {
		t.Fatal("a join token is not a permanent token")
	}
	if res := c.Accept(t.Context(), "permanent-1"); !res.Stored || c.Joining() || c.Current() != "permanent-1" {
		t.Fatalf("accept: %+v", res)
	}
	sec, _ := kube.CoreV1().Secrets("eddy-system").Get(t.Context(), "eddy-agent-token", metav1.GetOptions{})
	if string(sec.Data["token"]) != "permanent-1" || string(sec.Data["joinToken"]) != "eddy_join_x" {
		t.Fatalf("secret %v", sec.Data)
	}

	// A second replica with the same join token picks the stored token up.
	c2 := NewCredentials("", "eddy_join_x", store)
	if changed, err := c2.Refresh(t.Context()); err != nil || !changed || c2.Current() != "permanent-1" {
		t.Fatalf("replica refresh: %v %v %q", changed, err, c2.Current())
	}
}

type failingStore struct{}

func (failingStore) Load(context.Context) (string, error) { return "", errors.New("forbidden") }
func (failingStore) Save(context.Context, string) error   { return errors.New("secrets is forbidden") }

func TestCredentialsKeepTokenInMemoryWhenStoreFails(t *testing.T) {
	c := NewCredentials("", "eddy_join_x", failingStore{})
	res := c.Accept(t.Context(), "permanent-1")
	if res.Stored || res.Error == "" || c.Current() != "permanent-1" || c.LastError() == "" {
		t.Fatalf("accept %+v, current %q", res, c.Current())
	}
	if NewCredentials("tok", "", nil).Joining() {
		t.Fatal("a permanent token is not joining")
	}
}
