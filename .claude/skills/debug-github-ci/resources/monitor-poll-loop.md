# Push-fix-monitor loop — the canonical poll script

After pushing a fix, you don't want to busy-poll `gh pr checks` from inside a Bash call (that wastes context and turns). Use the `Monitor` tool with this poll loop so each terminal-state arrives as a notification.

## Standard recipe (copy-paste)

```bash
prev=""
while true; do
  s=$(gh pr checks <PR> --json name,bucket,state 2>/dev/null) || { echo "gh-api-error"; sleep 30; continue; }
  cur=$(jq -r '.[] | select(.name != null) | select(.bucket != "pending") | "\(.name): \(.bucket)"' <<<"$s" | sort)
  comm -13 <(echo "$prev") <(echo "$cur")
  prev=$cur
  if jq -e 'map(select(.name != null)) | all(.bucket != "pending")' <<<"$s" >/dev/null 2>&1; then
    echo "DONE: all checks completed"
    break
  fi
  sleep 30
done
```

## How to invoke it

```
Monitor:
  description: "PR #<N> CI checks — emit each terminal state, exit when all done"
  timeout_ms: 2400000      # 40 minutes — covers a full integration-test sweep
  persistent: false        # exits after DONE
  command: <the script above>
```

## How it works

- Every 30s, it polls `gh pr checks` for the PR.
- For each *newly-non-pending* check (compared to last tick), it emits one stdout line — that becomes one notification.
- When every check has left `pending` state, it prints `DONE` and exits.
- Output volume is bounded: at most 1 line per check transition + 1 final line.

## Why not `gh pr checks --watch`?

`gh pr checks --watch` is a TTY-oriented refresh — it overwrites the screen. The output isn't structured per-event, so the harness can't break it into per-event notifications. The `comm -13` diff approach above gives one event per check transition, which is what we want.

## Why 30s polling?

GitHub Actions check states update with low latency (sub-second), but `gh pr checks` against the API is rate-limited. 30s is a good middle ground: fast enough that you don't sit idle for long after a job completes, slow enough that 40 minutes of polling = 80 API calls (well under the 5000/hour limit).

## Don't poll yourself in parallel

The system reminders are clear: while a Monitor is armed, **do not** also run `gh pr checks` in foreground Bash calls to "check progress". Notifications will arrive on their own. Polling separately wastes context and can confuse you about stale states.

## When a check goes red mid-loop

The Monitor emits `<name>: fail` as soon as that check transitions. You can keep working in the foreground (e.g. start downloading artifacts for that job), but don't push another fix until the loop completes — sometimes a *different* check is pending that gives more diagnostic info, and you want to see it before deciding what's broken.

If you do need to push a fix mid-loop because the failure is unambiguous (e.g. a typo in the previous push), the existing Monitor will continue running against the old run — push, then re-arm a new Monitor with the same recipe pointing at the new run. The old Monitor will eventually complete (all old checks complete) and then exit cleanly.

## Cancelling a stale Monitor

If you've pushed a fix and want to abandon waiting on the previous run:
- Use `TaskStop` with the monitor's task ID (visible in earlier notifications), OR
- Wait for it to time out (default 40 min in the recipe above).

The runs themselves can be cancelled with `gh run cancel <runId>`, but that's rarely needed.
