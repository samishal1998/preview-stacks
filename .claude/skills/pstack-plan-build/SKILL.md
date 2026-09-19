---
name: pstack-plan-build
description: Build a written pstack implementation plan (docs/*-plan.md with "### Task N:" headings) through an orchestrated subagent-driven build. A read-only pre-flight conflict scan comes first, and its rulings go into a ledger. Then, for each task, an implementer, a task review and up to five fix rounds, and at the end a whole-branch review with one fix wave. The skill bundles the Workflow scripts and the recovery steps for stalled, killed or result-losing runs. Use it whenever the user asks to build, execute, implement, run or continue a plan or a slice in this repo with agents, subagents, a workflow or ultracode, or to resume, rescue or restart a plan build, even if they don't say "workflow". Launch the Workflow tool only when the user has opted into multi-agent orchestration; otherwise follow the single-agent fallback described here.
---

# Orchestrated plan build

This skill takes a spec and a task-by-task plan in this repo to a reviewed, committed GitButler branch.
It is `superpowers:subagent-driven-development` (SDD) driven by a deterministic Workflow script
(`assets/build.js`) instead of by hand, with this repo's rules built in. The Loki logging build
shipped this way in three slices of 14, 16 and 12 tasks. Each slice's pre-flight scan found 21, 14
and 15 conflicts in the plan, several of them blocking, before any code was written.

Read `superpowers:subagent-driven-development` once for the method: the ledger, the four stop
classes, and the rulings format. Read `superpowers:writing-plans` for the plan format. Neither is
restated here. This file covers what the project changes, the exact commands, and what to do when
the harness fails.

## Gate: orchestration or fallback

Launch the Workflow tool **only** when the user opted into multi-agent orchestration: an ultracode
system reminder, or an explicit "use agents / a workflow". A slice ran 2–8 hours and 90–140 agents
(slice 1 needed two runs), so running one uninvited spends a lot of their budget.

Without that opt-in, use the **fallback**: run `superpowers:subagent-driven-development` yourself,
dispatching each seat with the Agent tool. Use the same prompts: the prompt text in
`assets/build.js` is the reference for what each seat gets. If subagents are not wanted at all, use
`superpowers:executing-plans`. Everything below except **Launch** and **Monitor** applies to both
paths.

## What this project changes about SDD

1. **No git worktree.** The GitButler branch is the isolated workspace. The owner routes every VCS
   write through `but`, and worktrees fight GitButler. Skip `superpowers:using-git-worktrees`, even
   though SDD's Setup asks for it.
2. **Commits go through `but commit -b <branch> -m "<type(scope): summary>" <file ids>`, always with
   explicit ids.** With no ids, `but commit` commits every uncommitted change, other agents' work
   included. When more than one stack is applied, `-b <name>` may be refused ("Run but status for
   applicable targets"). Pass the branch's short id from `but status` instead (the `cl` in
   `╭┄ cl [claude/fix-flaky-loki-apply-test]`).
3. **Never commit** `packages/conformance/golden/host/db/pstack.db-{shm,wal}`, anything under
   `.superpowers/`, or credential and sealed files (`dns_token`, `.config*.yaml`, `hetzner*.yml`).
   Two credential leaks were caught by this rule. `.claude/skills/pstack-ship-stack/scripts/never-commit.sh`
   checks a path list.
4. **No attribution footers** in commits (no Co-Authored-By, Claude-Session or "Generated with").
5. **Keep the ledger.** SDD says to `rm -rf` the workspace at the end. Here the ledger is the durable
   record of every ruling the owner may want to undo, so leave it in place.
6. **The build never pushes.** Push, PR, merge, tag and publish happen only when the owner asks, via
   `pstack-ship-stack` (and `pstack-release` for a release).
7. **Copy is terse** (docs/ui-rules.md): state, not explanation. The owner has corrected verbosity
   3+ times. Rule on it before goldens are generated, not after.

## 1. Prerequisites

Resolve the superpowers skills dir once. The version in the path changes when the plugin updates:

```bash
SP=$(ls -d ~/.claude/plugins/cache/claude-plugins-official/superpowers/*/skills | sort -V | tail -1)
cd /Volumes/S1/code/preview-stacks
```

**Spec at a durable path.** Use `docs/<feature>-design.md`, or
`~/.claude/projects/-Volumes-S1-code-preview-stacks/<feature>-artifacts/` for a draft. Never use the
session scratchpad (`/private/tmp/claude-501/...`), which is wiped when the session restarts. Slice
3's plan pointed its spec at a wiped scratchpad path and needed a ruling (S3-R14) to find it again.

**Plan in `docs/`**, written with `superpowers:writing-plans`. It needs a `**Spec:**` line, a
`## Global Constraints` section (build.js hands each seat only that section), and `### Task N: <name>`
headings with Files and Interfaces. Check that every task extracts:

```bash
PLAN=docs/<feature>-plan.md; TOTAL=<n>
for n in $(seq 1 $TOTAL); do "$SP/subagent-driven-development/scripts/task-brief" $PLAN $n || break; done
```

**Branch.** The owner's naming is `<name>/<short-description>`, and the session used `claude/...`.
Stack the branch on an in-flight dependency; otherwise start it from the common base:

