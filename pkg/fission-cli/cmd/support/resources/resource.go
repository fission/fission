// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/utils"
)

type Resource interface {
	Dump(context.Context, string)
}

func getFileName(dumpdir string, meta metav1.ObjectMeta) string {
	f := fmt.Sprintf("%v/%v_%v_%v.txt", dumpdir, meta.Namespace, meta.Name, meta.ResourceVersion)
	return filepath.Clean(f)
}

func getPodFileName(dumpdir string, pod metav1.ObjectMeta, containerName string) string {
	f := fmt.Sprintf("%v/%v_%v_%v_%v.txt", dumpdir, pod.Namespace, pod.Name, pod.ResourceVersion, containerName)
	return filepath.Clean(f)
}

// credentialKeys are substrings that mark a trigger-metadata key as carrying a
// secret rather than configuration. Matched case-insensitively against the key.
//
// Deliberately not "sasl" on its own: KEDA's Kafka scaler uses sasl and
// saslType for the mechanism name (plaintext, scram_sha512), which is
// diagnostic, not sensitive.
var credentialKeys = []string{
	"password", "passwd", "secret", "token", "credential",
	"apikey", "api_key", "accesskey", "access_key",
	"connectionstring", "connection_string", "privatekey", "private_key",
}

// maskCredentialValues returns a copy of in with credential-bearing values
// replaced by "-", and reports whether anything was masked. Keys are always
// preserved: which settings were present is diagnostic, their values are not.
//
// A value is masked when its key names a credential, or when the value is a URL
// carrying a password in its userinfo — the amqp://user:pass@host form that
// KEDA's rabbitmq and redis scalers take inline.
func maskCredentialValues(in map[string]string) (map[string]string, bool) {
	if in == nil {
		return nil, false
	}

	out := make(map[string]string, len(in))
	masked := false
	for k, v := range in {
		if v != "" && (isCredentialKey(k) || hasURLPassword(v)) {
			out[k] = "-"
			masked = true
			continue
		}
		out[k] = v
	}
	return out, masked
}

func isCredentialKey(k string) bool {
	lower := strings.ToLower(k)
	for _, c := range credentialKeys {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}

func hasURLPassword(v string) bool {
	u, err := url.Parse(v)
	if err != nil || u.User == nil {
		return false
	}
	_, ok := u.User.Password()
	return ok
}

func writeToFile(file string, obj any) {
	bs, err := yaml.Marshal(obj)
	if err != nil {
		console.Error(fmt.Sprintf("Error encoding object: %v", err))
		return
	}

	// Due to unknown reason, the kubernetes objectMeta fields contain
	// empty byte and will fail os.Create/os.Openfile with error message
	// "open <file> invalid argument". To fix the problem, we need to
	// remove the empty byte from string.
	file = string(utils.RemoveZeroBytes([]byte(file)))

	// Diagnostic dump fragments — see dump.go for the surrounding 0o700 dir.
	err = os.WriteFile(file, bs, 0o600)
	if err != nil {
		console.Error(fmt.Sprintf("Error writing file %v: %v", file, err))
	}
}
