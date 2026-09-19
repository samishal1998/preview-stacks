#!/bin/sh
# Flag paths that must never reach a commit (AGENTS.md "Never commit"). Paths on stdin, one per line.
# Prints each hit; exit 1 if any, 0 if none.
#
#   git diff --name-only origin/main...<top-branch> | never-commit.sh    # what the stack ships
#   git status --porcelain -uall | cut -c4- | never-commit.sh            # what is uncommitted
#
# The patterns mirror .gitignore plus the spellings it misses (`.config.<x>.yaml`, the conformance
# db sidecars). The two exclusions are tracked on purpose: Loki's config template and *.tpl.yaml.
hits=$(grep -E '(^|/)(pstack\.db-(shm|wal)|dns[-_]token|[^/]*\.token|\.npmrc|hetzner[^/]*\.yml|[^/]*\.cloud-init\.yml|cloud-init\.[^/]*\.yaml|[^/]*\.sealed|\.?config([-.][^/]*)?\.yaml|\.?config-[^/]*\.json)$|^\.superpowers/|(^|/)\.claude/settings\.local\.json$' |
  grep -vE '/loki/config\.yaml$|\.tpl\.yaml$')
[ -z "$hits" ] && exit 0
printf '%s\n' "$hits" | sed 's/^/never-commit: /'
exit 1
