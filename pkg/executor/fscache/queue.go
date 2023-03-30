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

func (q *Queue) Len() int {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	return q.items.Len()
}
