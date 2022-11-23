/*
Copyright 2016 The Fission Authors.

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

package logdb

import (
	"bytes"
	"context"
	"fmt"
	"time"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

const (
	INFLUXDB   = "influxdb"
	KUBERNETES = "kubernetes"
)

type LogDatabase interface {
	GetLogs(context.Context, LogFilter, *bytes.Buffer) error
}

type LogFilter struct {
	Pod            string
	PodNamespace   string
	Function       string
	FuncUid        string
	Since          time.Time
	Reverse        bool
	RecordLimit    int
	FunctionObject *v1.Function
	Details        bool
	WarnUser       bool
}

type LogEntry struct {
	Timestamp time.Time
	Message   string
	Stream    string
	Sequence  int
	Container string
	Namespace string
	FuncName  string
	FuncUid   string
	Pod       string
}

type ByTimestampSort struct {
	entries []LogEntry
	desc    bool
}

func (a ByTimestampSort) Len() int      { return len(a.entries) }
func (a ByTimestampSort) Swap(i, j int) { a.entries[i], a.entries[j] = a.entries[j], a.entries[i] }
func (a ByTimestampSort) Less(i, j int) bool {
	if a.desc {
		return a.entries[i].Timestamp.UnixNano() > a.entries[j].Timestamp.UnixNano()
	} else {
		return a.entries[i].Timestamp.UnixNano() < a.entries[j].Timestamp.UnixNano()
	}
}

func ByTimestamp(entries []LogEntry, desc bool) ByTimestampSort {
	return ByTimestampSort{entries, desc}
}

func GetLogDB(dbType string, ctx context.Context, logDBOptions LogDBOptions) (LogDatabase, error) {
	switch dbType {
	case INFLUXDB:
		return NewInfluxDB(ctx, logDBOptions)
	case KUBERNETES:
		return NewKubernetesEndpoint(logDBOptions)
	}
	return nil, fmt.Errorf("log database type is incorrect, now only support %s", INFLUXDB)
}
