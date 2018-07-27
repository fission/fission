package main

import (
	"fmt"
	"os"
)

func logErrorAndExit(err interface{}) {
	os.Stderr.WriteString(fmt.Sprintf("%v\n", err))
	os.Exit(1)
}
