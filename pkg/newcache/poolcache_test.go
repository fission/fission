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

	c.DeleteValue("func2", "ip2")

	c.MarkAvailable("func", "ip")
	cc = c.ListAvailableValue()

	if len(cc) != 1 {
		log.Panic("expected 1 available items, received", len(cc))
	}
	_, err := c.GetValue("func")
	checkErr(err)

	c.DeleteValue("func", "ip")

	_, err = c.GetValue("func")
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetValue("expires", "42", "all answers")

	time.Sleep(150 * time.Millisecond)
	_, err = c.GetValue("expires")
	if err == nil {
		log.Panicf("found expired element")
	}
}
