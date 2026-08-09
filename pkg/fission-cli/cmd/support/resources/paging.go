// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/fission/fission/pkg/fission-cli/console"
)

// listPageSize bounds how much one request pulls. Unpaged, a busy cluster is an
// unbounded read into CLI memory — and a support dump runs against exactly the
// clusters that have accumulated the most objects.
const listPageSize = 500

// listAllPages accumulates every page of a paged List.
//
// fetch is called once per page with the continue token, and returns that
// page's items and the token for the next one. A Limit without this loop would
// be worse than an unbounded read: it truncates to the first page and says
// nothing, so a short result reads as "there was no more".
//
// Two things it refuses to do. It will not spin: a token equal to the one just
// sent, or a fresh token on an empty page, ends the walk. And it will not throw
// away work — a watch cache ageing out mid-pagination returns 410 Gone, so the
// pages already collected are kept and the shortfall is logged, because a
// silently truncated bundle is worse than a noisy one.
func listAllPages[T any](what string, fetch func(cont string) (items []T, next string, err error)) ([]T, error) {
	var out []T
	var cont string

	for page := 0; ; page++ {
		items, next, err := fetch(cont)
		if err != nil {
			if page > 0 && apierrors.IsResourceExpired(err) {
				console.Error(fmt.Sprintf(
					"%v list expired after %v pages; the dump has partial %v data: %v", what, page, what, err))
				break
			}
			return nil, err
		}

		out = append(out, items...)

		if next == "" || next == cont || len(items) == 0 {
			break
		}
		cont = next
	}

	return out, nil
}
