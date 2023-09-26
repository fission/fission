package fscache

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func checkErr(err error) {
	if err != nil {
		log.Panicf("err: %v", err)
	}
}

func TestPoolCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	c := NewPoolCache(logger)
	concurrency := 5
	requestsPerPod := 2

	keyFunc := crd.CacheKeyURG{
		UID: "func",
	}
	keyFunc2 := crd.CacheKeyURG{
		UID: "func2",
	}

	// should return err since no svc is present
	_, err := c.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found value when expected it to be nil")
	}

	c.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10, 0)

	// should not return any error since we added a svc
	_, err = c.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
	checkErr(err)

	c.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10, 0)

	// should return err since all functions are busy
	_, err = c.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found value when expected it to be nil")
	}

	c.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10, 0)

	c.SetSvcValue(ctx, keyFunc2, "ip2", &FuncSvc{
		Name: "value2",
	}, resource.MustParse("50m"), 10, 0)

	c.SetSvcValue(ctx, keyFunc2, "ip22", &FuncSvc{
		Name: "value22",
	}, resource.MustParse("33m"), 10, 0)

	checkErr(c.DeleteValue(ctx, keyFunc2, "ip2"))

	cc := c.ListAvailableValue()
	if len(cc) != 0 {
		log.Panicf("expected 0 available items")
	}

	c.MarkAvailable(keyFunc, "ip")
	c.MarkFuncDeleted(keyFunc)

	checkErr(c.DeleteValue(ctx, keyFunc, "ip"))

	_, err = c.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetSvcValue(ctx, keyFunc, "100", &FuncSvc{
		Name: "value",
	}, resource.MustParse("3m"), 10, 0)
	c.SetCPUUtilization(keyFunc, "100", resource.MustParse("4m"))
}

func TestPoolCacheRequests(t *testing.T) {
	key := crd.CacheKeyURG{
		UID:        "func",
		Generation: 1,
	}
	type structForTest struct {
		name             string
		requests         int
		concurrency      int
		rpp              int
		simultaneous     int
		failedRequests   int
		retainPods       int
		generationUpdate bool
	}

	for _, tt := range []structForTest{
		{
			name:        "test1",
			requests:    1,
			concurrency: 1,
			rpp:         1,
		},
		{
			name:        "test2",
			requests:    2,
			concurrency: 2,
			rpp:         1,
		},
		{
			name:        "test3",
			requests:    300,
			concurrency: 5,
			rpp:         60,
		},
		{
			name:           "test4",
			requests:       6,
			concurrency:    1,
			rpp:            5,
			failedRequests: 1,
		},
		{
			name:           "test5",
			requests:       6,
			concurrency:    5,
			rpp:            1,
			failedRequests: 1,
		},
		{
			name:         "test6",
			requests:     300,
			concurrency:  5,
			rpp:          60,
			simultaneous: 30,
		},
		{
			name:           "test7",
			requests:       310,
			concurrency:    5,
			rpp:            60,
			simultaneous:   30,
			failedRequests: 10,
		},
		{
			name:        "test9",
			requests:    2,
			concurrency: 2,
			rpp:         1,
			retainPods:  1,
		},
		{
			name:             "test10",
			requests:         10,
			concurrency:      5,
			rpp:              2,
			retainPods:       2,
			generationUpdate: true,
		},
	} {
		t.Run(fmt.Sprintf("scenario-%s", tt.name), func(t *testing.T) {
			var failedRequests, svcCounter uint64
			p := NewPoolCache(loggerfactory.GetLogger())
			wg := sync.WaitGroup{}
			simultaneous := tt.simultaneous
			if simultaneous == 0 {
				simultaneous = 1
			}
			for i := 1; i <= tt.requests; i++ {
				wg.Add(1)
				go func(reqno int) {
					defer wg.Done()
					svc, err := p.GetSvcValue(context.Background(), key, tt.rpp, tt.concurrency)
					if err != nil {
						code, _ := ferror.GetHTTPError(err)
						if code == http.StatusNotFound {
							p.SetSvcValue(context.Background(), key, fmt.Sprintf("svc-%d", svcCounter), &FuncSvc{
								Name: "value",
							}, resource.MustParse("45m"), tt.rpp, tt.retainPods)
							atomic.AddUint64(&svcCounter, 1)
						} else {
							t.Log(reqno, "=>", err)
							atomic.AddUint64(&failedRequests, 1)
						}
					} else {
						if svc == nil {
							t.Log(reqno, "=>", "svc is nil")
							atomic.AddUint64(&failedRequests, 1)
						}
					}
				}(i)
				if i%simultaneous == 0 {
					wg.Wait()
				}
			}
			wg.Wait()

			require.Equal(t, tt.failedRequests, int(atomic.LoadUint64(&failedRequests)))
			require.Equal(t, tt.concurrency, int(atomic.LoadUint64(&svcCounter)))

			for i := 0; i < tt.concurrency; i++ {
				for j := 0; j < tt.rpp; j++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						p.MarkAvailable(key, fmt.Sprintf("svc-%d", i))
					}(i)
				}
			}
			wg.Wait()
			if tt.generationUpdate {
				newKey := crd.CacheKeyURG{
					UID:        "func",
					Generation: 2,
				}
				p.SetSvcValue(context.Background(), newKey, fmt.Sprintf("svc-%d", svcCounter), &FuncSvc{
					Name: "value",
				}, resource.MustParse("45m"), tt.rpp, tt.retainPods)
				funcSvc := p.ListAvailableValue()
				require.Equal(t, tt.concurrency, len(funcSvc))
			} else {
				funcSvc := p.ListAvailableValue()
				require.Equal(t, tt.concurrency-tt.retainPods, len(funcSvc))
			}
		})
	}
}
