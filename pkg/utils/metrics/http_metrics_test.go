package metrics

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var dataRow = []byte("I'm the data Row\n")

func chunkedHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	ticker := time.NewTicker(time.Second) // We may set it to 10 secs
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ticker.C:
				_, _ = w.Write(dataRow)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Emulate some work
	time.Sleep(5 * time.Second)

	// Telling the loop that keeps the connection alive to end
	cancel()

	// Waiting until the loop ends
	wg.Wait()
}

func TestChunked(t *testing.T) {
	mr := mux.NewRouter()
	mr.Use(HTTPMetricMiddleware)
	mr.Handle("/", http.HandlerFunc(chunkedHandler))
	s := httptest.NewServer(mr)
	defer s.Close()

	resp, err := http.Get(s.URL)
	require.NoError(t, err)
	assert.Contains(t, resp.TransferEncoding, "chunked")
	defer resp.Body.Close()

	r := bufio.NewReader(resp.Body)
	for {
		line, err := readChunkedResponseLine(r)
		if err != nil {
			if err == io.EOF {
				return
			}
			log.Fatal(err.Error())
		}
		if len(line) == 0 {
			log.Println("Alive!")
			continue
		}

		fmt.Println(string(line)) // we got the final response
		assert.Equal(t, dataRow, append(line, '\n'))
	}
}

func readChunkedResponseLine(r *bufio.Reader) ([]byte, error) {
	line, isPrefix, err := r.ReadLine()
	if err != nil {
		return nil, err
	}

	if isPrefix {
		rest, err := readChunkedResponseLine(r)
		if err != nil {
			return nil, err
		}
		line = append(line, rest...)
	}

	return line, nil
}
