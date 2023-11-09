package manager

import (
	"context"
	"sync"
	"time"

	k8sCache "k8s.io/client-go/tools/cache"
)

var _ Interface = &GroupManager{}

// Interface keeps track of the go routines in the system and can be used to gracefully shutdown the
// the system by waiting for completion of go routines added to it.
type Interface interface {
	// Add will start a go routine for the given "function" and adds it to the list of go routines
	// and will also remove the "function" from the list when it completes
	Add(ctx context.Context, function func(context.Context))

	AddInformers(ctx context.Context, informers map[string]k8sCache.SharedIndexInformer)

	// Wait blocks the execution of the process until all the go routines in the manager are completed.
	Wait()

	// WaitWithTimeout blocks the execution of the process until timeout or till all all the go routines in the manager are completed
	WaitWithTimeout(timeout time.Duration) error
}

type GroupManager struct {
	wg sync.WaitGroup
}

func New() Interface {
	return &GroupManager{
		wg: sync.WaitGroup{},
	}
}
func (g *GroupManager) Add(ctx context.Context, f func(context.Context)) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		f(ctx)
	}()
}

func (g *GroupManager) AddInformers(ctx context.Context, informers map[string]k8sCache.SharedIndexInformer) {
	for _, informer := range informers {
		g.Add(ctx, func(ctxArg context.Context) {
			informer.Run(ctxArg.Done())
		})
	}
}

func (g *GroupManager) Wait() {
	g.wg.Wait()
}

func (g *GroupManager) WaitWithTimeout(timeout time.Duration) error {
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
