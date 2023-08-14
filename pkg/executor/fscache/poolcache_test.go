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

	// should return err since no svc is present
	_, err := c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found value when expected it to be nil")
	}

	c.SetSvcValue(ctx, "func", "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10, 0)

	// should not return any error since we added a svc
	_, err = c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	checkErr(err)

	c.SetSvcValue(ctx, "func", "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10, 0)

	// should return err since all functions are busy
	_, err = c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found value when expected it to be nil")
	}

	c.SetSvcValue(ctx, "func", "ip", &FuncSvc{
		Name: "value",
	}, resource.MustParse("45m"), 10, 0)

	c.SetSvcValue(ctx, "func2", "ip2", &FuncSvc{
		Name: "value2",
	}, resource.MustParse("50m"), 10, 0)

	c.SetSvcValue(ctx, "func2", "ip22", &FuncSvc{
		Name: "value22",
	}, resource.MustParse("33m"), 10, 0)

	checkErr(c.DeleteValue(ctx, "func2", "ip2"))

	cc := c.ListAvailableValue()
	if len(cc) != 0 {
		log.Panicf("expected 0 available items")
	}

	c.MarkAvailable("func", "ip")

	checkErr(c.DeleteValue(ctx, "func", "ip"))

	_, err = c.GetSvcValue(ctx, "func", requestsPerPod, concurrency)
	if err == nil {
		log.Panicf("found deleted element")
	}

	c.SetSvcValue(ctx, "cpulimit", "100", &FuncSvc{
		Name: "value",
	}, resource.MustParse("3m"), 10, 0)
	c.SetCPUUtilization("cpulimit", "100", resource.MustParse("4m"))
}

func TestPoolCacheRequests(t *testing.T) {

	type structForTest struct {
		name           string
		requests       int
		concurrency    int
		rpp            int
		simultaneous   int
		failedRequests int
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
			name:         "test8",
			requests:     10,
			concurrency:  10,
			rpp:          1,
			simultaneous: 10,
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
					svc, err := p.GetSvcValue(context.Background(), "func", tt.rpp, tt.concurrency)
					if err != nil {
						code, _ := ferror.GetHTTPError(err)
						if code == http.StatusNotFound {
							p.SetSvcValue(context.Background(), "func", fmt.Sprintf("svc-%d", svcCounter), &FuncSvc{
								Name: "value",
							}, resource.MustParse("45m"), tt.rpp, 0)
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
		})
	}
}
