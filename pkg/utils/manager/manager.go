package manager

import (
	"context"
	"sync"
	"time"
)

// Manager keeps track of the go routines in the system and can be used to gracefully shutdown the
// the system by waiting for completion of go routines added to it.
type Manager interface {
	// Add will start a go routine for the given "function" and adds it to the list of go routines
	// and will also remove the "function" from the list when it completes
	Add(ctx context.Context, function func(context.Context))

	// Wait blocks the execution of the process until all the go routines in the manager are completed.
	Wait()

	// WaitWithTimeout blocks the execution of the process until timeout or till all all the go routines in the manager are completed
	WaitWithTimeout(timeout time.Duration) error
}

type GoRoutineManager struct {
	wg sync.WaitGroup
}

func New() Manager {
	return &GoRoutineManager{
		wg: sync.WaitGroup{},
	}
}
func (g *GoRoutineManager) Add(ctx context.Context, f func(context.Context)) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		f(ctx)
	}()
}

func (g *GoRoutineManager) Wait() {
	g.wg.Wait()
}

func (g *GoRoutineManager) WaitWithTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
