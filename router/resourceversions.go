package router

/*

Warning: This stuff is wrong in theory but works in practice.  Don't
use it for anything other than test automation.

The idea is:

1. We need some way for test scripts to wait long enough for the
router controllers to have caught up with the CRDs.

2. "resourceVersion" is a monotonically increasing number in practice,
since it comes from etcd's raft index.  We aren't supposed to use this
information; the Kubernetes API docs are clear that resourceVersion is
to be treated as an opaque string that isn't even necessarily a
number.  So we're violating some abstraction layers here for test
efficiency.

*/

import (
	"fmt"
	"strconv"
	"sync"
)

type (
	ResourceVersionMonitor struct {
		mutex  sync.Mutex
		latest uint64
	}
)

func (rvm *ResourceVersionMonitor) Update(rv string) error {
	rvm.mutex.Lock()
	defer rvm.mutex.Unlock()

	var rvNumber uint64
	rvNumber, err := strconv.ParseUint(rv, 10, 64)
	if err != nil {
		return err
	}
	if rvNumber > rvm.latest {
		rvm.latest = rvNumber
	}
	return nil
}

func (rvm *ResourceVersionMonitor) Get() string {
	rvm.mutex.Lock()
	defer rvm.mutex.Unlock()

	return fmt.Sprintf("%v", rvm.latest)
}
