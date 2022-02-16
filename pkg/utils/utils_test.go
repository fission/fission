/*
Copyright 2019 The Fission Authors.

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

package utils

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestIsURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"http", "http://example.com", true},
		{"https", "https://example.com", true},
		{"file", "file://example.com", false},
		{"filename", "foobar.zip", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsURL(tt.url); got != tt.want {
				t.Errorf("IsURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetChecksum(t *testing.T) {
	tests := []struct {
		name    string
		src     io.Reader
		want    *fv1.Checksum
		wantErr bool
	}{
		{
			name: "string case",
			src:  bytes.NewReader([]byte("foobar hello world")),
			want: &fv1.Checksum{
				Type: "sha256",
				Sum:  "99936be1902361c29745aef68bd818f5f08246fc695e2d6e4cc474daf79fed32",
			},
			wantErr: false,
		},
		{
			name:    "empty reader",
			src:     nil,
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetChecksum(tt.src)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetChecksum() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetChecksum() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizeString(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "Testing tab escape sequence",
			src:  "This\tis\ta\ttest\t string.",
			want: "Thisisatest string.",
		},
		{
			name: "Testing carriage return",
			src:  "This is a \rtest string. \r This is the second test string\r.",
			want: "This is a test string.  This is the second test string.",
		},
		{
			name: "Testing next line escape sequence",
			src:  "This is a \ntest string. \n This is the second test string\n.",
			want: "This is a test string.  This is the second test string.",
		},
		{
			name: "Testing quotes",
			src:  `This is a "test string"". This is" the second test string "."`,
			want: `This is a \"test string\"\". This is\" the second test string \".\"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeString(tt.src)
			if got != tt.want {
				t.Errorf("SanitizeString() got = %v, want = %v", got, tt.want)
			}
		})
	}
}
