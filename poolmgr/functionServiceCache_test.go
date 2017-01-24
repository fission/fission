package poolmgr

import (
	"log"
	"testing"

	"github.com/fission/fission"
	"time"
)

func TestFunctionServiceCache(t *testing.T) {
	fsc := MakeFunctionServiceCache()
	if fsc == nil {
		log.Panicf("error creating cache")
	}

	var fsvc *funcSvc
	now := time.Now()

	fsvc = &funcSvc{
		function: &fission.Metadata{
			Name: "foo",
			Uid:  "1212",
		},
		environment: &fission.Environment{
			Metadata: fission.Metadata{
				Name: "foo-env",
				Uid:  "2323",
			},
			RunContainerImageUrl: "fission/foo-env",
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
