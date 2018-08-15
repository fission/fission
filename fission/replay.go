package main

import (
	"fmt"
	"log"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli"
)

func replay(c *cli.Context) error {
	fc := getClient(c.GlobalString("server"))

	reqUID := c.String("reqUID")
	if len(reqUID) == 0 {
		log.Fatal("Need a reqUID, use --reqUID flag to specify")
	}

	responses, err := fc.ReplayByReqUID(reqUID)
	checkErr(err, "replay records")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	for _, resp := range responses {
		fmt.Fprintf(w, "%v",
			resp,
		)
	}

	w.Flush()

	return nil
}
