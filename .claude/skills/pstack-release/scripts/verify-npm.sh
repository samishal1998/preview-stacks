#!/usr/bin/env bash
# verify-npm.sh X.Y.Z [run-id] — did release.yml's npm job publish X.Y.Z, and is it on npm?
#
# A green npm job is not proof. `publish-kit publish --missing-only` turns "already on the registry"
# into success (0.39.1: "done — 0 package(s), 2 skipped published"), and `npm view` alone cannot
# tell a CI publish from the owner's hand publish. So this reads both: the job log's package count
# and the registry.
#
# run-id defaults to the newest release.yml run for tag vX.Y.Z (a tag push or an npm_only dispatch).
# Exit: 0 CI published and both packages are on npm; 2 both on npm but CI published nothing (hand
# publish, or skipped); 1 anything else (a package missing, run unfinished, no npm job).
set -uo pipefail

v="${1:?usage: verify-npm.sh X.Y.Z [run-id]}"
v="${v#v}"
run="${2:-$(gh run list --workflow release.yml --branch "v$v" -L 1 --json databaseId -q '.[0].databaseId')}"
[ -n "$run" ] || { echo "no release.yml run for v$v"; exit 1; }

status=$(gh run view "$run" --json status -q .status)
[ "$status" = completed ] || { echo "run $run is $status; wait for it to finish"; exit 1; }

job=$(gh run view "$run" --json jobs -q '.jobs[] | select(.name=="npm") | "\(.databaseId) \(.conclusion)"')
echo "run $run, npm job: ${job:-none}"

published=0
rejected=0
if [ -n "$job" ] && [ "${job#* }" != skipped ]; then
  # Log lines are "npm<TAB>step<TAB>[BOM]timestamp text"; keep the text.
  log=$(gh run view "$run" --job "${job%% *}" --log 2>/dev/null | cut -f3- | sed -E 's/^[^0-9]*[0-9]{4}-[0-9-]+T[0-9:.]+Z //')
  printf '%s\n' "$log" | grep -E '==> (publish |skip |done)|Authenticate your account at|/-/v1/done|does not exist in this registry|missing authentication|##\[error\]NPM_TOKEN is empty' | sed 's/^/  log: /'
  published=$(printf '%s\n' "$log" | sed -nE 's/.*==> done — ([0-9]+) package\(s\).*/\1/p' | tail -1)
  published=${published:-0}
  printf '%s\n' "$log" | grep -q 'Authenticate your account at' && rejected=1
fi

missing=0
for p in @samyx/preview-stacks-client @samyx/preview-stacks-ui; do
  if [ "$(npm view "$p@$v" version 2>/dev/null)" = "$v" ]; then
    echo "registry: $p@$v present"
  else
    echo "registry: $p@$v MISSING"
    missing=$((missing + 1))
  fi
done

[ "$rejected" = 1 ] && echo "NPM_TOKEN rejected: bun fell back to interactive web auth. The owner replaces the secret; see SKILL.md."
if [ "$missing" -gt 0 ]; then
  echo "verdict: NOT on npm ($missing of 2 missing)"
  exit 1
elif [ "$published" -gt 0 ]; then
  echo "verdict: CI published $published package(s); both on npm"
  exit 0
else
  echo "verdict: both on npm, but CI published 0 (hand-published or skipped); CI's npm path is unproven"
  exit 2
fi
