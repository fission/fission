package utils

import (
	"context"
	"os"
	"regexp"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/manager"
)

func TestServiceAccountCheck(t *testing.T) {
	mgr := manager.New()
	defer mgr.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kubernetesClient := fake.NewSimpleClientset()
	logger := loggerfactory.GetLogger()
	os.Setenv(ENV_CREATE_SA, "true")
	CreateMissingPermissionForSA(ctx, kubernetesClient, logger, "default")

	// Get rolebinding for a service account
	rolebindings, err := kubernetesClient.RbacV1().RoleBindings("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rolebindings.Items) != 2 {
		t.Fatal("Rolebinding not created", len(rolebindings.Items))
	}
	regexp := regexp.MustCompile(`fission\-(fetcher|builder)\-rolebinding\-[a-z0-9]{6}`)
	for _, rolebinding := range rolebindings.Items {
		if !regexp.Match([]byte(rolebinding.Name)) {
			t.Fatal("Rolebinding not created for fission-builder or fission-fetcher", rolebinding.Name)
		}
	}
}
