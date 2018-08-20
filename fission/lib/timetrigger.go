/*
Copyright 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    tttp://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lib

import (
	"fmt"
	"time"

	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/fission/log"
	"github.com/robfig/cron"
)

func GetAPITimeInfo(client *client.Client) time.Time {
	serverInfo, err := client.ServerInfo()
	if err != nil {
		log.Fatal(fmt.Sprintf("Error syncing server time information: %v", err))
	}
	return serverInfo.ServerTime.CurrentTime
}

func GetCronNextNActivationTime(cronSpec string, serverTime time.Time, round int) error {
	sched, err := cron.Parse(cronSpec)
	if err != nil {
		return err
	}

	fmt.Printf("Current Server Time: \t%v\n", serverTime.Format(time.RFC3339))

	for i := 0; i < round; i++ {
		serverTime = sched.Next(serverTime)
		fmt.Printf("Next %v invocation: \t%v\n", i+1, serverTime.Format(time.RFC3339))
	}

	return nil
}
