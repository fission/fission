package storagesvc

import (
	"github.com/fission/fission/crd"
)

type CRDClient struct {
	client crd.FissionClient
}

func MakeCRDClient() *CRDClient {
	fissionClient, _, _, err := crd.MakeFissionClient()
	if err != nil {
		return nil
	}
	return &CRDClient{client: fissionClient}
}

func (cc *CRDClient) getPkgList() {
	cc.client.Packages().List()
}

func (cc *CRDClient) getFunctionList() {

}

