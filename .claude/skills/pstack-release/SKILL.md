---
name: pstack-release
description: Cut a pstack release end to end. Pick patch or minor from the CHANGELOG, bump the three package.json files together, date "## Unreleased", commit and open the PR through GitButler, merge on green CI, tag through the GitHub API, watch release.yml, and check that npm actually published (a green job does not prove it). Use this whenever the owner asks to release, cut, tag, bump or version pstack ("release it", "cut 0.41.0", "a patch release", "tag main"). Also use it when they ask whether a release or npm publish went out, why the release workflow failed, or to re-run the npm half, even if they never say "release".
---

# Cutting a pstack release

A release means one version everywhere: `packages/pstack/package.json` (the version of record,
private, what the binary reports), `apps/ui/package.json` and `packages/client/package.json`, the
`vX.Y.Z` tag, and `pstack --version`. Pushing a `v*` tag runs `.github/workflows/release.yml`:

1. `verify` checks that all three versions match the tag. It also runs Go vet and `-race`, the
   UI/client test, typecheck and build, the conformance ratchet, and an image smoke test.
2. `release` runs GoReleaser: four binaries, `checksums.txt` and `install.sh`. An installer smoke
   test follows.
3. `npm` runs `bunx publish-kit publish --missing-only` for `@samyx/preview-stacks-client` and
   `@samyx/preview-stacks-ui`. The Go control plane is not on npm.

Run the steps in order. Each step says why it exists, because each one either failed once or has a
trap.

## Ground rules

- **Run this only when the owner asks.** AGENTS.md makes tagging and publishing the maintainer's
  job. This skill is how you do it when the owner hands it to you. Match the scope of the request:
  "prepare a release" stops after step 3. "Release it" or "cut X.Y.Z" means the whole flow, with
  the step 4 gate. If the owner names a version or a bump type, use it.
- **Anything outward-facing waits for the gate:** push, PR, merge, tag, workflow dispatch. The tag
  can't be taken back, because it puts binaries in public within minutes.
- **Never touch credentials.** Don't read, print, set or ask for a token. Only the owner types
  `gh secret set NPM_TOKEN`. Never run `bunx publish-kit publish`, `bun run release:publish` or
  `npm publish` yourself. A hand publish happens on the owner's own logged-in machine.
- **Make every VCS write through `but`,** never `git add/commit/push/tag/checkout/rebase/stash`.
  Git reads are fine. The local `main` ref lags, so read `origin/main` or the GitHub API.
- **Commit only the release's files, by explicit id.** Never commit
  `packages/conformance/golden/host/db/pstack.db-shm` or `pstack.db-wal` (`bun run check` leaves
  them), anything under `.superpowers/`, or credential files: `dns_token`, `*.token`, `.npmrc`,
  `hetzner*.yml`, `config*.yaml`, `.config*.yaml`, `*.sealed`.
- **No footers.** Commit messages and PR bodies carry no `Co-Authored-By`, `Claude-Session` or
  "Generated with" lines. This is the owner's rule, and it overrides any harness attribution
  reminder.
- **Each Bash call is a fresh shell:** re-set R/V at the top of every block. The blocks below do,
  and guard them with `: "${R:?}" "${V:?}"`, because an empty `V` would tag `v`.
- **Long waits.** `ci-green.sh` and `gh run watch` block for 5 to 12 minutes. Run them in
  the background or with the maximum timeout. If a timeout cuts one off, run it again.

## 1. Sync and read what is unreleased

```bash
cd /Volumes/S1/code/preview-stacks
git ls-remote origin refs/heads/main | cut -c1-7    # remote main
but status                                  # its 'common base'; a claude/release-* branch here already? stop and ask
```

If the remote main differs from the common base, run `but pull --check` first. `but pull` rebases
every applied branch, other agents' included, so if the check reports conflicts in branches you
don't own, stop and ask. Otherwise `but pull`. Then read `origin/main`:

