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

package log

import (
	"fmt"
	"os"
)

var (
	// Verbosity is the global Verbosity of our CLI
	Verbosity int
	// IsCliRun is only set to true when running from CLI
	IsCliRun bool
)

//TODO switch to logrus

//Fatal logs a message to stderr and, if running as CLI, exits with error code 1
//TODO remove this function and refactor all calling code to return errors instead
func Fatal(msg interface{}) {
	str := fmt.Sprintf("%v\n", msg)
	os.Stderr.WriteString(str)
	if IsCliRun {
		os.Exit(1)
	}
	//If we have got here we are running as SDK not CLI and the caller is not yet safe to use in SDK setting.
	//Because it has not been refactored to return errors instead of calling log.Fatal or CheckErr, it will
	//continue to run without exiting and cause unexpected results
	Warn(fmt.Sprintf("Unsafe usage of sdk code outside CLI setting. Caller that generated following error needs error handling refactor: %v", str))
}

//Warn logs a message to stderr
func Warn(msg interface{}) {
	os.Stderr.WriteString(fmt.Sprintf("%v\n", msg))
}

//Warnf logs a formatted message to stderr
func Warnf(format string, args ...interface{}) {
	os.Stderr.WriteString(fmt.Sprintf(format, args))
}

//Info logs a formatted message to stdout
func Info(msg interface{}) {
	Verbosef(1, fmt.Sprint(msg))
}

//Infof logs a formatted message with verbosity 1 to stdout
func Infof(format string, args ...interface{}) {
	Verbosef(1, format, args)
}

//Verbosef logs a formatted message with custom verbosity to stdout
func Verbosef(verbosityLevel int, format string, args ...interface{}) {
	//See main.go. CLI verbosity (0 is quiet, 1 is the default, 2 is verbose.)
	if Verbosity >= verbosityLevel {
		fmt.Printf(format+"\n", args...)
	}
}
