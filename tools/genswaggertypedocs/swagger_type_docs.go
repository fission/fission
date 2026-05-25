// SPDX-FileCopyrightText: The Kubernetes Authors
//
// SPDX-License-Identifier: Apache-2.0

// lifted from k8s.io/kubernetes so we can add methods to types
package main

import (
	"fmt"
	"io"
	"os"

	kruntime "k8s.io/apimachinery/pkg/runtime"

	flag "github.com/spf13/pflag"
)

var (
	functionDest = flag.StringP("func-dest", "f", "-", "Output for swagger functions; '-' means stdout (default)")
	typeSrc      = flag.StringP("type-src", "s", "", "From where we are going to read the types")
	verify       = flag.BoolP("verify", "v", false, "Verifies if the given type-src file has documentation for every type")
)

func main() {
	flag.Parse()

	if *typeSrc == "" {
		fmt.Println("Please define -s flag as it is the source file")
		os.Exit(1)
	}

	var funcOut io.Writer
	if *functionDest == "-" {
		funcOut = os.Stdout
	} else {
		file, err := os.Create(*functionDest)
		if err != nil {
			fmt.Printf("Couldn't open %v: %v", *functionDest, err)
			os.Exit(1)
		}
		defer file.Close()
		funcOut = file
	}

	docsForTypes := kruntime.ParseDocumentationFrom(*typeSrc)

	if *verify {
		rc, err := kruntime.VerifySwaggerDocsExist(docsForTypes, funcOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error in verification process: %s\n", err)
		}
		os.Exit(rc)
	}

	if len(docsForTypes) > 0 {
		if err := kruntime.WriteSwaggerDocFunc(docsForTypes, funcOut); err != nil {
			fmt.Fprintf(os.Stderr, "Error when writing swagger documentation functions: %s\n", err)
			os.Exit(-1)
		}
	}
}
