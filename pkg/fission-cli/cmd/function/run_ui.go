// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"
	"io"
	"os"

	"github.com/fatih/color"
	"golang.org/x/term"
)

// Color-coded status output for run-local. Status and progress lines go to
// stderr; the function's own response goes to stdout uncolored, so piping the
// output stays clean. Color is decided from the actual sink (on only when the
// sink is a color-capable terminal) because fatih/color's global keys on stdout,
// which run-local never writes status to — and tests pass a *bytes.Buffer, which
// is not a terminal, so assertions see plain text.

// step prints a progress step — image pull, container start, specialize — in cyan.
func step(w io.Writer, format string, a ...any) { paintln(w, color.FgCyan, format, a...) }

// success prints a positive milestone — serving, reloaded — in green.
func success(w io.Writer, format string, a ...any) { paintln(w, color.FgGreen, format, a...) }

// fail prints a recoverable failure — a reload or watch error — in red.
func fail(w io.Writer, format string, a ...any) { paintln(w, color.FgRed, format, a...) }

// note prints secondary information — kept containers, request id, log delimiters —
// dimmed so it reads as background to the milestones above.
func note(w io.Writer, format string, a ...any) { paintln(w, color.FgHiBlack, format, a...) }

func paintln(w io.Writer, attr color.Attribute, format string, a ...any) {
	fmt.Fprintln(w, paint(w, attr, fmt.Sprintf(format, a...)))
}

// paint wraps s in attr's ANSI codes when w is a color-capable terminal sink,
// and returns it unchanged otherwise.
func paint(w io.Writer, attr color.Attribute, s string) string {
	if !colorEnabled(w) {
		return s
	}
	c := color.New(attr)
	c.EnableColor() // override fatih/color's stdout-based global; we gate on w
	return c.Sprint(s)
}

// colorEnabled reports whether w is a terminal that should receive ANSI color
// (honoring the NO_COLOR convention). A non-file sink — e.g. a test buffer or a
// redirected file — never gets color.
func colorEnabled(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(f.Fd()))
}
