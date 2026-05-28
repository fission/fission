#!/usr/bin/env bash
# issue-pr-scrub orchestrator. Portable across OSS repos via --repo.
#
#   scrub.sh doctor                         prerequisites check
#   scrub.sh sync   --repo owner/name [--full]   gitcrawl sync into local SQLite
#   scrub.sh extract --repo owner/name
#   scrub.sh triage  --repo owner/name
#   scrub.sh report  --repo owner/name
#   scrub.sh run    --repo owner/name [--full]   sync -> extract -> triage -> report
#   scrub.sh labels --repo owner/name [--create-missing --execute]
#   scrub.sh protect --repo owner/name [--from FILE] [--execute]   keep-open keepers
#   scrub.sh apply  --repo owner/name --auto|--from FILE [--execute] [--max N]
#
# Read-only EXCEPT the write-capable commands: `apply --execute`,
# `protect --execute`, and `labels --create-missing --execute`.
# `run` never writes to GitHub.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PY="python3"

die() { echo "error: $*" >&2; exit 1; }

# Pull --repo / --full out of the args; pass the rest through to the python stage.
REPO=""; FULL=0; WITH_COMMENTS=0; PASS=()
parse_common() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --repo) REPO="$2"; shift 2;;
      --full) FULL=1; shift;;
      --with-comments) WITH_COMMENTS=1; shift;;
      *) PASS+=("$1"); shift;;
    esac
  done
}

need_repo() { [ -n "$REPO" ] || die "--repo owner/name is required"; }

# NB: bash 3.2 (macOS default) errors on "${arr[@]}" when empty under set -u,
# so guard every array expansion with the "${arr[@]+...}" idiom.
stage_py() { "$PY" "$HERE/$1" --repo "$REPO" ${PASS[@]+"${PASS[@]}"}; }

cmd_doctor() {
  echo "== prerequisites =="
  for b in gitcrawl gh python3 sqlite3; do
    if command -v "$b" >/dev/null 2>&1; then echo "  ok   $b ($(command -v "$b"))"; else echo "  MISS $b"; fi
  done
  echo "== python =="; "$PY" -c 'import sys,tomllib,sqlite3; print("  ok   python", ".".join(map(str,sys.version_info[:3])))' \
    || echo "  MISS python 3.11+ with tomllib"
  echo "== gh auth =="; gh auth status 2>&1 | sed 's/^/  /' || echo "  MISS gh not authenticated"
  echo "== gitcrawl =="; gitcrawl doctor 2>/dev/null | grep -E '"version"|github_token_present|openai_key_present' | sed 's/^/  /' || echo "  (gitcrawl not initialized; run: gitcrawl init)"
}

cmd_sync() {
  need_repo
  command -v gitcrawl >/dev/null || die "gitcrawl not installed (brew install openclaw/tap/gitcrawl)"
  # Metadata-only by default (fast); add --with-comments to hydrate comment bodies.
  local comments_flag=""
  [ "$WITH_COMMENTS" -eq 1 ] && comments_flag="--include-comments"
  # First-time/full backfill vs incremental (plain sync sweeps recently-closed).
  if [ "$FULL" -eq 1 ]; then
    echo ">> full backfill: gitcrawl sync $REPO --state all $comments_flag"
    gitcrawl sync "$REPO" --state all $comments_flag
  else
    echo ">> incremental: gitcrawl sync $REPO $comments_flag"
    gitcrawl sync "$REPO" $comments_flag
  fi
}

cmd_run() {
  need_repo
  echo "### scrub run: $REPO"
  cmd_sync
  stage_py extract.py
  stage_py triage.py
  stage_py report.py
  echo "### done. Review the report, then: scrub.sh apply --repo $REPO --auto"
}

main() {
  [ $# -ge 1 ] || { sed -n '2,20p' "$HERE/scrub.sh" | sed 's/^# \{0,1\}//'; exit 1; }
  local sub="$1"; shift
  parse_common "$@"
  case "$sub" in
    doctor)  cmd_doctor;;
    sync)    cmd_sync;;
    extract) need_repo; stage_py extract.py;;
    triage)  need_repo; stage_py triage.py;;
    report)  need_repo; stage_py report.py;;
    run)     cmd_run;;
    labels)  need_repo; stage_py labels_sync.py;;
    protect) need_repo; stage_py protect.py;;
    apply)   need_repo; stage_py apply.py;;
    *) die "unknown subcommand: $sub";;
  esac
}

main "$@"
