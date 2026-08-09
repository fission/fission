// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"fmt"

	"github.com/fission/fission/pkg/fission-cli/console"
)

const (
	// listPageSize bounds how much one request pulls. Unpaged, a busy cluster is
	// an unbounded read into CLI memory — and a support dump runs against
	// exactly the clusters that have accumulated the most objects.
	listPageSize = 500

	// maxListPages stops a server that hands out tokens forever. At the page
	// size above this is a million objects, so reaching it means something is
	// wrong rather than large.
	maxListPages = 2000
)

// listAllPages accumulates every page of a paged List.
//
// fetch is called once per page with the continue token, and returns that
// page's items and the token for the next one.
//
// It reports partial separately from err, and callers must not conflate them. A
// Limit without this loop truncates to the first page and says nothing, so a
// short result reads as "there was no more" — and the same trap applies one
// level up: returning collected pages with a nil error would make every caller
// treat a truncated list as complete. Some can live with that and some cannot,
// so the decision belongs to them.
//
// Work is never thrown away. A watch cache ageing out mid-walk returns 410
// Gone, and other transient failures happen too; whatever was already read is
// returned with partial set. Only a failure on the very first page is a hard
// error, because then there is nothing to hand back.
func listAllPages[T any](what string, fetch func(cont string) (items []T, next string, err error)) (
	result []T, partial bool, err error) {
	var out []T
	var cont string

	for page := 0; ; page++ {
		if page >= maxListPages {
			console.Error(fmt.Sprintf(
				"%v list did not terminate after %v pages; the dump has partial %v data", what, page, what))
			return out, true, nil
		}

		items, next, err := fetch(cont)
		if err != nil {
			if page == 0 {
				return nil, false, err
			}
			console.Error(fmt.Sprintf(
				"%v list failed after %v pages; the dump has partial %v data: %v", what, page, what, err))
			return out, true, nil
		}

		out = append(out, items...)

		// Only an empty continue token ends the list. A page may legitimately
		// come back short, or even empty, when the server filters it — so
		// stopping on an empty page would silently truncate. The repeated-token
		// check is what guards against a server that never advances.
		if next == "" || next == cont {
			break
		}
		cont = next
	}

	return out, false, nil
}
