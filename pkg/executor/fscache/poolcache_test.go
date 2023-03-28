package fscache

import (
	"context"
	"log"
	"reflect"
	"testing"

	fuzz "github.com/AdaLogics/go-fuzz-headers"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/fission/fission/pkg/utils/loggerfactory"
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
	concurrency := 5
	requestsPerPod := 2

	// should return err since no svc is present
	_, err := c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found value when expected it to be nil")
	}

	c.SetSvcValue(ctx, "func", "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10)

	// should not return any error since we added a svc
	_, err = c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	checkErr(err)

	c.SetSvcValue(ctx, "func", "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10)

	// should return err since all functions are busy
	_, err = c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found value when expected it to be nil")
	}

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

	checkErr(c.DeleteValue(ctx, "func", "ip"))

	_, err = c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetSvcValue(ctx, "cpulimit", "100", &FuncSvc{
		Name: "value",
	}, resource.MustParse("3m"), 10)
	c.SetCPUUtilization("cpulimit", "100", resource.MustParse("4m"))
}

func FuzzGetSvcValueAndSetSvcValue(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		f := fuzz.NewConsumer(data)
		function, err := f.GetString()
		if err != nil {
			return
		}
		address, err := f.GetString()
		if err != nil {
			return
		}
		rpp, err := f.GetInt()
		if err != nil {
			return
		}
		concurrency, err := f.GetInt()
		if err != nil {
			return
		}
		funcSvc := &FuncSvc{}
		err = f.GenerateStruct(funcSvc)
		if err != nil {
			return
		}
		resource := resource.Quantity{}
		err = f.GenerateStruct(resource)
		if err != nil {
			return
		}
		p := NewPoolCache(loggerfactory.GetLogger())
		val, err := p.GetSvcValue(context.Background(), function, rpp, concurrency)
		if err != nil {
			return
		}
		p.SetSvcValue(context.Background(), function, address, funcSvc, resource, rpp)
		if !reflect.DeepEqual(val, funcSvc) {
			t.Logf("val: %+v", val)
			t.Logf("funcSvc: %+v", funcSvc)
			t.Fatalf("Got wrong value")
		}
	})
}
