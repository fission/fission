package fscache

import (
	"context"
	"log"
	"testing"

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

	c.SetSvcValue(ctx, "func", "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"))

	c.SetSvcValue(ctx, "func2", "ip2", &FuncSvc{
		Name: "value2",
	}, resource.MustParse("50m"))

	c.SetSvcValue(ctx, "func2", "ip22", &FuncSvc{
		Name: "value22",
	}, resource.MustParse("33m"))

	checkErr(c.DeleteValue(ctx, "func2", "ip2"))

	cc := c.ListAvailableValue()
	if len(cc) != 0 {
		log.Panicf("expected 0 available items")
	}

	c.MarkAvailable("func", "ip")

	_, active, err := c.GetSvcValue(ctx, "func", 5)
	if active != 1 {
		log.Panicln("Expected 1 active, found", active)
	}
	checkErr(err)

	checkErr(c.DeleteValue(ctx, "func", "ip"))

	_, _, err = c.GetSvcValue(ctx, "func", 5)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetSvcValue(ctx, "cpulimit", "100", &FuncSvc{
		Name: "value",
	}, resource.MustParse("3m"))
	c.SetCPUUtilization("cpulimit", "100", resource.MustParse("4m"))

	_, _, err = c.GetSvcValue(ctx, "cpulimit", 5)

	if err == nil {
		log.Panicf("received pod address with higher CPU usage than limit")
	}
	c.SetCPUUtilization("cpulimit", "100", resource.MustParse("2m"))
	_, _, err = c.GetSvcValue(ctx, "cpulimit", 5)
	checkErr(err)
}