```bash
but branch new claude/<feature>-<slice> --anchor <parent-branch>   # stacked
but branch new claude/<feature>                                   # standalone
MERGE_BASE=$(git rev-parse claude/<feature>-<slice>)              # record it NOW, before Task 1
```

`git rev-parse` resolves an empty GitButler branch to its base, so this works for both forms. The
final review diffs `MERGE_BASE..branch`. Take it before any task commits.

**Ledger.** `sdd-workspace` creates `.superpowers/sdd/<plan-basename>/` and a self-ignoring
`.superpowers/sdd/.gitignore` (`*`). Check it held:

```bash
WS=$("$SP/subagent-driven-development/scripts/sdd-workspace" $PLAN)
but status   # no .superpowers path may appear
```

If one appears, ask the owner to add `.superpowers/` to `.git/info/exclude`. Claude Code denies
agent writes under `.git/`.

The ledger's first line is SDD's resume key. Write the header by hand:

```
# SDD ledger — plan: docs/<feature>-plan.md

Spec: <durable path> (binding). Branch: claude/<feature>-<slice>, stacked on <parent> at <MERGE_BASE 7>.
```

**Clean start.** Run `but status`. The only uncommitted entries should be the two golden db
sidecars. `bun run check` should be green at `MERGE_BASE`. If a test was already red before Task 1
(slice 3 found `TestLokiApplyCarriesASupersededSave`), record it in the ledger as pre-existing. It is
not a task's job to fix.

## 2. Pre-flight scan → rulings

Run the read-only scan before Task 1. In the fallback, dispatch one Agent with the same prompt.

```
Workflow({ scriptPath: "/Volumes/S1/code/preview-stacks/.claude/skills/pstack-plan-build/assets/preflight.js",
           args: { plan: "docs/<feature>-plan.md", spec: "<spec path>", specSection: "<optional heading>" } })
```

It returns three things:

- `pairRows`: one row per task pair that shares a file or an interface, showing what one produces
  and the other consumes.
- `taskRows`: one row per task. Would its tests pass against its code? Can its negative controls
  fail?
- `conflicts`: each with the tasks it amends, the plan text, what the spec says, a proposed ruling
  and the cost if wrong.

Turn that into the ledger:

