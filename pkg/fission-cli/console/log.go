// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
		fmt.Println(sanitizeLogLine(fmt.Sprintf(format, args...)))
	}
}

// sanitizeLogLine strips trailing newlines and replaces any embedded
// CR/LF with a literal backslash-r/-n so external data can't inject
// fake log lines (CWE-117).
func sanitizeLogLine(s string) string {
	s = strings.TrimSuffix(s, "\n")
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	r := strings.NewReplacer("\r", "\\r", "\n", "\\n")
	return r.Replace(s)
}

// trimNewline preserves the existing API for callers that only need
// to drop a trailing newline; it now also neutralises embedded
// CR/LF the same way sanitizeLogLine does.
func trimNewline(m any) string {
	return sanitizeLogLine(fmt.Sprintf("%v", m))
}
