package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/dchest/uniuri"
)

type (
	Analytics struct {
		id  string
		url string
	}
	AnalyticsData struct {
		id                string
		FunctionCallCount uint64 `json:"FunctionCallCount"`
	}
)

func MakeAnalytics(url string) *Analytics {

	if len(url) == 0 {
		url = os.Getenv("ANALYTICS_URL")
		if len(url) == 0 {
			return nil
		}
	}

	a := &Analytics{
		url: url,
		id:  uniuri.NewLen(8),
	}
	go a.run()
	return a
}

func (a *Analytics) gatherData() *AnalyticsData {
	return &AnalyticsData{
		FunctionCallCount: atomic.LoadUint64(&globalFunctionCallCount),
	}
}

func (a *Analytics) run() {
	ticker := time.NewTicker(24 * time.Hour)
	for range ticker.C {
		msg := a.gatherData()
		msg.id = a.id

		msgbytes, err := json.Marshal(*msg)
		if err != nil {
			continue
		}

		resp, err := http.Post(a.url, "application/json", bytes.NewReader(msgbytes))
		if resp != nil {
			// close response body to prevent resources leak
			resp.Body.Close()
		}
	}
}
