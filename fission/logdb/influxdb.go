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
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	influxdbClient "github.com/influxdata/influxdb/client/v2"

	"github.com/fission/fission"
)

const (
	INFLUXDB_DATABASE = "fissionFunctionLog"
	INFLUXDB_URL      = "http://influxdb:8086/query"
)

func NewInfluxDB(serverURL string) (InfluxDB, error) {
	return InfluxDB{endpoint: serverURL}, nil
}

type InfluxDB struct {
	endpoint string
}

func (influx InfluxDB) GetPods(filter LogFilter) ([]string, error) {
	parameters := make(map[string]interface{})
	parameters["funcuid"] = filter.FuncUid

	queryCmd := "select * from \"log\" where \"funcuid\" = $funcuid group by \"pod\""
	query := influxdbClient.NewQueryWithParameters(queryCmd, INFLUXDB_DATABASE, "", parameters)

	response, err := influx.query(query)
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
	var queryCmd string

	// please check "Example 4: Bind a parameter in the WHERE clause to specific tag value"
	// at https://docs.influxdata.com/influxdb/v1.2/tools/api/
	parameters := make(map[string]interface{})
	parameters["funcuid"] = filter.FuncUid
	parameters["time"] = timestamp

	if filter.Pod != "" {
		queryCmd = "select * from \"log\" where \"funcuid\" = $funcuid AND \"pod\" = $pod AND \"time\" > $time ORDER BY time ASC"
		parameters["pod"] = filter.Pod
	} else {
		queryCmd = "select * from \"log\" where \"funcuid\" = $funcuid AND \"time\" > $time ORDER BY time ASC"
	}

	query := influxdbClient.NewQueryWithParameters(queryCmd, INFLUXDB_DATABASE, "", parameters)
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

func (influx InfluxDB) query(query influxdbClient.Query) (*influxdbClient.Response, error) {
	queryURL, err := url.Parse(influx.endpoint)
	if err != nil {
		return nil, err
	}
	// connect to controller first, then controller will redirect our query command
	// to influxdb and proxy back the db response.
	queryURL.Path = fmt.Sprintf("/proxy/%s", INFLUXDB)
	req, err := http.NewRequest("POST", queryURL.String(), nil)
	if err != nil {
		return nil, err
	}

	parametersBytes, err := json.Marshal(query.Parameters)
	if err != nil {
		return nil, err
	}

	// set up http URL query string
	params := req.URL.Query()
	params.Set("q", query.Command)
	params.Set("db", query.Database)
	params.Set("params", string(parametersBytes))
	req.URL.RawQuery = params.Encode()

	httpClient := http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fission.MakeErrorFromHTTP(resp)
	}

	// decode influxdb response
	response := influxdbClient.Response{}
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	if decoder.Decode(&response) != nil {
		return nil, fmt.Errorf("Failed to decode influxdb response: %v", err)
	}
	return &response, nil
}