1. Write `## Pre-flight scan` with both tables as markdown (slice 1's ledger is the model). SDD
   requires the rows. A bare "clean" is not a scan.
2. Under `## Pre-flight rulings (controller, <date>)`, rule on **every** conflict, one line each:
   `Ruling: S3-R2 — <what you decided> — <why> — cost if wrong: <cost>`. The spec binds; the plan is
   its argument. A decision the owner recorded in the spec binds too. Prefix the numbers per plan
   (R, S2-R, S3-R) so a line never becomes ambiguous across ledgers.
3. Build `args.rulings` from the same lines, keyed by the conflict's task numbers:
   `{"6": ["Ruling: S3-R2 — …"], "12": ["Ruling: S3-R7 — …"]}`. Use the same text in both places, so
   what a seat was told matches what the owner reads.

Real rulings show what the scan catches:

- A wrong line count that a task's precondition aborts on (S3-R1).
- An edit anchor that an earlier task moves. R10: extract by content, never `sed -n 132,182p`.
- A plan contradicting the spec's failure table (R5).
- A mkdir mode left to the umask (S3-R3).
- Copy made terse before goldens freeze it (R18).

When a ruling changes a number that a re-run of the plan would hit, also fix the plan text in a
separate `docs(plan)` commit. Otherwise the next reader trips over the same thing.

Stop and ask the owner only for SDD's four stop classes: irreversible, security-sensitive, outward
side effects, or a plan so broken that every path is a guess. Rule on everything else and continue.

## 3. Launch

Save the args first. Resuming needs them identical, and a new run needs them edited:

```bash
cat > "$WS/launch-args.json" <<'EOF'
{ "plan": "docs/<feature>-plan.md", "spec": "<spec path>", "branch": "claude/<feature>-<slice>",
  "mergeBase": "<full sha>", "total": 12, "superpowers": "<$SP expanded>",
  "rulings": { "6": ["Ruling: S3-R2 — …"] } }
EOF
```

Then pass the same object as `args`, as a JSON object and never as a string:

```
Workflow({ scriptPath: "/Volumes/S1/code/preview-stacks/.claude/skills/pstack-plan-build/assets/build.js", args: { … } })
```

Every run in the Loki build used a `scriptPath` inside the session's `workflows/scripts/` directory.
If the tool refuses a path in the repo, Read the file and pass its contents as `script`. The tool
result then gives the path it persisted, and that path is the `scriptPath` for resuming. The same
fallback applies to `preflight.js`.

The header of `assets/build.js` documents every argument (`plan, spec, specSection, branch,
mergeBase, total, ws, startAt, rulings, pre, superpowers, repo`). For each task, it runs:

- An implementer.
- An implementer re-dispatch on BLOCKED/NEEDS_CONTEXT.
- A task reviewer.
- An adjudicator for plan conflicts and "cannot verify" items.
- Fix rounds, each followed by a scoped re-review, up to 5 rounds.
- At the cap, a breaker that parks findings or carries a ruling into the next task.

After the last task it runs the final review, one fix wave, a scoped re-review and residual
adjudication. Every decision is appended to the ledger. If you edit build.js or preflight.js, check the syntax.
Plain `node --check` always rejects these files (top-level `return` is illegal outside a function),
so wrap them:

```bash
cd /Volumes/S1/code/preview-stacks; for f in build preflight; do { echo '(async () => {'; sed 's/^export const meta/const meta/' .claude/skills/pstack-plan-build/assets/$f.js; echo '})()'; } > "$TMPDIR/$f.js" && node --check "$TMPDIR/$f.js" && echo "$f ok"; done
```

Agent prompts are the resume cache keys. The same args replay every finished agent for free.
Changing `rulings[n]` re-runs everything from task n's first agent onward, which is what you want.

## 4. Monitor

Wait in bounded stretches of 5–10 minutes. Between them, post one line of status. Don't poll in a
tight loop. The Workflow result gives the run id. The run directory is:

```bash
RUN=$(ls -td ~/.claude/projects/-Volumes-S1-code-preview-stacks/*/subagents/workflows/wf_* | head -1)   # or wf_<run id>
J=$RUN/journal.jsonl
```

```bash
tail -3 "$WS/progress.md"                                              # what the ledger last recorded
# agents started and not yet finished (label + id)
jq -rs '([.[]|select(.type=="result" or .type=="failed")|.key]) as $d | .[] | select(.type=="started") | select(.key as $k | ($d|index($k))|not) | "\(.agentId) \(.label)"' "$J"
# failed agents, by label
jq -rs '(map(select(.type=="started")) | map({(.key): .label}) | add) as $l | .[] | select(.type=="failed") | "failed: \($l[.key]) (\(.agentId))"' "$J"
# implementer contracts so far
jq -r 'select(.type=="result" and (.result|type)=="object" and (.result|has("status"))) | "\(.result.status) \(.result.base[0:7])..\(.result.head[0:7])"' "$J"
# what one agent is doing right now
jq -r 'select(.type=="assistant") | .message.content[] | select(.type=="tool_use") | "\(.name): \(.input.command // .input.file_path // "" | tostring | .[0:120])"' "$RUN/agent-<id>.jsonl" | tail -5
```

Each `agent-<id>.meta.json` names its label in `description`. A `ledger:*` agent that failed is
harmless, because ledger writes are non-fatal. It can leave a duplicate or missing ledger line; fix
that by hand.

## 5. When the harness fails

Read `references/recovery.md` for the exact recipe. The short map:

| Symptom | Recipe |
|---|---|
| "agent stalled on all N attempts", timeouts, the run died | Resume with `resumeFromRunId` and identical args |
| Resume refused: "run has not exited", even after TaskStop | New run with `startAt` = first task with no `Task N: complete` line |
| Implementer committed but its result was lost | `pre` + `startAt`, so the run starts at that task's review |
| Implementer died leaving uncommitted edits | `startAt` + a "RESUMING AN INTERRUPTED TASK" ruling |
| Run returned `{stoppedAt}` | Read the STOPPED line, rule, and relaunch at that task |
| Fix rounds "0 addressed, 1 open" on a browser check, with `a..a` commits | Run it out-of-band with `pstack-ui-verify`, then `pre` |
| Specs, plans or results vanished after a restart | Recover `{"type":"result"}` lines from journal.jsonl |
| A reviewer reports a red test outside the diff | Run it on a `git archive` of the base; if red there, record it as pre-existing |
| A commit landed on the wrong branch | `but status`; `but move`, but ask first if another agent owns that branch |

## 6. Finish

When the run returns, check `final.readyToMerge`, `wave`, `residual` and `outOfBand`:

1. **Out-of-band checks.** For each `outOfBand` entry (ledger lines containing `OUT-OF-BAND`), run the
   check with `pstack-ui-verify` and append the evidence to that task's report, with durable
   screenshot paths. If it finds a defect, that defect gets one fix dispatch and one scoped
   re-review, recorded in the ledger like any finding.
2. **Residuals.** `Final: Ruling (surface to owner)` lines were not implemented. Put them in front of
   the owner. Don't fix them silently.
3. **Verify it yourself** before you claim anything (`superpowers:verification-before-completion`):
   `bun run check` from the repo root. Run `but status` and check that every task commit sits on the
   branch and that nothing on the never-commit list is staged.
4. **Report to the owner**, tersely:
   - **Rulings I made**: every `Ruling:` line, in order, each with its cost if wrong
     (`grep -n 'Ruling' "$WS/progress.md"`). The list is exhaustive; a ruling missing from it was
     made in secret.
   - Parked findings (`grep -n 'parked' …`).
   - The count of deferred minors, and the ones the final review promoted.
   - The out-of-band results.
   - Anything that differs from the spec. The plan's last task usually lists these in the design
     doc's "where the build differs" banner.
5. **Stop.** Leave the ledger in place. Pushing, PRs and merging wait for the owner's word
   (`pstack-ship-stack`).
