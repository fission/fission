// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import "testing"

func TestParseS3Endpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		wantHost string
		wantTLS  bool
	}{
		{"bare host stays plain http", "minio.fission:9000", "minio.fission:9000", false},
		{"https url enables tls and drops scheme", "https://local.s3.endpoint.com:5443", "local.s3.endpoint.com:5443", true},
		{"http url stays plain", "http://minio.fission:9000", "minio.fission:9000", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, secure, err := parseS3Endpoint(c.endpoint)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != c.wantHost {
				t.Errorf("host: got %q, want %q", host, c.wantHost)
			}
			if secure != c.wantTLS {
				t.Errorf("secure: got %t, want %t", secure, c.wantTLS)
			}
		})
	}
}
