# Recovering a plan build

Each recipe below comes from a real failure in the Loki build. Two rules apply to all of them. First,
trust the ledger and `git log` over your memory: the commits the ledger names exist even when your
context no longer remembers them. Second, never re-dispatch a task that has a `Task N: complete` line.

Setup used below:

```bash
cd /Volumes/S1/code/preview-stacks
WS=.superpowers/sdd/<plan-basename>
RUN=~/.claude/projects/-Volumes-S1-code-preview-stacks/<session>/subagents/workflows/wf_<run id>
J=$RUN/journal.jsonl
grep -n 'complete (commits' "$WS/progress.md"            # finished tasks
tail -5 "$WS/progress.md"                                # where it stopped
```

## 1. An agent stalled, a model timed out, or the run died

Symptoms: "agent stalled on all 6 attempts", a network or process death, or a run that ended with no
return value.

Resume the same run with the **identical** args, taken from `$WS/launch-args.json`:

```
Workflow({ scriptPath: "/Volumes/S1/code/preview-stacks/.claude/skills/pstack-plan-build/assets/build.js",
           resumeFromRunId: "<run id>", args: <identical args> })
```

If the run was launched with `script`, not a repo `scriptPath`, use the persisted path its first
result gave. The longest unchanged prefix of agent calls returns from the cache instantly. The first call that
never finished runs live. Any change to args changes the prompts, which are the cache keys, and
re-runs everything from that point.

## 2. Resume is refused ("run has not exited"), even after TaskStop

Start a **new** run of the same script, with `startAt` set to the first task that has no
`Task N: complete` line. Before launching, carry forward what lived only in the stopped run's memory:

- **Breaker carries.** The breaker adds `Carried from Task N: …` rulings to the next task in memory
  only. Copy every `Task N: Ruling: …` line from the ledger that the breaker marked FIX into
  `rulings[N+1]`.
- **Later rulings.** Include any ruling you wrote into the ledger after the first launch.
- **A task in progress.** If the stopped task had already committed, also seed it with `pre` (recipe
  3). Otherwise its implementer starts over on top of those commits.

Save the new args to `$WS/launch-args.json` before launching.

## 3. The implementer committed, but its result was lost

The commits are on the branch, but the journal has no `result` for `impl:TN`. Rebuilding the task
would duplicate the work. Start the new run at that task's **review** instead:

```bash
N=7
BASE=$(git rev-parse <previous task's head>)   # the b in "Task 6: complete (commits a..b" — or the BASE in task-7-report.md
HEAD=$(git rev-parse <branch>)
git log --format=%s $BASE..$HEAD              # the commit subjects
but status                                    # anything of task N left uncommitted? finish it first
```

Launch with `startAt: 7` and:

```json
"pre": { "7": { "status": "DONE", "base": "<BASE>", "head": "<HEAD>",
                "commits": ["feat(x): …"], "tests": "see task-7-report.md", "concerns": [] } }
```

Supply all six fields, with `base` and `head` as full shas and `concerns` as an array. `pre` also works for a task
whose fix loop was cut off. Set `base` to the task's original base and `head` to the current branch
head. The reviewer then re-reviews the whole task range.

If the implementer did return but the run died later, its result is in the journal:

```bash
jq -rs '(map(select(.type=="started")) | map({(.key): .label}) | add) as $l | .[] | select(.type=="result" and $l[.key]=="impl:T7") | .result' "$J"
```

## 4. The implementer died and left uncommitted edits

In slice 1, six Task 12 attempts were cut off by model timeouts before committing. They left about
145 lines of uncommitted edits that compiled cleanly. Don't discard them. Launch a new run with
`startAt: N` and add this ruling to `rulings[N]`:

```
RESUMING AN INTERRUPTED TASK: earlier attempts left UNCOMMITTED edits in <files from `but diff`>.
Start with `but diff` and read them against the brief: keep what is correct, finish what is missing,
fix what is wrong — do not discard them wholesale. BASE is still `git rev-parse <branch>` (nothing
of this task is committed). Commit as you complete each step so an interruption cannot lose the work again.
```

