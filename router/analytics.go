package router

import (
	"os"
	"bytes"
	"sync/atomic"
	"time"
	"net/http"
	"encoding/json"

	"github.com/dchest/uniuri"
)

type (
	Analytics struct {
		id string
		url string
	}
	AnalyticsData struct {
		id string
		functionCallCount uint64 `json:"functionCallCount"`
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
		id: uniuri.NewLen(8),
	}
	go a.run()
	return a
}

func (a *Analytics) gatherData() *AnalyticsData {
	return &AnalyticsData{
		functionCallCount: atomic.LoadUint64(&globalFunctionCallCount),
	}
}

func (a *Analytics) run() {	
	ticker := time.NewTicker(24 * time.Hour)
        for _ = range ticker.C {
		msg := a.gatherData()
		msg.id = a.id
		
		msgbytes, err := json.Marshal(*msg)
		if err != nil {
			continue
		}
		
		_, _ = http.Post(a.url, "application/json", bytes.NewReader(msgbytes))
	}
}