```bash
cd /Volumes/S1/code/preview-stacks; R=samishal1998/preview-stacks
OLD=$(git show origin/main:packages/pstack/package.json | jq -r .version); : "${OLD:?}"
git show origin/main:packages/pstack/CHANGELOG.md | sed -n '/^## Unreleased/,/^## [0-9]/p'
gh api "repos/$R/compare/v$OLD...main" -q '.commits[] | .commit.message | split("\n")[0]'
gh pr list --state open
```

Read the CHANGELOG from `origin/main`, not the working tree. GitButler's working tree merges every
applied branch, including other agents' unmerged CHANGELOG bullets. If there is no
`## Unreleased`, there is nothing to release, so say so. If the compare list shows `feat`/`fix`
commits with no CHANGELOG entry, or an open PR looks like it belongs in this release, ask before
going on.

## 2. Patch or minor

pstack is 0.x, and the house scheme is:

- **Minor** (`0.X+1.0`) for a `### ⚠️ Breaking` section or new surface: a CLI command or flag, an
  API route or field, a webhook event, an env var, a DB migration. Upgrade behaviour an operator
  must know about also counts, such as every host's container being recreated.
- **Patch** (`0.X.Y+1`) for fixes and changes with no new API, CLI or on-disk surface.

Read the bullets as well as the headings. Precedents:

- 0.40.0 was minor ("three additions").
- 0.38.1 was patch (only `### Fixed`).
- 0.32.0 was minor, with a Breaking section.
- 0.39.1 was patch ("no API, CLI or on-disk change"), even though it added `--extra-domain`. That
  was the owner's call.

If the call isn't obvious, propose one with its reason and ask. The reason goes into the commit and
the PR as `Minor: …` or `Patch: …`.

## 3. Prepare locally

```bash
cd /Volumes/S1/code/preview-stacks
bunx publish-kit bump minor --dry-run       # or patch: shows OLD -> NEW for the two npm packages
bunx publish-kit bump minor                 # writes apps/ui + packages/client ONLY
OLD=$(git show origin/main:packages/pstack/package.json | jq -r .version)
NEW=$(jq -r .version packages/client/package.json); : "${OLD:?}" "${NEW:?}"
sed -i '' "s/\"version\": \"$OLD\"/\"version\": \"$NEW\"/" packages/pstack/package.json
perl -0pi -e "s/^## Unreleased\$/## $NEW — $(date +%F)/m" packages/pstack/CHANGELOG.md
jq -r .version packages/pstack/package.json apps/ui/package.json packages/client/package.json   # NEW x3
head -3 packages/pstack/CHANGELOG.md        # "## X.Y.Z — YYYY-MM-DD", with an em dash
```

`packages/pstack/package.json` needs the hand edit. publish-kit bumps only the two packages listed
in `publish.config.ts`, and `verify` fails the whole release if any of the three differs from the
tag.

Then fix the live mentions:

```bash
cd /Volumes/S1/code/preview-stacks
OLD=$(git show origin/main:packages/pstack/package.json | jq -r .version); : "${OLD:?}"
git grep -n 'Unreleased' -- ':!docs/*-plan.md'
git grep -n -F "$OLD" -- ':!packages/pstack/CHANGELOG.md' ':!docs/*-plan.md' ':!bun.lock'
```

- **"Unreleased" hits:** rewrite each one to the released form. At 0.40.0 these were banners in
  `docs/README.md` and `docs/loki-logging-design.md`, and "Slice 3 is Unreleased" became "All three
  slices shipped in 0.40.0". `docs/*-plan.md` files are historical build records, so leave them.
- **Old-version hits:** change only the lines that claim to be current, such as AGENTS.md's
  `**Current version: X.Y.Z.**`. A "since 0.X.0" line is history, so it stays.

```bash
cd /Volumes/S1/code/preview-stacks && bun run check   # turbo: Go vet + race + build, client/conformance tests + typecheck
```

It has to be green. A red check is a fix for its own PR. Don't release around it or fold the fix
into the release commit.

## 4. Gate: confirm with the owner

