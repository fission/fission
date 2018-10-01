package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli"
)

func getSessionRV() string {
	fn := os.Getenv("FISSION_SESSION_FILE")
	if len(fn) == 0 {
		return ""
	}
	rv, err := ioutil.ReadFile(fn)
	if err != nil {
		return ""
	}
	return string(rv)
}

func updateSessionRV(newRVstr string) error {
	fn := os.Getenv("FISSION_SESSION_FILE")
	if len(fn) == 0 {
		// we're not tracking the session, no error
		return nil
	}

	// ensure that the new rv is a uint64
	newRV, err := strconv.ParseUint(newRVstr, 10, 64)
	if err != nil {
		return err
	}

	// proceed if new rv is newer than old (or if old doesn't exist or is invalid)
	oldRVstr := getSessionRV()
	var oldRV uint64
	oldRV, err = strconv.ParseUint(oldRVstr, 10, 64)
	// if the existing rv state has an invalid value, we update it anyway
	if err == nil && newRV <= oldRV {
		// nothing to update, "new" isn't new enough
		return nil
	}

	// write to temp and rename file (file renames are usually atomic)
	fnTemp := fn + ".tmp"
	err = ioutil.WriteFile(fnTemp, []byte(newRVstr), 0600)
	if err != nil {
		return err
	}

	return os.Rename(fnTemp, fn)
}

// Get current router rv.  If wantRVstr specified, wait for router to
// catch up to wantRV.
func routerLatestUpdate(rvmURL, wantRVstr string) (string, error) {

	startTime := time.Now()

	// the rv we need, as an int
	var wantRV uint64
	if len(wantRVstr) > 0 {
		var err error
		wantRV, err = strconv.ParseUint(wantRVstr, 10, 64)
		if err != nil {
			return "", err
		}
	}

	for {
		resp, err := http.Get(rvmURL)
		if err != nil {
			return "", err
		}

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		resp.Body.Close()

		haveRVstr := string(body)

		// if we're not waiting, we're done
		if wantRV == 0 {
			return haveRVstr, nil
		}

		// if we're waiting, compare what we have and what we want
		var haveRV uint64
		haveRV, err = strconv.ParseUint(haveRVstr, 10, 64)
		if err != nil {
			return "", err
		}

		// we have all the updates we need, stop waiting
		if haveRV >= wantRV {
			fmt.Printf("waited %v; done\n", time.Since(startTime))
			return haveRVstr, nil
		}

		// timeout, quit
		if time.Since(startTime) > time.Minute {
			return "", errors.New(fmt.Sprintf("Timeout waiting for latest update %v", wantRVstr))
		}

		// wait, repeat
		time.Sleep(500 * time.Millisecond)
	}
}

func adminRouterLatestUpdate(c *cli.Context) error {
	routerURL := getRouterURL(c)
	routerURL = strings.TrimSuffix(routerURL, "/")
	rvmURL := fmt.Sprintf("http://%v/_lastResourceVersion", routerURL)

	wait := false
	if c.Bool("wait") {
		wait = true
	}

	wantRV := getSessionRV()
	if wait && len(wantRV) == 0 {
		msg := "Nothing to wait for, ignoring --wait"
		fmt.Println(msg)
		return errors.New(msg)
	}

	update, err := routerLatestUpdate(rvmURL, wantRV)
	if err != nil {
		msg := fmt.Sprintf("Error getting latest router update: %v", err)
		fmt.Println(msg)
		return errors.New(msg)
	}
	if !wait {
		fmt.Println(update)
	}
	return nil
}
