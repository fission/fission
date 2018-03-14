package crd

import (
	"log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
)

func GetFunctionsByEnv(fissionClient *FissionClient, envName string, envNamespace string) ([]metav1.ObjectMeta, error) {
	poolMgrfnList := make([]metav1.ObjectMeta, 0)
	fnList, err := fissionClient.Functions(metav1.NamespaceAll).List(metav1.ListOptions{})
	if err != nil {
		log.Printf("Error fetching function list across all namespaces")
		return poolMgrfnList, err
	}

	for _, item := range fnList.Items {
		if item.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fission.ExecutorTypePoolmgr &&
			item.Spec.Environment.Name == envName &&
			item.Spec.Environment.Namespace == envNamespace {
			poolMgrfnList = append(poolMgrfnList, item.Metadata)
		}
	}

	return poolMgrfnList, nil
}
