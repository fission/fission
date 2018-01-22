package storagesvc

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/fission/fission/crd"
)

type CRDClient struct {
	client *crd.FissionClient
}

func MakeCRDClient() *CRDClient {
	fissionClient, _, _, err := crd.MakeFissionClient()
	if err != nil {
		return nil
	}
	return &CRDClient{client: fissionClient}
}

// This method fetches the pkg list from kubernetes.
// More methods can be added here as needed.
func (cc *CRDClient) getPkgList() ([]crd.Package, error){
	pkgList, err := cc.client.Packages(metav1.NamespaceAll).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	return pkgList.Items, nil
}