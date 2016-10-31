package poolmgr

import (
	"github.com/platform9/fission"

	"fmt"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	"log"
	"net/http"
	"testing"
)

func getKubeClient() *unversioned.Client {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		panic("failed loading client config")
	}
	client := unversioned.NewOrDie(config)
	return client
}

type staticHandler struct {
	resp string
}

func (s *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(s.resp))
}

// staticHttpServer starts an http server at port and responds to any
// request with the given response.  Use this to mock the controller
// raw function fetch HTTP endpoint.
func staticHttpServer(port int, response string) {
	s := &staticHandler{resp: response}
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%v", port), s))
}

func TestGenericPool(t *testing.T) {
	namespace := "fission-test"

	client := getKubeClient()

	_, err := client.Namespaces().Create(&api.Namespace{
		ObjectMeta: api.ObjectMeta{
			Name:   namespace,
			Labels: map[string]string{},
		},
	})
	if err != nil {
		log.Panicf("failed to create namespace: %v", err)
	}

	// destroys everything in the namespace
	defer client.Namespaces().Delete(namespace)

	env := &fission.Environment{
		Metadata: fission.Metadata{
			Name: "test-env",
			Uid:  "",
		},
		RunContainerImageUrl: "fission/testing",
	}

	gp, err := MakeGenericPool(client, env, 3, namespace)
	if err != nil {
		log.Panicf("failed to make generic pool: %v", err)
	}
	log.Printf("Pool created")

	// test specialization

	testFunc := `
module.exports = function (context, callback) {
    callback(200, "Hello, world!");
}
`
	go staticHttpServer(2222, testFunc)

	m := fission.Metadata{
		Name: "foo",
		Uid:  "xxx-yyy",
	}
	fsvc, err := gp.GetFuncSvc(&m)
	if err != nil {
		log.Fatalf("Error getting function svc: %v", err)
	}
	log.Printf("fsvc: %v", fsvc)
}
