package ratelimiter

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func apiCallExecutor(i int, wg *sync.WaitGroup) {
	defer wg.Done()
	fmt.Printf("Executor, Serving %d\n", i)
	time.Sleep(10 * time.Second)
}

func TestRateLimiter_RateLimit(t *testing.T) {
	rl := MakeRateLimiter(5, 5, 1*time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		rl.RateLimit("resourceTest", func(flag bool) (interface{}, error) {
			if flag {
				wg.Add(1)
				go apiCallExecutor(i, &wg)
				return "Success", nil
			}
			fmt.Printf("Cache, Serving %d\n", i)
			return "Failed", nil
		})
	}
	wg.Wait()
}
