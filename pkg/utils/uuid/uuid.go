// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package uuid

import (
	"uuid"

	"k8s.io/apimachinery/pkg/types"
)

// NewV4 is pinned explicitly: the stdlib's uuid.New is only "at this time"
// equivalent to NewV4 and may change algorithm in a future release.

func NewUUID() types.UID {
	return types.UID(uuid.NewV4().String())
}

func NewString() string {
	return uuid.NewV4().String()
}
