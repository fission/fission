// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logdb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

const (
	INFLUXDB   = "influxdb"
	KUBERNETES = "kubernetes"
	LOKI       = "loki"
)

type LogDatabase interface {
	GetLogs(context.Context, LogFilter, *bytes.Buffer) error
}

// LogStreamer is an optional capability a driver implements when it can tail
// logs live. `fission function logs --follow` uses StreamLogs when the selected
// driver supports it (the kubernetes driver follows the pod log stream, the
// loki driver opens a /tail WebSocket), and falls back to one-second GetLogs
// polling otherwise. StreamLogs writes lines to out as they arrive in the
// driver's native format and blocks until the context is cancelled or the
// stream ends.
type LogStreamer interface {
	StreamLogs(ctx context.Context, filter LogFilter, out io.Writer) error
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
	AllPods        bool
	// Correlation filters (RFC-0015 / RFC-0016). Backends that index these
	// fields (e.g. the Loki adapter) narrow the query to a single invocation;
	// backends that do not (kubernetes, influxdb) ignore them.
	RequestID string
	TraceID   string
	Level     string
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

// writeLogEntry renders one entry into output — the detailed multi-line form
// (with --detail) or the compact "[timestamp] message" form — shared by every
// driver so the CLI output is identical regardless of backend.
func writeLogEntry(output io.Writer, entry LogEntry, details bool) error {
	var msg string
	if details {
		msg = fmt.Sprintf("Timestamp: %s\nNamespace: %s\nFunction Name: %s\nFunction ID: %s\nPod: %s\nContainer: %s\nStream: %s\nLog: %s\n---\n",
			entry.Timestamp, entry.Namespace, entry.FuncName, entry.FuncUid, entry.Pod, entry.Container, entry.Stream, entry.Message)
	} else {
		msg = fmt.Sprintf("[%s] %s\n", entry.Timestamp, entry.Message)
	}
	if _, err := io.WriteString(output, msg); err != nil {
		return fmt.Errorf("error copying pod log: %w", err)
	}
	return nil
}

// Factory constructs a log-database driver. Each driver self-registers under
// its name via Register in an init(), so adding a backend is a new file — not
// an edit to a central switch.
type Factory func(ctx context.Context, opts LogDBOptions) (LogDatabase, error)

var registry = map[string]Factory{}

// Register adds a log-database driver under name. Intended to be called from a
// driver file's init().
func Register(name string, f Factory) {
	registry[name] = f
}

// supportedDrivers returns the registered driver names, sorted, for help text
// and error messages.
func supportedDrivers() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func GetLogDB(dbType string, ctx context.Context, logDBOptions LogDBOptions) (LogDatabase, error) {
	f, ok := registry[dbType]
	if !ok {
		return nil, fmt.Errorf("unknown log database %q; supported: %s", dbType, strings.Join(supportedDrivers(), ", "))
	}
	return f(ctx, logDBOptions)
}
