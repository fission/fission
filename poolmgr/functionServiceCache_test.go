package poolmgr

import (
	"log"
	"testing"
	"time"

	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/tpr"
)

func TestFunctionServiceCache(t *testing.T) {
	fsc := MakeFunctionServiceCache()
	if fsc == nil {
		log.Panicf("error creating cache")
	}

	var fsvc *funcSvc
	now := time.Now()

	fsvc = &funcSvc{
		function: &api.ObjectMeta{
			Name: "foo",
			UID:  "1212",
		},
		environment: &tpr.Environment{
			Metadata: api.ObjectMeta{
				Name: "foo-env",
				UID:  "2323",
			},
			Spec: fission.EnvironmentSpec{
				Version: 1,
				Runtime: fission.Runtime{
					Image: "fission/foo-env",
				},
				Builder: fission.Builder{},
			},
		},
		address: "xxx",
		podName: "yyy",
		ctime:   now,
		atime:   now,
	}
	err, _ := fsc.Add(*fsvc)
	if err != nil {
		fsc.Log()
		log.Panicf("Failed to add fsvc: %v", err)
	}

	f, err := fsc.GetByFunction(fsvc.function)
	if err != nil {
		fsc.Log()
		log.Panicf("Failed to get fsvc: %v", err)
	}
	fsvc.atime = f.atime
	fsvc.ctime = f.ctime
	if *f != *fsvc {
		fsc.Log()
		log.Panicf("Incorrect fsvc \n(expected: %#v)\n (found: %#v)", fsvc, f)
	}

	err = fsc.TouchByAddress(fsvc.address)
	if err != nil {
		fsc.Log()
		log.Panicf("Failed to touch fsvc: %v", err)
	}

	deleted, err := fsc.DeleteByPod(fsvc.podName, 0)
	if err != nil {
		fsc.Log()
		log.Panicf("Failed to delete fsvc: %v", err)
	}
	if !deleted {
		fsc.Log()
		log.Panicf("Did not delete fsvc")
	}

	_, err = fsc.GetByFunction(fsvc.function)
	if err == nil {
		fsc.Log()
		log.Panicf("found fsvc while expecting empty cache: %v", err)
	}
}
