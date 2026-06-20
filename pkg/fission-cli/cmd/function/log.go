// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/logdb"
)

type LogSubCommand struct {
	cmd.CommandActioner
}

func Log(input cli.Input) error {
	return (&LogSubCommand{}).do(input)
}

func (opts *LogSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return fmt.Errorf("error in logs for function : %w", err)
	}

	dbType := input.String(flagkey.FnLogDBType)
	fnPod := input.String(flagkey.FnLogPod)

	logReverseQuery := !input.Bool(flagkey.FnLogFollow) && input.Bool(flagkey.FnLogReverseQuery)

	allPods := input.Bool(flagkey.FnLogAllPods)
	recordLimit := input.Int(flagkey.FnLogCount)
	if recordLimit <= 0 {
		recordLimit = 1000
	}

	f, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function: %w", err)
	}

	logDBOptions := logdb.LogDBOptions{
		Client: opts.Client(),
	}

	// request the controller to establish a proxy server to the database.
	logDB, err := logdb.GetLogDB(dbType, input.Context(), logDBOptions)
	if err != nil {
		return fmt.Errorf("failed to get log from %s: %w", dbType, err)
	}

	// Correlation filters are only honored by backends that index those fields
	// (the loki adapter); warn rather than silently return unfiltered logs.
	if dbType != logdb.LOKI &&
		(input.String(flagkey.FnLogRequestID) != "" || input.String(flagkey.FnLogTraceID) != "" || input.String(flagkey.FnLogLevel) != "") {
		console.Warn(fmt.Sprintf("--request-id/--trace-id/--level filters are only applied by the loki dbtype and are ignored for %q", dbType))
	}

	// The filter is built once; the polling loop below overrides only the
	// per-iteration fields (Since/Reverse/WarnUser/Details).
	baseFilter := logdb.LogFilter{
		Pod:            fnPod,
		PodNamespace:   input.String(flagkey.NamespacePod),
		Function:       f.Name,
		FuncUid:        string(f.UID),
		RecordLimit:    recordLimit,
		FunctionObject: f,
		Details:        input.Bool(flagkey.FnLogDetail),
		AllPods:        allPods,
		RequestID:      input.String(flagkey.FnLogRequestID),
		TraceID:        input.String(flagkey.FnLogTraceID),
		Level:          input.String(flagkey.FnLogLevel),
	}

	// Live streaming: when --follow is set and the driver can tail (kubernetes
	// follows the pod stream, loki opens a /tail WebSocket), stream straight to
	// stdout instead of the one-second poll loop below. Drivers that can't
	// stream fall through to polling.
	if input.Bool(flagkey.FnLogFollow) {
		if streamer, ok := logDB.(logdb.LogStreamer); ok {
			return streamer.StreamLogs(input.Context(), baseFilter, os.Stdout)
		}
	}

	requestChan := make(chan struct{})
	// responseChan carries each iteration's result so the one-shot exit returns
	// the backend error by type, with no err variable shared across goroutines.
	responseChan := make(chan error)
	ctx := input.Context()
	warn := true

	go func(ctx context.Context, requestChan chan struct{}, responseChan chan error) {
		t := time.Unix(0, 0*int64(time.Millisecond))
		detail := baseFilter.Details
		for {
			select {
			case <-requestChan:
				logFilter := baseFilter
				logFilter.Since = t
				logFilter.Reverse = logReverseQuery
				logFilter.Details = detail
				logFilter.WarnUser = warn

				buf := new(bytes.Buffer)
				qerr := logDB.GetLogs(ctx, logFilter, buf)
				t = time.Now().UTC() // next time fetch values from this time
				if qerr != nil {
					console.Verbose(2, "error querying logs: %s", qerr)
					if dbType == logdb.KUBERNETES { // in case of Kubernetes log we print pod namespace warning once
						warn = false
					}
					responseChan <- qerr
					continue
				}
				if _, cerr := io.Copy(os.Stdout, buf); cerr != nil {
					console.Verbose(2, "error copying logs: %s", cerr)
					responseChan <- cerr
					continue
				}

				if dbType == logdb.KUBERNETES { // in case of Kubernetes log we print pods info only once. And then print new logs
					detail = false
				}
				responseChan <- nil
			case <-ctx.Done():
				return
			}
		}
	}(ctx, requestChan, responseChan)

	for {
		requestChan <- struct{}{}
		time.Sleep(1 * time.Second)

		qerr := <-responseChan
		if !input.Bool(flagkey.FnLogFollow) {
			// One-shot query: surface a backend error (bad query, auth,
			// unreachable) instead of swallowing it and exiting 0.
			return qerr
		}
	}
}
