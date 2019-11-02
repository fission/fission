/*
Copyright 2019 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package function

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/logdb"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type LogSubCommand struct {
	client *client.Client
}

func Log(flags cli.Input) error {
	opts := LogSubCommand{
		client: cmd.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *LogSubCommand) do(flags cli.Input) error {
	m, err := cmd.GetMetadata("name", "fnNamespace", flags)
	if err != nil {
		return err
	}

	dbType := flags.String("dbtype")
	if len(dbType) == 0 {
		dbType = logdb.INFLUXDB
	}

	fnPod := flags.String("pod")

	logReverseQuery := !flags.Bool("f") && flags.Bool("r")

	recordLimit := flags.Int("recordcount")
	if recordLimit <= 0 {
		recordLimit = 1000
	}

	f, err := opts.client.FunctionGet(m)
	if err != nil {
		return errors.Wrap(err, "error getting function")
	}

	// request the controller to establish a proxy server to the database.
	logDB, err := logdb.GetLogDB(dbType, util.GetServerUrl())
	if err != nil {
		return errors.New("failed to connect log database")
	}

	requestChan := make(chan struct{})
	responseChan := make(chan struct{})
	ctx := context.Background()

	go func(ctx context.Context, requestChan, responseChan chan struct{}) {
		t := time.Unix(0, 0*int64(time.Millisecond))
		for {
			select {
			case <-requestChan:
				logFilter := logdb.LogFilter{
					Pod:         fnPod,
					Function:    f.Metadata.Name,
					FuncUid:     string(f.Metadata.UID),
					Since:       t,
					Reverse:     logReverseQuery,
					RecordLimit: recordLimit,
				}
				logEntries, err := logDB.GetLogs(logFilter)
				if err != nil {
					fmt.Printf("Error querying logs: %v", err)
					responseChan <- struct{}{}
					return
				}
				for _, logEntry := range logEntries {
					if flags.Bool("d") {
						fmt.Printf("Timestamp: %s\nNamespace: %s\nFunction Name: %s\nFunction ID: %s\nPod: %s\nContainer: %s\nStream: %s\nLog: %s\n---\n",
							logEntry.Timestamp, logEntry.Namespace, logEntry.FuncName, logEntry.FuncUid, logEntry.Pod, logEntry.Container, logEntry.Stream, logEntry.Message)
					} else {
						fmt.Printf("[%s] %s\n", logEntry.Timestamp, logEntry.Message)
					}
					t = logEntry.Timestamp
				}
				responseChan <- struct{}{}
			case <-ctx.Done():
				return
			}
		}
	}(ctx, requestChan, responseChan)

	for {
		requestChan <- struct{}{}
		time.Sleep(1 * time.Second)

		<-responseChan
		if !flags.Bool("f") {
			ctx.Done()
			break
		}
	}

	return nil
}
