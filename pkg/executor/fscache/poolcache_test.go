package fscache

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/fission/fission/pkg/utils/loggerfactory"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func checkErr(err error) {
	if err != nil {
		log.Panicf("err: %v", err)
	}
}

func TestPoolCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)

	c.SetSvcValue(ctx, "func", "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10)

	c.SetSvcValue(ctx, "func2", "ip2", &FuncSvc{
		Name: "value2",
	}, resource.MustParse("50m"), 10)

	c.SetSvcValue(ctx, "func2", "ip22", &FuncSvc{
		Name: "value22",
	}, resource.MustParse("33m"), 10)

	checkErr(c.DeleteValue(ctx, "func2", "ip2"))

	cc := c.ListAvailableValue()
	if len(cc) != 0 {
		log.Panicf("expected 0 available items")
	}

	c.MarkAvailable("func", "ip")
	concurrency := 10
	_, err := c.GetSvcValue(ctx, "func", 5, concurrency)

	checkErr(err)

	checkErr(c.DeleteValue(ctx, "func", "ip"))

	_, err = c.GetSvcValue(ctx, "func", 5, concurrency)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetSvcValue(ctx, "cpulimit", "100", &FuncSvc{
		Name: "value",
	}, resource.MustParse("3m"), 10)
	c.SetCPUUtilization("cpulimit", "100", resource.MustParse("4m"))
}

func TestGetSvcValueWhenNoFuncSvcGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)

	functionName := "no_func"
	expectedError := "Resource not found - function Name 'no_func' not found"

	value, err := c.GetSvcValue(ctx, functionName, 5, 1)

	if value != nil {
		t.Error("expected value to be nil")
	}

	if err == nil {
		t.Error("expected GetSvcValue to return an error")
	}

	if err.Error() != expectedError {
		t.Errorf("expected error to match %s, got %s", expectedError, err.Error())
	}
}

func TestGetSvcValueWhenResourceFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)

	functionName := "test"
	addr := "testAddr"
	c.cache[functionName] = NewFuncSvcGroup()
	c.cache[functionName].svcs[addr] = &funcSvcInfo{}
	c.cache[functionName].svcs[addr].val = &FuncSvc{
		Name:              functionName,
		Function:          &metav1.ObjectMeta{},
		Environment:       &fv1.Environment{},
		Address:           addr,
		KubernetesObjects: []apiv1.ObjectReference{},
		Executor:          "",
		CPULimit:          resource.Quantity{},
		Ctime:             time.Time{},
		Atime:             time.Time{},
	}

	c.cache[functionName].svcs[addr].activeRequests = 1
	c.cache[functionName].svcs[addr].cpuLimit = resource.MustParse("4m")

	value, err := c.GetSvcValue(ctx, functionName, 5, 1)

	if value == nil {
		t.Error("expected value to not be nil")
	}

	if value.Name != functionName {
		t.Errorf("expected value.Name to be %s, got %s", functionName, value.Name)
	}
	if err != nil {
		t.Error("expected GetSvcValue to not return any error")
	}
}

func TestGetSvcValueWhenConcurrencyLimitReached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)

	functionName := "test"
	addr := "testAddr"
	c.cache[functionName] = NewFuncSvcGroup()
	c.cache[functionName].svcs[addr] = &funcSvcInfo{}
	c.cache[functionName].svcs[addr].val = &FuncSvc{
		Name:              functionName,
		Function:          &metav1.ObjectMeta{},
		Environment:       &fv1.Environment{},
		Address:           addr,
		KubernetesObjects: []apiv1.ObjectReference{},
		Executor:          "",
		CPULimit:          resource.Quantity{},
		Ctime:             time.Time{},
		Atime:             time.Time{},
	}

	c.cache[functionName].svcs[addr].activeRequests = 5
	c.cache[functionName].svcs[addr].cpuLimit = resource.MustParse("4m")

	value, err := c.GetSvcValue(ctx, functionName, 5, 1)

	if value != nil {
		t.Error("expected value to be nil")
	}

	if err == nil {
		t.Error("expected GetSvcValue to return an error")
	}
}

