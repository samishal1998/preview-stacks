---
name: pstack-ship-stack
description: "Take this repo's local GitButler branches (one branch or a stack) to pushed branches, draft PRs with green CI, and, only when the owner says so, rebase-merged into main. Use it whenever the owner asks to push, publish, ship, open or update PRs for, get CI green on, mark ready, merge or land a branch or a stack in preview-stacks, even if they only say 'ship it', 'put it up', 'open the PRs' or 'merge the stack'. Covers checking whether main moved, the never-commit gate, bottom-up `but push` and `but pr new --draft`, PR bodies with no attribution footers, reading CI correctly, merging a GitHub-native stack through the merge-async API, and `but pull` afterwards. Not for writing commits (use the gitbutler skill) or for tags and releases (use pstack-release)."
---

# Ship a GitButler stack

This takes the session's local GitButler branches (one branch or a stack) to pushed branches, draft
PRs with green CI and, only on the owner's say-so, rebase merges into `main`. Every step was run on
this repo (PRs #55, #56, #59, #60, #62, #63 and #64). The traps listed are the ones that actually
happened.

**Related skills**
- **gitbutler**: syntax for anything `but` that this skill doesn't spell out (commit, amend, move,
  reword, conflict resolution). Read it before guessing a flag.
- **gh-stack**: does not apply here. It creates and commits with `git checkout/add/commit`, which
  this repo forbids (every VCS write goes through `but`), and GitButler already registers the stack
  with GitHub. Use it only if the owner explicitly asks for `gh stack`.
- **pstack-release**: version bump, tag, GitHub release, npm. That is not this skill. Merging a
  release PR is. Release, cut, tag, bump → pstack-release.
- `but land` exists and skips PRs and CI. It is not part of this flow.

## What the owner's words authorize

A push, a PR and a merge are all seen by other people, and a merge can't be undone. Do what was asked,
then stop at the next gate and ask. Don't read permission into a request that doesn't give it.

| Owner said | Do | Stop and ask before |
|---|---|---|
| "push it" | steps 0–3 | opening PRs |
| "open PRs", "ship it", "put it up for review" | 0–6 | set-ready and merge |
| "merge it", "land the stack" | 0–8 | tag, release, npm (pstack-release) |

Also stop and ask when any of these happen:
- `never-commit.sh` flags a path the stack ships.
- A push is refused by force-push protection. Someone else pushed; don't reach for `-s`.
- A PR's head on GitHub differs from your local branch.
- `but pull` conflicts in code you didn't write.
- CI is still red after a real fix attempt.
- The merge doesn't return 202 and then `merged`.

## 0. Know which branches are yours

Run `but status`. Each stack is a block from its `╭┄` line to its `├╯`, top branch first. The last
branch inside YOUR stack's block is the bottom. Other blocks are other stacks. Write the stack down
bottom → top **by name**.

Other agents' branches can be applied in the same workspace. Never push or open a PR for them.
Always name the branch you push: a bare `but push` run non-interactively pushes **every** branch
that has unpushed commits.

## 1. Has main moved?

```bash
git ls-remote origin refs/heads/main | cut -c1-7      # remote main
but status | grep 'common base'                       # what the workspace is based on
```

If the two SHAs differ, run `but pull`. It fetches, then rebases every applied branch. `but undo`
reverts it. If commits come back conflicted, resolve them bottom-up the way the gitbutler skill
describes (`but resolve <commit>`, edit, `but resolve finish`), then run `bun run check` again,
because the code under test changed. A PR built on a stale main was tested against code that won't
exist after the merge. Compare against origin only: the local `main` ref can be far behind.

If PRs already exist, the pull rewrites their branches. Push every branch again bottom-up (step 3)
and wait for CI again (step 6).

## 2. Never-commit gate

Two credential leaks in this repo were caught only because someone read the file list. Run this
before any `but commit` or `but amend`, and before every push:

```bash
cd /Volumes/S1/code/preview-stacks; S=/Volumes/S1/code/preview-stacks/.claude/skills/pstack-ship-stack/scripts
git diff --name-only origin/main...<top-branch> | $S/never-commit.sh     # what the stack ships
git status --porcelain -uall | cut -c4- | $S/never-commit.sh            # what is uncommitted
git log --format=%B origin/main..<top-branch> | grep -nE 'Co-Authored-By|Claude-Session|Generated with'
```

