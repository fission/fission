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

func (cc *CRDClient) getPkgList() (*crd.Package, error){
	pkgList, err := cc.client.Packages().List()
	if err != nil {
		return nil, err
	}
	return pkgList.Items, nil
}

func (cc *CRDClient) getFunctionList() {

}

func (cc *CRDClient) getPackageFromFunction(funcName string) (pkgName string, err error) {


}