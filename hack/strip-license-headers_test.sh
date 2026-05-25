#!/usr/bin/env bash
# Fixture-based tests for strip-license-headers.py.
set -euo pipefail
cd "$(dirname "$0")/.."
SCRIPT=hack/strip-license-headers.py
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

apache_go() {
  cat <<'EOF'
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

package foo

func Bar() {}
EOF
}

# 1. Go: legacy block removed, code preserved.
apache_go > "$tmp/a.go"
python3 "$SCRIPT" "$tmp/a.go"
head -1 "$tmp/a.go" | grep -q '^package foo$' || fail "go block not stripped"
grep -q 'func Bar' "$tmp/a.go" || fail "go body lost"

# 2. Idempotent: second run is a no-op.
cp "$tmp/a.go" "$tmp/a.go.bak"
python3 "$SCRIPT" "$tmp/a.go"
diff -q "$tmp/a.go" "$tmp/a.go.bak" >/dev/null || fail "go strip not idempotent"

# 3. Hash file (Makefile-style, no shebang): block removed.
{ echo '# Copyright 2017 The Fission Authors.'; echo '#'; \
  echo '# Licensed under the Apache License, Version 2.0 (the "License");'; \
  echo '# limitations under the License.'; echo; echo '.PHONY: all'; } > "$tmp/Makefile"
python3 "$SCRIPT" "$tmp/Makefile"
head -1 "$tmp/Makefile" | grep -q '^.PHONY: all$' || fail "hash block not stripped"

# 4. Shell file with shebang: shebang preserved, block removed.
{ echo '#!/usr/bin/env bash'; echo '# Licensed under the Apache License, Version 2.0 (the "License");'; \
  echo '# limitations under the License.'; echo 'set -e'; } > "$tmp/s.sh"
python3 "$SCRIPT" "$tmp/s.sh"
head -1 "$tmp/s.sh" | grep -q '^#!/usr/bin/env bash$' || fail "shebang lost"
grep -q 'set -e' "$tmp/s.sh" || fail "shell body lost"
grep -q 'Apache License' "$tmp/s.sh" && fail "shell block not stripped" || true

# 5. File without a license block is left untouched.
printf 'package z\n\n// regular comment\nfunc Q() {}\n' > "$tmp/z.go"
cp "$tmp/z.go" "$tmp/z.go.bak"
python3 "$SCRIPT" "$tmp/z.go"
diff -q "$tmp/z.go" "$tmp/z.go.bak" >/dev/null || fail "non-license file modified"

echo "all strip tests passed"
