// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"strings"
)

func IsNotFound(err error) bool {
	return strings.HasSuffix(strings.TrimSpace(err.Error()), "not found")
}