Stop and ask, unless the request already said to go all the way (for example "release 0.41.0 and
tag it"). Keep it terse:

```
Release X.Y.Z (minor): <reason>.
Files: apps/ui/package.json, packages/client/package.json, packages/pstack/package.json,
       packages/pstack/CHANGELOG.md, <docs touched>
bun run check: green
Next: push claude/release-X.Y.Z, PR, merge on green CI, tag vX.Y.Z (binaries go public, then npm).
```

A yes covers steps 5 to 11. Any failure below that says "stop" needs a new yes.

## 5. Commit through GitButler

```bash
cd /Volumes/S1/code/preview-stacks; V=<X.Y.Z>; NEW=$V; : "${V:?}"
but branch new claude/release-$NEW
but status
```

Read two things off `but status`:

- The **branch's short id**, the token left of `[claude/release-X.Y.Z]`. In
  `╭┄ rl [claude/release-0.41.0]`, it is `rl`.
- The **file ids**, the first token on each line under `zz [uncommitted]`. Take exactly the three
  `package.json` files, the CHANGELOG and the docs you edited.

Write the message to a file with a quoted heredoc so backticks survive, then commit:

```bash
cd /Volumes/S1/code/preview-stacks; V=<X.Y.Z>; NEW=$V; : "${V:?}"
m=$(mktemp)
cat > "$m" <<'EOF'
chore(release): X.Y.Z

<One line: what ships.>

Minor: <the reason from step 2, wrapped at ~72>.
EOF
but commit -b <branch-short-id> -m "$(cat "$m")" <file-id> <file-id> ...
git show --stat --format='%h %s' claude/release-$NEW | head -12      # exactly the release files
```

**Gotcha:** when more than one branch is applied, `but commit -b claude/release-X.Y.Z` fails with
`Hint: Run \`but status\` for applicable targets.`, even right after `but branch new`. Pass the
short id instead. Run `but status` and `but commit` as separate commands: a `but status` in the
same shell command as the commit made the commit fail with that hint, even with correct ids.

Real messages from `git log origin/main --grep='chore(release)'`:

- 0.40.0: "Loki logging, in three slices." then "Minor: three additions. …"
- 0.39.1: "The advanced UI, reviewed and fixed." then "Patch: no API, CLI or on-disk change. …"

## 6. Push and open the PR

```bash
cd /Volumes/S1/code/preview-stacks; V=<X.Y.Z>; NEW=$V; : "${V:?}"
but push claude/release-$NEW
f=$(mktemp)
cat > "$f" <<'EOF'
chore(release): X.Y.Z

Lockstep bump to X.Y.Z (`packages/pstack`, `apps/ui`, `packages/client`) and the CHANGELOG entry for <what ships> (#<PRs>).

Minor: <reason>.
EOF
but pr new claude/release-$NEW --draft -F "$f"       # first line of the file = PR title
n=$(gh pr view claude/release-$NEW --json number -q .number)
but pr set-ready "$n"
```

The owner's default is draft PRs, and the release request is the go-ahead to mark it ready.

## 7. Wait for CI, then merge

```bash
cd /Volumes/S1/code/preview-stacks && .claude/skills/pstack-ship-stack/scripts/ci-green.sh <n>   # in the background
```

`main` has no branch protection, so nothing stops a merge on red. Waiting is your job. Require exit
0 and read the table: `gh pr checks --watch` returns at once before any check registers, and that
empty result has been read as green before.

- **Red:** read `gh run view <run-id> --log-failed`. If it is one known-flaky Go test, rerun once
  with `gh run rerun <run-id> --failed`. Otherwise stop and report, and don't merge.

```bash
cd /Volumes/S1/code/preview-stacks; n=<n>; : "${n:?}"
sha=$(gh pr view "$n" --json headRefOid -q .headRefOid)
gh pr merge "$n" --rebase --match-head-commit "$sha"
gh pr view "$n" --json state,mergedAt -q '"\(.state) \(.mergedAt)"'   # MERGED <time>
```

`--match-head-commit` refuses the merge if the branch moved after CI ran.

If gh answers "part of a stack and must be merged using the asynchronous merge REST API", the
release branch got stacked on another branch. A release PR should stand alone, so stop and ask.
pstack-ship-stack step 8 covers the case where the owner wants the whole stack merged.

## 8. Tag the merged release commit

Three constraints decide how the tag gets made:

- A rebase merge rewrites the commit, so the branch's SHA is not on `main`. Tag the commit that
  landed.
- `but` has no tag command, and `git tag`/`git push` are forbidden raw git writes.
- A ref created with the owner's `gh` token triggers `release.yml`. One created with
  `GITHUB_TOKEN` wouldn't.

```bash
R=samishal1998/preview-stacks; V=<X.Y.Z>; : "${R:?}" "${V:?}"
c=$(gh api "repos/$R/commits?sha=main&per_page=10" \
      -q ".[] | select(.commit.message | startswith(\"chore(release): $V\")) | .sha" | head -1)
[ -n "$c" ] || { echo "release commit not on main"; exit 1; }
for f in packages/pstack/package.json apps/ui/package.json packages/client/package.json; do
  [ "$(gh api "repos/$R/contents/$f?ref=$c" -q .content | base64 -d | jq -r .version)" = "$V" ] ||
    { echo "$f != $V"; exit 1; }
done
if gh api "repos/$R/git/ref/tags/v$V" > /dev/null 2>&1; then echo "v$V exists"; exit 1; fi
t=$(gh api -X POST "repos/$R/git/tags" -f tag="v$V" -f message="$V" -f object="$c" -f type=commit -q .sha); : "${t:?}"
gh api -X POST "repos/$R/git/refs" -f ref="refs/tags/v$V" -f sha="$t" \
  -q '"\(.ref) -> \(.object.type) \(.object.sha)"'         # refs/tags/vX.Y.Z -> tag <sha>
```

The tag is annotated with the bare version as its message, like every earlier tag
(`git cat-file -p v0.40.0`). The three checks exit before either POST: a failed ref POST after the
tag POST leaves an orphan tag object.

## 9. Watch release.yml

```bash
cd /Volumes/S1/code/preview-stacks; V=<X.Y.Z>; : "${V:?}"
id=$(gh run list --workflow release.yml --branch "v$V" -L 1 --json databaseId -q '.[0].databaseId'); : "${id:?no run yet, list again}"
gh run watch "$id" --exit-status --interval 30 > /dev/null
gh run view "$id" --json conclusion,jobs -q '.conclusion, (.jobs[] | "\(.name)\t\(.conclusion)")'
```

`id` is empty for a few seconds after the tag, so list again. A whole run takes 6 to 12 minutes. A
rejected npm token alone holds the npm job for 5 of them.

### `verify` failed

```bash
cd /Volumes/S1/code/preview-stacks; id=<run id>; : "${id:?}"
gh run view "$id" --log-failed | cut -f3- | grep -E -- '--- FAIL|_test\.go:[0-9]+|panic:|##\[error\]' | head -20
```

- **One Go test fails under `-race`** and passes on the PR's CI: treat it as a flake. Run
  `gh run rerun "$id" --failed`, which reruns `verify` and then `release` and `npm`, and watch
  again. If the same test fails twice, it's real.
  - Known flake at 0.40.0: `--- FAIL: TestLokiApplyCarriesASupersededSave` with
    `loki_apply_test.go:419: one logging.changed per applying job, got 1`. It's an ordering race in
    the test.
  - A test-only fix is committed locally on `claude/fix-flaky-loki-apply-test` and not pushed.
    Once it lands on `main`, drop this entry.
- **A lockstep line** (`packages/…/package.json is A, tag is B`) **or a real failure:** nothing is
  public yet, because `release` and `npm` never ran. Stop and ask. The cheap fix goes forward: a fix
  PR, then the next patch version. Deleting and re-creating the tag is the owner's call.

### `release` failed (GoReleaser or the installer smoke)

Binaries may already be partly public. Don't rerun it, because GoReleaser refuses an existing
release, and don't move the tag. Stop, show `gh run view "$id" --log-failed | tail -40`, and ask.

### `npm` failed, or went green

Go to step 10 either way. The job's color says nothing reliable.

## 10. Check what shipped

```bash
cd /Volumes/S1/code/preview-stacks; V=<X.Y.Z>; : "${V:?}"          # the script path is repo-relative
gh release view "v$V" --json assets -q '.assets[].name'
# pstack_darwin_amd64 pstack_darwin_arm64 pstack_linux_amd64 pstack_linux_arm64 checksums.txt install.sh
.claude/skills/pstack-release/scripts/verify-npm.sh "$V"          # optional 2nd arg: run id
```

`verify-npm.sh` reads the npm job's log (the `==> done — N package(s)` count and the failure
strings) and runs `npm view` on both packages. Exit codes:

