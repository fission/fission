/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

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

func TestCache(t *testing.T) {
	c := MakeCache[string, string](100*time.Millisecond, 100*time.Millisecond)

	_, err := c.Set("a", "b")
	checkErr(err)
	_, err = c.Set("p", "q")
	checkErr(err)

	val, err := c.Get("a")
	checkErr(err)
	if val != "b" {
		log.Panicf("value %v", val)
	}

	cc := c.Copy()
	if len(cc) != 2 {
		log.Panicf("expected 2 items")
	}

	err = c.Delete("a")
	checkErr(err)

	_, err = c.Get("a")
	if err == nil {
		log.Panicf("found deleted element")
	}

	_, err = c.Set("expires", "42")
	checkErr(err)
	time.Sleep(150 * time.Millisecond)
	_, err = c.Get("expires")
	if err == nil {
		log.Panicf("found expired element")
	}
}
