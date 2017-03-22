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
	"log"
	"time"

	"fmt"

	"strings"

	influxdbClient "github.com/influxdata/influxdb/client/v2"
)

const (
	INFLUXDB_DATABASE = "fissionFunctionLog"
)

func NewInfluxDB(cnf DBConfig) (InfluxDB, error) {
	dbClient, err := influxdbClient.NewHTTPClient(influxdbClient.HTTPConfig{
		Addr:     cnf.Endpoint,
		Username: cnf.Username,
		Password: cnf.Password,
	})
	if err != nil {
		log.Fatal(err)
		return InfluxDB{}, err
	}

	return InfluxDB{
		dbClient: dbClient,
	}, nil
}

type InfluxDB struct {
	dbClient influxdbClient.Client
}

func (influx InfluxDB) GetPods(filter LogFilter) ([]string, error) {
	query := fmt.Sprintf("select * from \"log\" where \"funcuid\" = '%s' group by \"pod\"", filter.FuncUid)
	q := influxdbClient.Query{
		Command:  query,
		Database: INFLUXDB_DATABASE,
	}
	response, err := influx.dbClient.Query(q)
	if err != nil /*|| response.Err != ""*/ {
		return []string{}, err
	}
	pods := []string{}
	for _, r := range response.Results {
		for _, series := range r.Series {
			for _, pod := range series.Tags {
				pods = append(pods, pod)
			}
		}
	}
	return pods, nil
}

func (influx InfluxDB) GetLogs(filter LogFilter) ([]LogEntry, error) {
	timestamp := filter.Since.UnixNano()
	var query string
	if filter.Pod != "" {
		query = fmt.Sprintf("select * from \"log\" where \"funcuid\" = '%s' AND \"pod\" = '%s' AND \"time\" > %d ORDER BY time ASC", filter.FuncUid, filter.Pod, timestamp)
	} else {
		query = fmt.Sprintf("select * from \"log\" where \"funcuid\" = '%s' AND \"time\" > %d ORDER BY time ASC", filter.FuncUid, timestamp)
	}

	logEntries := []LogEntry{}
	response, err := influx.query(query)
	if err != nil {
		return logEntries, nil
	}
	for _, r := range response.Results {
		for _, series := range r.Series {
			for _, row := range series.Values {
				t, err := time.Parse(time.RFC3339, row[0].(string))
				if err != nil {
					log.Fatal(err)
				}
				logEntries = append(logEntries, LogEntry{
					Timestamp: t,
					Container: row[2].(string),
					FuncName:  row[3].(string),
					FuncUid:   row[4].(string),
					Message:   strings.TrimSuffix(row[5].(string), "\n"),
					Namespace: row[6].(string),
					Pod:       row[7].(string),
					Stream:    row[8].(string),
				})
			}
		}
	}
	return logEntries, nil
}

func (influx InfluxDB) query(queryCmd string) (*influxdbClient.Response, error) {
	q := influxdbClient.Query{
		Command:  queryCmd,
		Database: INFLUXDB_DATABASE,
	}
	return influx.dbClient.Query(q)
}
