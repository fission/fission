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

package console

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
)

var (
	// global Verbosity of our CLI
	Verbosity int
)

func Error(msg any) {
	fmt.Fprintf(os.Stderr, "%v: %v\n", color.RedString("Error"), trimNewline(msg))
}

func Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%v: %v\n", color.RedString("Error"), trimNewline(msg))
}

func Warn(msg any) {
	fmt.Fprintf(os.Stdout, "%v: %v\n", color.YellowString("Warning"), trimNewline(msg))
}

func Info(msg any) {
	fmt.Fprintf(os.Stdout, "%v\n", trimNewline(msg))
}

func Infof(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%v\n", trimNewline(fmt.Sprintf(format, args...)))
}

func Verbose(verbosityLevel int, format string, args ...any) {
	if Verbosity >= verbosityLevel {
		fmt.Printf(format+"\n", args...)
	}
}

func trimNewline(m any) string {
	return strings.TrimSuffix(fmt.Sprintf("%v", m), "\n")
}
