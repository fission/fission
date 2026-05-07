#!/usr/bin/env bash
# Summarise a Harness SAST SARIF file. Usage: .security-fixes/sarif-summary.sh [path] [ruleId]
set -euo pipefail
SARIF="${1:-output.sarif}"
[[ -f "$SARIF" ]] || { echo "no SARIF at $SARIF" >&2; exit 2; }

echo "== severity =="
jq -r '.runs[].results[] | .level // "?"' "$SARIF" | sort | uniq -c

echo
echo "== findings by rule (CVSS desc) =="
jq -r '
  (.runs[].tool.driver.rules // []) as $rules
  | (.runs[].results // []) as $res
  | $rules | map({key: .id, value: (.properties["security-severity"] // "0")}) | from_entries as $sev
  | $res
  | group_by(.ruleId)
  | map({rule: .[0].ruleId, count: length, cvss: ($sev[.[0].ruleId] // "?")})
  | sort_by(.cvss | tonumber? // 0) | reverse
  | .[] | "\(.cvss)\t\(.count)\t\(.rule)"
' "$SARIF"

echo
echo "== top 20 packages =="
jq -r '.runs[].results[].locations[0].physicalLocation.artifactLocation.uri // "?"' "$SARIF" \
  | awk -F/ 'NF>=2{print $1"/"$2} NF==1{print $1}' | sort | uniq -c | sort -rn | head -20

echo
echo "== distinct sites for a rule (pass rule id as arg 2) =="
if [[ -n "${2:-}" ]]; then
  jq -r --arg r "$2" '.runs[].results[] | select(.ruleId==$r)
    | "\(.locations[0].physicalLocation.artifactLocation.uri):\(.locations[0].physicalLocation.region.startLine)"' "$SARIF" \
    | sort -u
fi