## 5. The run returned `{ stoppedAt: N }`

The ledger's `Task N: STOPPED — …` line says why. Handle it the way SDD handles BLOCKED:

- **Missing context:** add a ruling that supplies it.
- **Plan defect:** rule on it, with the spec as the binding authority, and record the ruling.
- **Task too big:** split it in the plan with a `docs(plan)` commit.

Then launch a new run with `startAt: N`. Stop and ask the owner only when every way forward is a
guess.

## 6. A ledger write killed the run

This is fixed in build.js: every ledger append is `.catch(() => null)`. The cost is that a failed
append leaves a line missing, or a retry leaves a duplicate. Slice 3's ledger has "Task 4: complete"
twice. Repair it by hand. The first message in the failed agent's
transcript, `$RUN/agent-<id>.jsonl`, holds the exact lines; the failed-agents query in SKILL.md
gives the id.

## 7. Specs, plans or results vanished after a session restart

The session scratchpad (`/private/tmp/claude-501/<project>/<session>/scratchpad`) is wiped on
restart. Every agent's structured return survives in the journals:

```bash
grep -l '<a distinctive phrase>' ~/.claude/projects/-Volumes-S1-code-preview-stacks/*/subagents/workflows/wf_*/journal.jsonl
jq -c 'select(.type=="result") | .result' <that journal> > recovered.jsonl
```

To prevent it, write specs, plans and decision logs to `docs/` or to
`~/.claude/projects/-Volumes-S1-code-preview-stacks/<feature>-artifacts/` from the start.

## 8. The fix loop spins on a browser check

The symptom is fix-round ledger lines like `(0 addressed, 1 open — … browser check … not performed;
commits 4a1de79..4a1de79)`. The range is empty because the fixer committed nothing. This happened
five rounds running in both slice 1 T11 and slice 3 T10. Build seats assumed no browser existed, and
a fix round cannot produce evidence the re-reviewer accepts.

build.js now routes these to `outOfBand` when it adjudicates them. The loop can still spin when the
reviewer raises the check as a plain Important finding. In that case:

1. Stop the run (TaskStop).
2. Run the check yourself with `pstack-ui-verify` (headless Chrome over CDP against a local pstack
   with a fake docker). Put the evidence at a durable path, never `/tmp`.
3. Append the screenshot paths and observations to `$WS/task-N-report.md`.
4. Launch a new run with `startAt: N`, `pre` for the task (recipe 3), and this ruling in
   `rulings[N]`: `Browser check performed out-of-band by the controller: <evidence path>; <result>`.

Any defect the check finds becomes an ordinary finding in the task's review.

## 9. A reviewer reports a red test outside the diff

Run the test on a clean copy of the task's base. Use `git archive` into a scratch dir, never `git
checkout`, which fights GitButler:

```bash
D=$(mktemp -d) && git archive <base sha> | tar -x -C "$D" && (cd "$D" && go test -race -count=1 -run <Test> ./packages/pstack/internal/<pkg>/)
```

If it is red at the base too, the failure pre-exists (slice 3 T6: `TestLokiApplyCarriesASupersededSave`
was red on the slice-2 tip). Record it in the ledger as
`Ruling: pre-existing — <test> red at <sha> — not this task's` and carry on. If it fails only
intermittently, reproduce it with `-count=50 -cpu=1` before you blame anything.

## 10. A task commit landed on the wrong branch

After any "-b refused" message, run `but status` and check that the task's commits sit under the
build branch. A typo in `-b` does not fail. `but commit` creates a new unstacked branch with that
name and commits there. Moving a commit is `but move` (the `gitbutler` skill has the syntax). Stop
and ask the owner first if the commit landed on a branch that another agent owns or that has been
pushed.
