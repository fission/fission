package ratelimiter

import (
	"log"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type resource struct {
	limiter  *rate.Limiter
	lastTime time.Time
}

type RateLimiter struct {
	resources  map[string]*resource
	mu         *sync.RWMutex
	r          rate.Limit
	b          int
	timeExpiry time.Duration
}

func MakeRateLimiter(r rate.Limit, b int, timeExpiry time.Duration) *RateLimiter {
	limiter := &RateLimiter{
		resources:  make(map[string]*resource),
		mu:         &sync.RWMutex{},
		r:          r,
		b:          b,
		timeExpiry: timeExpiry,
	}
	log.Printf("RateLimiter created: %+v\n", limiter)
	go limiter.cleanResources()
	return limiter
}

func (rl *RateLimiter) cleanResources() {
	for {
		log.Printf("Ratelimiter resource cleaning service\n")
		time.Sleep(30 * time.Second)
		rl.mu.Lock()
		for key, resource := range rl.resources {
			if time.Since(resource.lastTime) > rl.timeExpiry {
				delete(rl.resources, key)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) getRateLimiter(resourceKey string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	res, exist := rl.resources[resourceKey]
	if !exist {
		limiter := rate.NewLimiter(rl.r, rl.b)
		rl.resources[resourceKey] = &resource{
			limiter:  limiter,
			lastTime: time.Now(),
		}
		return limiter
	}
	res.lastTime = time.Now()
	return res.limiter
}

func (rl *RateLimiter) RateLimit(resourceKey string,
	callbackFunc func(bool) (interface{}, error)) (interface{}, error) {
	log.Printf("RateLimit fun called 1")
	limiter := rl.getRateLimiter(resourceKey)
	log.Printf("RateLimit func Called ")
	return callbackFunc(limiter.Allow())
}
