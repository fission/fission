// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpapi_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fission/fission/pkg/statestore/httpapi"
	"github.com/fission/fission/pkg/statesvc/stateapi"
)

// TestMaxRequestBytesCoversStatesvc pins the embedded-mode invariant: every
// request the statesvc front door accepts (its own reader bound, which is
// also the admin HMAC verifier's cap) fits through this server's decoder.
// Otherwise a statesvc on the client driver rejects, at the transport, an
// append or KV write it already admitted.
func TestMaxRequestBytesCoversStatesvc(t *testing.T) {
	t.Parallel()
	assert.GreaterOrEqual(t, int64(httpapi.MaxRequestBytes), int64(stateapi.MaxRequestBodyBytes))
}
