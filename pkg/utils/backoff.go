package utils

import (
	"fmt"
	"time"
)

const (
	DefaultInitialInterval = 500 * time.Millisecond
	DefaultMultiplier      = 1.5
	DefaultMaxInterval     = 60 * time.Second
	DefaultMaxCount        = 15
)

type backoff struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	MaxCount        float64
	currentbackoff  time.Duration
	currentCount    float64
}

type backoffInterface interface {
	GetInitialInterval() time.Duration
	GetMultiplier() float64
	GetMaxInterval() time.Duration
	GetMaxCount() float64
	SetMaxCount(float64)
	SetMultiplier(float64)
	SetMaxInterval(time.Duration)
	SetInitialInterval(time.Duration)
	GetNext() time.Duration
	GetCurrentCount() float64
	GetCurrentBackoffDuration() time.Duration
	NextExists() bool
}

func NewBackOff(initialInterval time.Duration, maxInterval time.Duration, multiplier float64, maxCount float64) (*backoff, error) {
	if multiplier < 0 || maxInterval < 0 || initialInterval < 0 || maxCount < 0 {
		return &backoff{}, fmt.Errorf("Negative Value for multiplier and maxInternal not allowed")
	}

	return &backoff{
		MaxInterval:     maxInterval,
		Multiplier:      multiplier,
		InitialInterval: initialInterval,
		currentbackoff:  DefaultInitialInterval,
		currentCount:    0,
	}, nil
}

func NewDefaultBackOff() *backoff {
	return &backoff{
		MaxInterval:     DefaultMaxInterval,
		Multiplier:      DefaultMultiplier,
		InitialInterval: DefaultInitialInterval,
		MaxCount:        DefaultMaxCount,
		currentbackoff:  DefaultInitialInterval,
		currentCount:    0,
	}
}
func (backoff *backoff) GetMultiplier() float64 {
	return backoff.Multiplier
}

func (backoff *backoff) GetMaxInterval() time.Duration {
	return backoff.MaxInterval
}

func (backoff *backoff) GetInitialInterval() time.Duration {
	return backoff.InitialInterval
}

func (backoff *backoff) GetMaxCount() float64 {
	return backoff.MaxCount
}

func (backoff *backoff) SetMaxCount(maxCount float64) {
	backoff.MaxCount = maxCount
}

func (backoff *backoff) SetMultiplier(multiplier float64) {
	backoff.Multiplier = multiplier
}

func (backoff *backoff) SetMaxInterval(maxInterval time.Duration) {
	backoff.MaxInterval = maxInterval
}

func (backoff *backoff) SetInitialInterval(initialInterval time.Duration) {
	backoff.InitialInterval = initialInterval
}

func (backoff *backoff) GetCurrentBackoffDuration() time.Duration {
	return backoff.currentbackoff
}
func (backoff *backoff) GetCurrentCount() float64 {
	return backoff.currentCount
}

func (backoff *backoff) GetNext() time.Duration {
	backoff.currentbackoff = backoff.currentbackoff * time.Duration(backoff.Multiplier)
	backoff.currentCount = backoff.currentCount + 1
	return backoff.currentbackoff
}

func (backoff *backoff) NextExists() bool {
	if backoff.currentbackoff*time.Duration(backoff.Multiplier) > backoff.MaxInterval || backoff.currentCount > backoff.MaxCount {
		return false
	}
	return true
}
