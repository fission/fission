package newcache

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

func TestNewCache(t *testing.T) {
	c := MakeCache(100*time.Millisecond, 100*time.Millisecond)

	c.Set("func", "ip", "value")

	c.Set("func2", "ip2", "value2")

	c.Set("func2", "ip22", "value22")

	cc := c.Copy()
	if len(cc) != 2 {
		log.Panicf("expected 2 items")
	}
	active := c.GetTotalActive("func2")
	if active != 2 {
		log.Panicf("expected 2 items")
	}

	c.Delete("func2", "ip2")

	c.UnSet("func", "ip")

	_, err := c.Get("func")
	checkErr(err)

	c.Delete("func", "ip")

	_, err = c.Get("func")
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.Set("expires", "42", "all answers")

	time.Sleep(150 * time.Millisecond)
	_, err = c.Get("expires")
	if err == nil {
		log.Panicf("found expired element")
	}
}
