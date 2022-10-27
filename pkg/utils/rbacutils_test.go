package utils

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const (
	namespace      string = "testns"
	serviceAccount string = "testSA"
	clusterRole    string = "testClusterRole"
	rolebinding    string = "testRolebinding"
)

func TestSetupRoleBinding(t *testing.T) {
	ctx := context.Background()
	logger := loggerfactory.GetLogger()
	kubernetesClient := fake.NewSimpleClientset()

	//case 1 => when role binding doesn't exists
	_, err := createServiceAccount(ctx, kubernetesClient)
	if err != nil {
		t.Fatalf("Error creating service account: %s", err.Error())
	}
	_, err = createClusterRole(ctx, clusterRole, kubernetesClient)
	if err != nil {
		t.Fatalf("Error creating cluster role: %s", err.Error())
	}
	err = SetupRoleBinding(ctx, logger, kubernetesClient, rolebinding, namespace, clusterRole, fv1.ClusterRole, serviceAccount, namespace)
	assert.Nil(t, err, "error should be nil and new role binding will get created")

	//case 2 => rolebinding exists but service account doesn't exists
	err = kubernetesClient.CoreV1().ServiceAccounts(namespace).Delete(ctx, serviceAccount, metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Error deleting service account: %s", err.Error())
	}
	err = SetupRoleBinding(ctx, logger, kubernetesClient, rolebinding, namespace, clusterRole, fv1.ClusterRole, serviceAccount, namespace)
	assert.Nil(t, err, "error should be nil and service account should add in rolebinding")

	//case 3 => rolebinding, cluster role and service account, all exists
	err = SetupRoleBinding(ctx, logger, kubernetesClient, rolebinding, namespace, clusterRole, fv1.ClusterRole, serviceAccount, namespace)
	assert.Nil(t, err, "error should be nil and nothing to add")

	//case 4 => This must fail, if there is change in cluster-role-name
	err = SetupRoleBinding(ctx, logger, kubernetesClient, rolebinding, namespace, "invalid-cluster-name", fv1.ClusterRole, serviceAccount, namespace)
	assert.NotNil(t, err)
	assert.Equal(t, err.Error(), fmt.Sprintf("rolebinding %s in namespace %s exists with different roleref, retry by deleting existing rolebinding", rolebinding, namespace))
}

func createClusterRole(ctx context.Context, clusterRole string, kubernetesClient *fake.Clientset) (*v1.ClusterRole, error) {
	objRole := MakeClusterRoleObj(clusterRole)
	var err error
	objRole, err = kubernetesClient.RbacV1().ClusterRoles().Create(ctx, objRole, metav1.CreateOptions{})
	return objRole, err
}

func createServiceAccount(ctx context.Context, kubernetesClient *fake.Clientset) (*corev1.ServiceAccount, error) {
	objSA := MakeSAObj(serviceAccount, namespace)
	var err error
	objSA, err = kubernetesClient.CoreV1().ServiceAccounts(namespace).Create(ctx, objSA, metav1.CreateOptions{})
	return objSA, err
}

// MakeClusterRoleObj returns a ClusterRole object
func MakeClusterRoleObj(clusterRoleName string) *v1.ClusterRole {
	return &v1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleName,
		},
	}
}
