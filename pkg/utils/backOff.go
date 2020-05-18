package utils

import (
	"fmt"
	"time"
)

const (
	DefaultInitialInterval     = 500 * time.Millisecond
	DefaultMultiplier          = 1.5
	DefaultMaxInterval         = 60 * time.Second
)

type backOff struct{
	InitialInterval time.Duration
	MaxInterval time.Duration
	Multiplier float64
	currentBackOff time.Duration
	currentCount int
}

type BackOffInterface interface {
	GetInitialInterval() (time.Duration)
	GetMultiplier() (float64)
	SetMultiplier(float64) ()
	GetMaxInterval() (time.Duration)
	SetMaxInterval(time.Duration) ()
	SetInitialInterval (time.Duration)()
	GetNext () (time.Duration)
	GetCurrentCount() (int)
	GetCurrentBackOffDuration () (time.Duration)
	NextExists() (bool)

}
func NewBackOff (initialInterval time.Duration, maxInterval time.Duration, multiplier float64) (*backOff, error) {
	if multiplier < 0 || maxInterval < 0 || initialInterval < 0{
		return &backOff{},fmt.Errorf("Negative Value for multiplier and maxInternal not allowed")
	}

	return &backOff{
		MaxInterval: maxInterval,
		Multiplier: multiplier,
		InitialInterval: initialInterval,
		currentBackOff: DefaultInitialInterval,
		currentCount: 0,
	}, nil
}

func (backOff *backOff) GetMultiplier () float64 {
	return backOff.Multiplier
}

func (backOff *backOff) GetMaxInterval () time.Duration {
	return backOff.MaxInterval
}

func (backOff *backOff) GetInitialInterval () time.Duration {
	return backOff.InitialInterval
}

func (backOff *backOff) SetMultiplier (multiplier float64) () {
	backOff.Multiplier = multiplier
}

func (backOff *backOff) SetMaxInterval (maxInterval time.Duration) () {
	backOff.MaxInterval = maxInterval
}

func (backOff *backOff) SetInitialInterval (initialInterval time.Duration) () {
	backOff.InitialInterval = initialInterval
}

func (backOff *backOff) GetCurrentBackOffDuration () (time.Duration) {
	return backOff.currentBackOff
}
func (backOff *backOff) GetCurrentCount () int {
	return backOff.currentCount
}

func (backOff *backOff) GetNext () time.Duration {
	backOff.currentBackOff = backOff.currentBackOff * time.Duration(backOff.Multiplier)
	backOff.currentCount = backOff.currentCount + 1
	return backOff.currentBackOff
}

func (backOff *backOff) NextExists() (bool) {
	if backOff.currentBackOff *time.Duration(backOff.Multiplier) > backOff.MaxInterval {
		return false
	}

	return true
}
