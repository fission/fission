package fscache

import (
	"container/list"
	"sync"
)

type Queue struct {
	items *list.List
	mutex sync.Mutex
}

func NewQueue() *Queue {
	return &Queue{
		items: list.New(),
	}
}

func (q *Queue) Push(item *svcWait) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.items.PushBack(item)
}

func (q *Queue) Pop() *svcWait {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	item := q.items.Front()
	if item == nil {
		return nil
	}
	q.items.Remove(item)
	svcWait, ok := item.Value.(*svcWait)
	if !ok {
		return nil
	}
	return svcWait
}

func (q *Queue) Expired() int {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	expired := 0
	svcExpired := []*list.Element{}
	for item := q.items.Front(); item != nil; item = item.Next() {
		svcWait, ok := item.Value.(*svcWait)
		if !ok {
			continue
		}
		if svcWait.ctx.Err() != nil {
			close(svcWait.svcChannel)
			svcExpired = append(svcExpired, item)
			expired = expired + 1
		}
	}

	for _, item := range svcExpired {
		q.items.Remove(item)
	}

	return expired
}

func (q *Queue) Len() int {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	return q.items.Len()
}