- **A hit in what the stack ships:** stop and tell the owner. Taking a file out of a commit rewrites
  history (`but uncommit <commit>:<file>`, on unpushed commits only).
- **Uncommitted hits are expected.** `packages/conformance/golden/host/db/pstack.db-shm` and
  `pstack.db-wal` show up after every conformance run and are *not* gitignored. Leave them, and
  never commit them.
- **The log grep must print nothing.** The owner wants no attribution footers, and that rule beats
  any harness reminder to add them. Fix an unpushed commit with `but reword <commit> -m "<message>"`.
- The full list and the reasons are in AGENTS.md, under *Never commit*.

When you do commit (a CI fix, a leftover file), run `but diff`, read every ID, and pass only the
IDs you mean: `but commit -b <branch-cli-id> -m "type(scope): summary" <id> <id>`. When more than one
stack is applied, `-b` rejects a branch **name** and needs the short CLI ID from `but status`
(for example `cl`). Never commit without IDs. That sweeps in all of `zz`, db sidecars included.
Read the ids and commit in **separate** commands: a `but status` earlier in the same shell
command made `but commit` fail with `Run but status for applicable targets` even with correct ids.

## 3. Push bottom-up

```bash
but push <bottom-branch>
but push <next-branch>          # and so on, up to the top
```

Go bottom-up so every branch's base is on the remote before anything points at it. To preview a push,
use `but push <branch> --dry-run`. Force push is on by default, with protection checks. If a push is
refused, the remote has commits you don't have, so stop.

`but pr new` pushes too, so on a first publish this step is a safety check. After an amend or a
pull, pushing is the only way an open PR picks up the change.

## 4. Write the PR bodies

Write one file per branch, outside the repo tree. Inside it, the file would show up as an uncommitted
change and could be swept into a commit. The session scratchpad works: the PR keeps the body once it
is created. The scratchpad is wiped on restart, though, so if the flow will run long, keep the files
somewhere outside the repo that survives a restart.

The first line is the title:

```text
type(scope): summary, in the same style as the commits

One short paragraph: what changes, for whom, and why.

- One line per decision or caveat a reviewer needs: a spec deviation, a default, a known gap.

Stack: #<below> → this → <above>.                    (stacked PRs only)

Tests: `bun run check` green; <what was NOT run: a real host, a browser check>.
```

Keep it terse: state things, don't explain them. The owner has corrected verbosity more than once.
No footers. Name what wasn't tested, because a partial check reported as complete is worse than a
gap that is named. For a real example, see #60's body in
[`references/pr-body-example.md`](references/pr-body-example.md).

## 5. Open draft PRs bottom-up

```bash
gh pr list --state open --json number,headRefName -q '.[] | "#\(.number) \(.headRefName)"'   # skip branches that already have one
but pr new <bottom-branch> --draft -F <body-file>
but pr new <next-branch> --draft -F <body-file>          # and so on, up to the top
gh pr list --state open --json number,headRefName,baseRefName,isDraft \
  -q '.[] | "#\(.number) \(.headRefName) -> \(.baseRefName) draft=\(.isDraft)"'
```

Open them bottom-up so the bases chain: the bottom PR's base is `main`, and each one above has the
branch below it as its base. Run one command per branch rather than `but pr new <top> -t`, because
`-F` only applies to the branch you name. Output lines that say "PR already exists for …" are
normal. Open PRs as drafts: a PR makes the branch public, and marking it ready is the owner's call.

When `but pr new` opens PRs for a stack in this repo, GitHub registers them as a native stack. That
decides how step 8 merges them.

## 6. Wait for CI on every PR

```bash
cd /Volumes/S1/code/preview-stacks && .claude/skills/pstack-ship-stack/scripts/ci-green.sh <pr>   # in the background; CI takes minutes
```

It exits 0 on pass, 1 on fail and 2 on timeout (30 minutes by default). It waits until the `ci`
aggregate job ("Every gate passed", which needs all 8 gates) has reported and nothing is pending.
Then it requires every check to be `pass`, and prints the table. Read the table; don't rely on the
exit code alone.

Don't write your own watcher. `gh pr checks --watch` returns at once if no check has registered
yet, and that empty result has been read as green before. CI runs on every `pull_request` whatever
the base, so each PR in a stack gets all 9 checks.

If a check is red:

