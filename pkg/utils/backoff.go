package utils

import (
	"fmt"
	"time"
)

const (
	DefaultInitialInterval = 500 * time.Millisecond
	DefaultMultiplier      = 1.5
	DefaultMaxInterval     = 300 * time.Second
	DefaultMaxCount        = 30
)

type backoff struct {
	// InitialInterval is the first interval of backoff
	InitialInterval time.Duration
	// MaxInterval defines the maximum Backoff Interval can be
	// After that, NextExist() returns false
	MaxInterval time.Duration
	// Multiplier defines the multiplier applied on current backoff interval
	Multiplier float64
	// MaxCount defines the maximum retries to be attempted
	// After that, NextExists() returns false
	MaxCount       float64
	currentbackoff time.Duration
	currentCount   float64
}

// NewBackOff returns a new backoff struct with initialized values
func NewBackOff(initialInterval time.Duration, maxInterval time.Duration, multiplier float64, maxCount float64) (*backoff, error) {
	if multiplier < 0 || maxInterval < 0 || initialInterval < 0 || maxCount < 0 {
		return &backoff{}, fmt.Errorf("negative value for multiplier and max internal not allowed")
	}

	return &backoff{
		MaxInterval:     maxInterval,
		Multiplier:      multiplier,
		InitialInterval: initialInterval,
		currentbackoff:  DefaultInitialInterval,
		currentCount:    0,
	}, nil
}

// NewDefaultBackOff returns new backoff struct with default values
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

// GetMultiplier returns multiplier of current backoff
func (backoff *backoff) GetMultiplier() float64 {
	return backoff.Multiplier
}

// GetMaxInterval return MaxInterval of current backoff
func (backoff *backoff) GetMaxInterval() time.Duration {
	return backoff.MaxInterval
}

// GetInitialInterval returns the InitialInterval of current backoff
func (backoff *backoff) GetInitialInterval() time.Duration {
	return backoff.InitialInterval
}

// GetMaxCount returns the MaxCount of current backoff
func (backoff *backoff) GetMaxCount() float64 {
	return backoff.MaxCount
}

// SetMaxCount updates the MaxCount of pre-created backoff
func (backoff *backoff) SetMaxCount(maxCount float64) {
	backoff.MaxCount = maxCount
}

// SetMultiplier updates the Multiplier of pre-created backoff
func (backoff *backoff) SetMultiplier(multiplier float64) {
	backoff.Multiplier = multiplier
}

// SetMaxInterval updates the MaxInterval of pre-created backoff
func (backoff *backoff) SetMaxInterval(maxInterval time.Duration) {
	backoff.MaxInterval = maxInterval
}

// SetInitialInterval updates the InitialInterval of pre-created backoff
func (backoff *backoff) SetInitialInterval(initialInterval time.Duration) {
	backoff.InitialInterval = initialInterval
}

// GetCurrentBackoffDuration returns the time.Duration for current backoff time determined
func (backoff *backoff) GetCurrentBackoffDuration() time.Duration {
	return backoff.currentbackoff
}

// GetCurrentCount returns the float64 with current retry count
func (backoff *backoff) GetCurrentCount() float64 {
	return backoff.currentCount
}

// GetNext returns time.Duration to add sleep for current retry
func (backoff *backoff) GetNext() time.Duration {
	backoff.currentbackoff = time.Duration(float64(backoff.currentbackoff) * backoff.Multiplier)
	backoff.currentCount = backoff.currentCount + 1
	return backoff.currentbackoff
}

// NextExists returns boolean representing the status of next backoff duration
func (backoff *backoff) NextExists() bool {
	if backoff.currentbackoff*time.Duration(backoff.Multiplier) > backoff.MaxInterval || backoff.currentCount > backoff.MaxCount {
		return false
	}
	return true
}
