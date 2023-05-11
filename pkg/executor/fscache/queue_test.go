package fscache

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"
)

func TestNewQueue(t *testing.T) {
	q := NewQueue(&zap.Logger{})
	if q == nil {
		t.Error("NewQueue returned nil")
	}
}

func TestQueuePushWithSingleRequest(t *testing.T) {
	q := NewQueue(&zap.Logger{})
	item := &svcWait{
		svcChannel: make(chan *FuncSvc),
		ctx:        nil,
	}
	q.Push(item)
	if q.Len() != 1 {
		t.Errorf("Expected queue length to be 1, got %d", q.Len())
	}
}

func TestQueuePopWithSingleRequest(t *testing.T) {
	q := NewQueue(&zap.Logger{})
	item := &svcWait{
		svcChannel: make(chan *FuncSvc),
		ctx:        nil,
	}
	q.Push(item)
	popped := q.Pop()
	if popped == nil {
		t.Error("Expected Pop to return a non-nil value")
	}
	if popped != item {
		t.Error("Expected Pop to return the same element that was pushed")
	}
	if q.Len() != 0 {
		t.Errorf("Expected queue length to be 0, got %d", q.Len())
	}
}

func TestQueuePushWithConcurrentRequest(t *testing.T) {
	q := NewQueue(&zap.Logger{})
	noOfRequests := 20
	var wg sync.WaitGroup
	wg.Add(noOfRequests)
	for i := 0; i < noOfRequests; i++ {
		go func() {
			defer wg.Done()
			item := &svcWait{
				svcChannel: make(chan *FuncSvc),
				ctx:        nil,
			}
			q.Push(item)
		}()
	}

	wg.Wait()

	if q.Len() != noOfRequests {
		t.Errorf("Expected queue length to be 20, got %d", q.Len())
	}
}

func TestQueuePopWithConcurrentRequest(t *testing.T) {
	q := NewQueue(&zap.Logger{})
	noOfPush := 20
	noOfPop := 15

	var wg sync.WaitGroup
	wg.Add(noOfPush + noOfPop)

	for i := 0; i < noOfPush; i++ {
		go func() {
			defer wg.Done()
			item := &svcWait{
				svcChannel: make(chan *FuncSvc),
				ctx:        nil,
			}
			q.Push(item)
		}()
	}

	for i := 0; i < noOfPop; i++ {
		go func() {
			defer wg.Done()
			q.Pop()
		}()
	}

	wg.Wait()

	if q.Len() != 5 {
		t.Errorf("Expected queue length to be 5, got %d", q.Len())
	}
}

func TestQueueLen(t *testing.T) {
	q := NewQueue(&zap.Logger{})
	if q.Len() != 0 {
		t.Errorf("Expected queue length to be 0, got %d", q.Len())
	}
	item := &svcWait{
		svcChannel: make(chan *FuncSvc),
		ctx:        nil,
	}
	q.Push(item)
	if q.Len() != 1 {
		t.Errorf("Expected queue length to be 1, got %d", q.Len())
	}
}

func TestExpired(t *testing.T) {
	q := NewQueue(&zap.Logger{})
	if q.Expired() != 0 {
		t.Errorf("Expected Expired to return 0, got %d", q.Expired())
	}
	ctx, cancel := context.WithCancel(context.Background())
	item := &svcWait{
		svcChannel: make(chan *FuncSvc),
		ctx:        ctx,
	}
	q.Push(item)
	if q.Len() != 1 {
		t.Errorf("Expected queue length to be 1, got %d", q.Len())
	}
	cancel()
	if q.Expired() != 1 {
		t.Errorf("Expected Expired to return 1, got %d", q.Expired())
	}
	if q.Len() != 0 {
		t.Errorf("Expected queue length to be 0, got %d", q.Len())
	}
}