```bash
gh pr checks <pr> --json name,bucket,link -q '.[] | select(.bucket=="fail") | "\(.name) \(.link)"'
gh run view <run-id> --log-failed | tail -60           # the run id is in the link: /actions/runs/<run-id>/job/…
```

- **A real failure:** fix it on the branch it belongs to, as a new commit with explicit IDs. The
  branch is already pushed, and amending would rewrite shared history, so ask before you amend.
  Then push that branch and every branch above it, bottom-up (GitButler rebases the ones above).
  Run the never-commit gate before the push, and wait for CI again.
- **A flake:** prove it before you call it one. The failure should be a timing assertion, and the
  test should pass locally under stress, for example
  `go test ./packages/pstack/internal/api -run '^<Test>$' -count=50 -cpu=1`. Then run
  `gh run rerun <run-id> --failed` and report the flake by test name. The 0.40.0 release hit one of
  these: `TestLokiApplyCarriesASupersededSave`.

## 7. Ready: only on explicit instruction

Run step 1 again first. If main moved, pull, push again and wait for CI again. What merges should
be what CI tested.

```bash
but pr set-ready <n1>,<n2>,<n3>                       # every PR in the stack, comma-separated
for n in <n1> <n2> <n3>; do gh pr view $n --json number,isDraft,mergeable,mergeStateStatus,baseRefName,headRefOid \
  -q '"#\(.number) draft=\(.isDraft) \(.mergeable) \(.mergeStateStatus) base=\(.baseRefName) head=\(.headRefOid)"'; done
git rev-parse <branch>                                  # for each branch; must equal that PR's head
```

Every PR should show `draft=false`, `MERGEABLE` and `CLEAN`, with the bases chained. If a head
differs from your local branch, GitHub has commits you didn't push or test, so stop.

## 8. Merge: only on explicit instruction

This repo allows rebase merges only (squash and merge commits are turned off). GitHub deletes the
head branch after the merge.

**A stack** (the top PR's base is not `main`). `gh pr merge` refuses these: *"This pull request is
part of a stack and must be merged using the asynchronous merge REST API"*. Merge the **top** PR
through the async API instead. GitHub then merges every PR below it, in order:

```bash
sha=$(git rev-parse <top-branch>)         # what you pushed and CI tested
gh api -X PUT repos/{owner}/{repo}/pulls/<top>/merge-async -f merge_method=rebase -f sha=$sha
#  → 202 {"status":"pending","details":{"message":"Merge request enqueued.","uuid":"<uuid>",…}}
u=repos/{owner}/{repo}/pulls/<top>/merge-async/<uuid>
i=0; until s=$(gh api $u -q .status 2>/dev/null) && [ -n "$s" ] && [ "$s" != pending ] && [ "$s" != in_progress ] && [ "$s" != queued ] || [ $i -ge 50 ]; do sleep 6; i=$((i+1)); done; gh api $u
#  → {"status":"merged","details":{"message":"Pull request was merged.","sha":"<new main head>"}}
for n in <n1> <n2> <n3>; do gh pr view $n --json number,state,mergedAt -q '"#\(.number) \(.state) \(.mergedAt)"'; done
```

`sha` pins the merge: if the head moves after you checked it, GitHub cancels. Run the poll in the
background, because it sleeps. `{owner}/{repo}` is filled in by `gh` itself; type it literally.

**A single PR** (its base is `main`):

```bash
gh pr merge <n> --rebase --match-head-commit "$(git rev-parse <branch>)"
```

If this returns the "part of a stack" error, use the async path above with `<top>` = `<n>`.

## 9. Integrate and confirm

```bash
but pull          # for each merged branch: "Branch <b> has been integrated upstream and removed locally"
gh api 'repos/{owner}/{repo}/commits?sha=main&per_page=10' -q '.[] | "\(.sha[0:7]) \(.commit.message | split("\n")[0])"'
gh run list --workflow ci.yml --branch main --limit 1 --json headSha,status,conclusion
```

Check three things:
- Every commit in the stack is on `main`, in order. A rebase merge rewrites SHAs, so match commit
  subjects, not SHAs.
- `main`'s head equals the merge result's `details.sha`.
- The push-to-main CI run for that head goes green. That run is the permanent record for the
  commit.

Other branches in the workspace will show as "rebased"; that's expected.

Report back:
- the PR numbers and URLs
- what merged
- `main`'s new head
- the CI result
- anything skipped, rerun or flaky