func TestGetSvcValueWhenRPPLimitReachedButNotConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)

	functionName := "test"
	addr := "testAddr"
	c.cache[functionName] = NewFuncSvcGroup()
	c.cache[functionName].svcs[addr] = &funcSvcInfo{}
	c.cache[functionName].svcs[addr].val = &FuncSvc{
		Name:              functionName,
		Function:          &metav1.ObjectMeta{},
		Environment:       &fv1.Environment{},
		Address:           addr,
		KubernetesObjects: []apiv1.ObjectReference{},
		Executor:          "",
		CPULimit:          resource.Quantity{},
		Ctime:             time.Time{},
		Atime:             time.Time{},
	}

	c.cache[functionName].svcs[addr].activeRequests = 5
	c.cache[functionName].svcs[addr].cpuLimit = resource.MustParse("4m")

	value, err := c.GetSvcValue(ctx, functionName, 5, 2)

	if value != nil {
		t.Error("expected value to be nil")
	}

	if c.cache[functionName].svcWaiting != 1 {
		t.Errorf("expected svcWaitin to be equal to 1, got %d", c.cache[functionName].svcWaiting)
	}

	if err == nil {
		t.Error("expected GetSvcValue to return an error")
	}
}

func TestSetValueWhen0SvcWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)

	functionName := "test"
	addr := "testAddr"
	funcSvc := &FuncSvc{
		Name:              functionName,
		Function:          &metav1.ObjectMeta{},
		Environment:       &fv1.Environment{},
		Address:           addr,
		KubernetesObjects: []apiv1.ObjectReference{},
		Executor:          "",
		CPULimit:          resource.Quantity{},
		Ctime:             time.Time{},
		Atime:             time.Time{},
	}
	c.SetSvcValue(ctx, functionName, addr, funcSvc, resource.MustParse("10m"), 1)

	if c.cache[functionName].svcWaiting != 0 {
		t.Errorf("expected svcWaiting to be 0, but got %d", c.cache[functionName].svcWaiting)
	}

	if c.cache[functionName].svcs[addr].activeRequests != 1 {
		t.Errorf("expected active requests to be 1, but got %d", c.cache[functionName].svcs[addr].activeRequests)
	}
}

func TestSetValueWhen2SvcWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)

	functionName := "test"
	addr := "testAddr"
	funcSvc := &FuncSvc{
		Name:              functionName,
		Function:          &metav1.ObjectMeta{},
		Environment:       &fv1.Environment{},
		Address:           addr,
		KubernetesObjects: []apiv1.ObjectReference{},
		Executor:          "",
		CPULimit:          resource.Quantity{},
		Ctime:             time.Time{},
		Atime:             time.Time{},
	}
	c.cache[functionName] = NewFuncSvcGroup()
	c.cache[functionName].svcWaiting = 3
	svcChan1 := make(chan *FuncSvc)
	svcChan2 := make(chan *FuncSvc)
	c.cache[functionName].queue.Push(&svcWait{
		svcChannel: svcChan1,
		ctx:        ctx,
	})
	c.cache[functionName].queue.Push(&svcWait{
		svcChannel: svcChan2,
		ctx:        ctx,
	})

	c.SetSvcValue(ctx, functionName, addr, funcSvc, resource.MustParse("50m"), 5)
	<-svcChan1
	<-svcChan2
	if c.cache[functionName].svcWaiting != 0 {
		t.Errorf("expected svcWaiting to be 0, but got %d", c.cache[functionName].svcWaiting)
	}

	if c.cache[functionName].svcs[addr].activeRequests != 3 {
		t.Errorf("expected active requests to be 3, but got %d", c.cache[functionName].svcs[addr].activeRequests)
	}
}
