#!/bin/sh
# Wait for CI on one PR; exit 0 only on a real pass.
#
#   ci-green.sh <pr-number> [timeout-seconds]    exit 0 pass, 1 fail, 2 timeout
#
# Why this exists: `gh pr checks --watch` returns at once when no check has registered yet (right
# after a push), and a watcher that prints nothing was read as green more than once. So: keep
# polling until the `ci` aggregate job (ci.yml, "Every gate passed") has reported and nothing is
# pending, then require every bucket to be "pass". Run it in the background; CI takes minutes.
pr=${1:?usage: ci-green.sh <pr-number> [timeout-seconds]}
limit=${2:-1800}
start=$(date +%s)
while :; do
  # gh exits 1 with no output before any check exists and 8 while pending; stdout is what counts.
  state=$(gh pr checks "$pr" --json name,bucket -q '
    if length == 0 or (any(.[]; .name == "ci") | not) or any(.[]; .bucket == "pending") then "wait"
    elif all(.[]; .bucket == "pass") then "pass"
    else "fail" end' 2>/dev/null)
  case $state in pass|fail) break ;; esac
  if [ $(( $(date +%s) - start )) -ge "$limit" ]; then
    echo "PR #$pr: timed out after ${limit}s"; gh pr checks "$pr" 2>&1; exit 2
  fi
  sleep 15
done
gh pr checks "$pr" 2>&1 | awk -F'\t' '{print $2 "\t" $1}'
echo "PR #$pr: $state"
[ "$state" = pass ]
