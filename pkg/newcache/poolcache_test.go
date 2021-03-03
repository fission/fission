package poolcache

import (
	"log"
	"testing"
	"time"
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

	cc := c.ListAvailableValue()
	if len(cc) != 0 {
		log.Panicf("expected 0 available items")
	}
	active := c.GetTotalAvailable("func2")
	if active != 2 {
		log.Panicf("expected 2 items")
	}

	checkErr(c.DeleteValue("func2", "ip2"))

	c.MarkAvailable("func", "ip")
	cc = c.ListAvailableValue()

	if len(cc) != 1 {
		log.Panic("expected 1 available items, received", len(cc))
	}
	_, err := c.GetValue("func", 5, 85)
	checkErr(err)

	checkErr(c.DeleteValue("func", "ip"))

	_, err = c.GetValue("func", 5, 85)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetValue("expires", "42", "all answers")

	time.Sleep(150 * time.Millisecond)
	_, err = c.GetValue("expires", 5, 85)
	if err == nil {
		log.Panicf("found expired element")
	}

	c.SetValue("cpulimit", "100", "value")
	c.SetCPUPercentage("cpulimit", "100", 95)
	c.MarkAvailable("cpulimit", "100")
	_, err = c.GetValue("cpulimit", 5, 85)
	if err == nil {
		log.Panicf("received pod address with higher CPU usage than limit")
	}
	_, err = c.GetValue("cpulimit", 5, 99)
	checkErr(err)
}