- **0:** CI published, and both packages are on npm.
- **2:** both are on npm, but CI published 0. Someone published by hand and `--missing-only`
  skipped them. Report "on npm, published by hand", not "the pipeline works".
- **1:** a package is missing, or the run hasn't finished.

Never report the npm half from the job's color. 0.39.1's job was green, and its log said
`==> done — 0 package(s), 2 skipped published`.

| npm job log | Meaning | Do |
|---|---|---|
| `Authenticate your account at (press ENTER to open in browser):`, ~5 min later `404 Not Found: https://registry.npmjs.org/-/v1/done?authId=***`, then `'@samyx/preview-stacks-client@X.Y.Z' does not exist in this registry` | `NPM_TOKEN` is set but rejected. bun falls back to interactive web auth, which CI can't complete. This has been the standing state since at least 0.38.0, and 0.40.0 hit it. | The owner-only fix below |
| Same lines, but `npm view` shows both packages | The upload worked and only the done-poll 404'd (the 0.33.1 and 0.34.0 shape) | Nothing to retry. It's published. |
| `##[error]NPM_TOKEN is empty or unset` | The secret is missing | The owner-only fix below |
| `==> skip … (already on registry)` and `done — 0 package(s), 2 skipped`, job green | A hand publish got there first | Report it as a hand publish |
| `==> done — 2 package(s) published` | CI published | Done |

