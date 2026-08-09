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
	"regexp"
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

	// KEDA's convention is passwordFromEnv, connectionFromEnv: the value is the
	// NAME of an environment variable, not the secret in it. Masking those
	// hides nothing and destroys the first thing anyone checks on a trigger
	// that will not authenticate. Underscores are stripped because the scaler
	// re-spells metadata keys as PASSWORD_FROM_ENV on the consumer.
	if strings.HasSuffix(strings.ReplaceAll(lower, "_", ""), "fromenv") {
		return false
	}

	for _, c := range credentialKeys {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}

// userinfoPassword matches the user:password@ shape of a URL's userinfo.
var userinfoPassword = regexp.MustCompile(`^[^/@\s]*:[^@\s]+@`)

func hasURLPassword(v string) bool {
	if u, err := url.Parse(v); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok {
			return true
		}
	}

	// url.Parse is not a safety oracle here. It reads a schemeless
	// "user:pass@host" as scheme "user" with an opaque body, and it fails
	// outright on an unencoded character in the password — a string that is
	// still perfectly usable by the client library. Neither means the value is
	// safe to ship, so fall back to matching the userinfo shape directly.
	rest := v
	if _, after, found := strings.Cut(v, "://"); found {
		rest = after
	}
	return userinfoPassword.MatchString(rest)
}

// writeNote records something the bundle's reader needs to know but that has no
// object to attach to: why a directory is empty, or why it is incomplete.
//
// console output goes to whoever ran the command; the tarball goes to whoever
// reads it weeks later. Only the second audience is still around when the
// bundle is opened, and a directory alone cannot tell them "nothing to report"
// from "this failed". Written as plain text rather than through writeToFile,
// which would YAML-quote it.
func writeNote(dumpDir, name, format string, args ...any) {
	file := filepath.Join(dumpDir, name)
	if err := os.WriteFile(file, []byte(fmt.Sprintf(format, args...)+"\n"), 0o600); err != nil {
		console.Error(fmt.Sprintf("Error writing note %v: %v", file, err))
	}
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
