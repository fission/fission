package fscache

import (
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
	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	concurrency := 5
	requestsPerPod := 2

	keyFunc := crd.CacheKeyURG{
		UID: "func",
	}
	keyFunc2 := crd.CacheKeyURG{
		UID: "func2",
	}

	t.Run("Test create new svc ", func(t *testing.T) {
		c1 := NewPoolCache(logger)

		// should return err since no svc is present
		_, err := c1.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}

		c1.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		// should not return any error since we added a svc
		_, err = c1.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		checkErr(err)
	})

	t.Run("Test return error when functions are busy", func(t *testing.T) {
		c2 := NewPoolCache(logger)
		c2.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)
		c2.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)
		// should return err since all functions are busy
		_, err := c2.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}
	})

	t.Run("Test does not list available values when a function svc is deleted", func(t *testing.T) {
		c3 := NewPoolCache(logger)
		c3.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		c3.SetSvcValue(ctx, keyFunc2, "ip2", &FuncSvc{
			Name: "value2",
		}, resource.MustParse("50m"), 10, 0)

		checkErr(c3.DeleteValue(ctx, keyFunc2, "ip2"))

		cc := c3.ListAvailableValue()
		if len(cc) != 0 {
			log.Panicf("expected 0 available items")
		}
		_, err := c3.GetSvcValue(ctx, keyFunc2, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found deleted element")
		}
	})

	t.Run("Test return error when current CPU usage is more then permissible", func(t *testing.T) {
		c4 := NewPoolCache(logger)
		_, err := c4.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}

		c4.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		// should not return any error since we added a svc
		_, err = c4.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		checkErr(err)

		c4.SetCPUUtilization(keyFunc, "ip", resource.MustParse("4m"))

		_, err = c4.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}
	})

	t.Run("Test function should not exist when mark deleted is called", func(t *testing.T) {
		c5 := NewPoolCache(logger)
		c5.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		// should not return any error since we added a svc
		_, err := c5.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		checkErr(err)

		c5.MarkFuncDeleted(keyFunc)
		checkErr(c5.DeleteValue(ctx, keyFunc, "ip"))

		_, err = c5.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}
	})
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
			name:        "test8",
			requests:    2,
			concurrency: 2,
			rpp:         1,
			retainPods:  1,
		},
		{
			name:             "test9",
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
				reqno := i
				wg.Go(func() {
					func(reqno int) {
						svc, err := p.GetSvcValue(t.Context(), key, tt.rpp, tt.concurrency)
						if err != nil {
							code, _ := ferror.GetHTTPError(err)
							if code == http.StatusNotFound {
								atomic.AddUint64(&svcCounter, 1)
								address := fmt.Sprintf("svc-%d", atomic.LoadUint64(&svcCounter))
								p.SetSvcValue(t.Context(), key, address, &FuncSvc{
									Name: address,
								}, resource.MustParse("45m"), tt.rpp, tt.retainPods)
							} else {
								t.Log(reqno, "=>", err)
								atomic.AddUint64(&failedRequests, 1)
							}
						} else {
							if svc == nil {
								t.Log(reqno, "=>", "svc is nil")
								atomic.AddUint64(&failedRequests, 1)
							}
							// } else {
							// 	t.Log(reqno, "=>", svc.Name)
							// }
						}
					}(reqno)
				})

				if reqno%simultaneous == 0 {
					wg.Wait()
				}
			}
			wg.Wait()

			require.Equal(t, tt.failedRequests, int(atomic.LoadUint64(&failedRequests)))
			require.Equal(t, tt.concurrency, int(atomic.LoadUint64(&svcCounter)))

			for i := 0; i < tt.concurrency; i++ {
				for j := 0; j < tt.rpp; j++ {
					svcno := i
					wg.Go(func() {
						func(svcno int) {
							p.MarkAvailable(key, fmt.Sprintf("svc-%d", svcno+1))
						}(svcno)
					})
				}
			}
			wg.Wait()
			if tt.generationUpdate {
				newKey := crd.CacheKeyURG{
					UID:        "func",
					Generation: 2,
				}
				address := fmt.Sprintf("svc-%d", svcCounter)
				p.SetSvcValue(t.Context(), newKey, address, &FuncSvc{
					Name: address,
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