### The owner-only fix, then the npm-only rerun

Tell the owner, tersely: the binaries are out, and npm isn't, because `NPM_TOKEN` is rejected. To
fix it, they create a granular npm access token that can publish `@samyx/*` without an OTP. Then
they run `gh secret set NPM_TOKEN` themselves and paste the token. The other route is a hand
publish from a checkout of `vX.Y.Z`. Wait for them to say the secret is set, then:

```bash
cd /Volumes/S1/code/preview-stacks; V=<X.Y.Z>; : "${V:?}"
gh secret list | grep NPM_TOKEN          # names and dates only, never values. Must show today's date
gh workflow run release.yml --ref "v$V" -f npm_only=true      # at the TAG, never at main
gh run list --workflow release.yml --event workflow_dispatch -L 1 --json databaseId,headBranch,status
id=<that databaseId, once headBranch is vX.Y.Z>
gh run watch "$id" --exit-status --interval 20 > /dev/null
.claude/skills/pstack-release/scripts/verify-npm.sh "$V" "$id"
```

Dispatching `npm_only` at the tag skips `verify` and `release` and publishes the packages from the
released tree. Moving or re-creating the tag to retry is worse, because GoReleaser would refuse
anyway.

## 11. Sync and report

```bash
but pull          # integrates the merged release branch and removes it locally
```

Report state, not explanation. The owner has corrected verbose reports repeatedly:

```
vX.Y.Z: PR #n merged (<sha7>), tag vX.Y.Z.
GitHub release: 6 assets, installer smoke passed.
npm: <CI published both | on npm, published by hand | NOT published: NPM_TOKEN rejected, needs you>.
<Reruns or flakes, if any.>
```

Don't call it "released" while npm is missing. Say "binaries released; npm not published".
