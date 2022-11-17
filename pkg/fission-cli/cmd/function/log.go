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
	"io"
	"os"
	"time"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
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
		return errors.Wrap(err, "error in logs for function ")
	}

	dbType := input.String(flagkey.FnLogDBType)
	fnPod := input.String(flagkey.FnLogPod)

	logReverseQuery := !input.Bool(flagkey.FnLogFollow) && input.Bool(flagkey.FnLogReverseQuery)

	recordLimit := input.Int(flagkey.FnLogCount)
	if recordLimit <= 0 {
		recordLimit = 1000
	}

	f, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "error getting function")
	}

	logDBOptions := logdb.LogDBOptions{
		Client: opts.Client(),
	}

	// request the controller to establish a proxy server to the database.
	logDB, err := logdb.GetLogDB(dbType, input.Context(), logDBOptions)
	if err != nil {
		return errors.Wrapf(err, "failed to get log database")
	}

	requestChan := make(chan struct{})
	responseChan := make(chan struct{})
	ctx := input.Context()

	go func(ctx context.Context, requestChan, responseChan chan struct{}) {
		t := time.Unix(0, 0*int64(time.Millisecond))
		detail := input.Bool(flagkey.FnLogDetail)
		for {
			select {
			case <-requestChan:
				logFilter := logdb.LogFilter{
					Pod:            fnPod,
					PodNamespace:   input.String(flagkey.NamespacePod),
					Function:       f.ObjectMeta.Name,
					FuncUid:        string(f.ObjectMeta.UID),
					Since:          t,
					Reverse:        logReverseQuery,
					RecordLimit:    recordLimit,
					FunctionObject: f,
					Details:        detail,
				}

				buf, err := logDB.GetLogs(ctx, logFilter)
				if err != nil {
					fmt.Printf("Error querying logs: %v", err)
					responseChan <- struct{}{}
					return
				}
				_, err = io.Copy(os.Stdout, buf)
				if err != nil {
					return
				}

				t = time.Now().UTC()            // next time fetch values from this time
				if dbType == logdb.KUBERNETES { //in case of Kubernetes log we print pods info only once. And then print new logs
					detail = false
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
		if !input.Bool(flagkey.FnLogFollow) {
			ctx.Done()
			break
		}
	}

	return nil
}
