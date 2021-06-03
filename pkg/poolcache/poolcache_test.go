package poolcache

import (
	"log"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func checkErr(err error) {
	if err != nil {
		log.Panicf("err: %v", err)
	}
}

func TestPoolCache(t *testing.T) {
	c := NewPoolCache()

	c.SetValue("func", "ip", "value", resource.MustParse("45m"))

	c.SetValue("func2", "ip2", "value2", resource.MustParse("50m"))

	c.SetValue("func2", "ip22", "value22", resource.MustParse("33m"))

	checkErr(c.DeleteValue("func2", "ip2"))

	cc := c.ListAvailableValue()
	if len(cc) != 0 {
		log.Panicf("expected 0 available items")
	}

	c.MarkAvailable("func", "ip")

	_, active, err := c.GetValue("func", 5)
	if active != 1 {
		log.Panicln("Expected 1 active, found", active)
	}
	checkErr(err)

	checkErr(c.DeleteValue("func", "ip"))

	_, active, err = c.GetValue("func", 5)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetValue("cpulimit", "100", "value", resource.MustParse("3m"))
	c.SetCPUUtilization("cpulimit", "100", resource.MustParse("4m"))

	_, _, err = c.GetValue("cpulimit", 5)

	if err == nil {
		log.Panicf("received pod address with higher CPU usage than limit")
	}
	c.SetCPUUtilization("cpulimit", "100", resource.MustParse("2m"))
	_, _, err = c.GetValue("cpulimit", 5)
	checkErr(err)
}
