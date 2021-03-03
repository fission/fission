package poolcache

import (
	"log"
	"testing"
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
	_, active, err := c.GetValue("func", 5, 85)
	if active != 1 {
		log.Panicln("Expected 1 active, found", active)
	}
	checkErr(err)

	checkErr(c.DeleteValue("func", "ip"))

	_, active, err = c.GetValue("func", 5, 85)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetValue("cpulimit", "100", "value")
	c.SetCPUPercentage("cpulimit", "100", 95)
	c.MarkAvailable("cpulimit", "100")
	_, _, err = c.GetValue("cpulimit", 5, 85)
	if err == nil {
		log.Panicf("received pod address with higher CPU usage than limit")
	}
	_, _, err = c.GetValue("cpulimit", 5, 99)
	checkErr(err)
}
