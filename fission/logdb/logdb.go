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
	"time"

	log "github.com/Sirupsen/logrus"
)

const (
	INFLUXDB = "influxdb"
)

type LogDatabase interface {
	GetPods(LogFilter) ([]string, error)
	GetLogs(LogFilter) ([]LogEntry, error)
}

type LogFilter struct {
	Pod      string
	Function string
	FuncUid  string
	Since    time.Time
}

type LogEntry struct {
	Timestamp time.Time
	Message   string
	Stream    string
	Container string
	Namespace string
	FuncName  string
	FuncUid   string
	Pod       string
}

type DBConfig struct {
	DBType   string
	Endpoint string
	Username string
	Password string
}

func GetLogDB(cnf DBConfig) (LogDatabase, error) {
	switch cnf.DBType {
	case INFLUXDB:
		return NewInfluxDB(cnf)
	}
	log.WithFields(log.Fields{
		"FISSION_LOGDB_URL": cnf.Endpoint,
	}).Fatalf("FISSION_LOGDB_URL is incorrect, now only support %s", INFLUXDB)
	return nil, nil
}
