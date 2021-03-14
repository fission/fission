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

	c.SetValue("func", "ip", "value")

	c.SetValue("func2", "ip2", "value2")

	c.SetValue("func2", "ip22", "value22")

	checkErr(c.DeleteValue("func2", "ip2"))

	cc := c.ListAvailableValue()
	if len(cc) != 0 {
		log.Panicf("expected 0 available items")
	}

	c.MarkAvailable("func", "ip")
	cpuUsage, _ := resource.ParseQuantity("2m")
	_, active, err := c.GetValue("func", 5, cpuUsage)
	if active != 1 {
		log.Panicln("Expected 1 active, found", active)
	}
	checkErr(err)

	checkErr(c.DeleteValue("func", "ip"))

	_, active, err = c.GetValue("func", 5, cpuUsage)
	if err == nil {
		log.Panicf("found deleted element")
	}

	cpuLimit, _ := resource.ParseQuantity("3m")

	c.SetValue("cpulimit", "100", "value")
	c.SetCPUUtilization("cpulimit", "100", cpuLimit)
	c.MarkAvailable("cpulimit", "100")
	currentValue, _ := resource.ParseQuantity("2m")
	_, _, err = c.GetValue("cpulimit", 5, currentValue)

	if err == nil {
		log.Panicf("received pod address with higher CPU usage than limit")
	}
	currentValue, _ = resource.ParseQuantity("5m")
	_, _, err = c.GetValue("cpulimit", 5, currentValue)
	checkErr(err)
}
