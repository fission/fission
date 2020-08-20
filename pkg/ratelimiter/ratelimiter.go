package ratelimiter

import (
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

	go limiter.cleanResources()
	return limiter
}

func (rl *RateLimiter) cleanResources() {
	for {
		time.Sleep(20 * time.Second)
		rl.mu.Lock()
		defer rl.mu.Unlock()

		for key, resource := range rl.resources {
			if time.Since(resource.lastTime) > rl.timeExpiry {
				delete(rl.resources, key)
			}
		}
	}
}

func (rl *RateLimiter) addResource(resourceKey string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	res, exist := rl.resources[resourceKey]
	if !exist {
		resource := &resource{
			limiter:  rate.NewLimiter(rl.r, rl.b),
			lastTime: time.Now(),
		}
		rl.resources[resourceKey] = resource
		return resource.limiter
	}

	res.lastTime = time.Now()
	return res.limiter
}

func (rl *RateLimiter) getRateLimiter(resourceKey string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	res, exist := rl.resources[resourceKey]
	if !exist {
		return rl.addResource(resourceKey)
	}
	return res.limiter
}

func (rl *RateLimiter) RateLimit(resourceKey string, callbackFunc func(bool) (interface{}, error)) (interface{}, error) {
	limiter := rl.getRateLimiter(resourceKey)
	return callbackFunc(limiter.Allow())
}
