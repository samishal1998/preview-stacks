export const meta = {
  name: 'pstack-plan-build',
  description: 'Subagent-driven build of one written plan: each task implement → task review → up to 5 fix rounds, then a whole-branch review and one fix wave',
  whenToUse: 'A spec and a task-by-task plan exist, the GitButler branch is created, and the ledger holds the pre-flight rulings (see .claude/skills/pstack-plan-build/SKILL.md)',
  phases: [
    { title: 'Tasks', detail: 'implementer + task reviewer + fix loop, one task at a time' },
    { title: 'Final review', detail: 'whole-branch review, one fix wave, scoped re-review, adjudication' },
  ],
}

// args — pass a JSON object, never a JSON-encoded string:
//   plan         string  required  repo-relative plan, e.g. 'docs/loki-logging-slice-3-plan.md'. Needs a
//                                  '## Global Constraints' section and '### Task N:' headings (task-brief keys on them).
//   spec         string  required  the binding spec. A durable path (docs/ or ~/.claude/projects/...), never the
//                                  session scratchpad, which is wiped on restart.
//   specSection  string  optional  heading inside `spec` when it covers more than this plan, e.g. 'Slice 1'.
//   branch       string  required  the GitButler branch the tasks commit to, e.g. 'claude/loki-logging-grafana'.
//   mergeBase    string  required  sha the branch started from (`git rev-parse <branch>` before Task 1).
//   total        number  required  number of tasks in the plan.
//   ws           string  optional  ledger dir. Default: <repo>/.superpowers/sdd/<plan basename>.
//   startAt      number  optional  first task to run (default 1). Earlier tasks need a 'Task N: complete' ledger line.
//   rulings      object  optional  { "3": ["Ruling: R4 — …", …] }. Keys are STRING task numbers. Each line overrides
//                                  the brief where they conflict, and is repeated to every seat of that task.
//   pre          object  optional  { "7": { status: 'DONE', base, head, commits: [], tests: '', concerns: [] } }.
//                                  A task whose implementer committed but whose result was lost: the run skips
//                                  that implementer and starts at the review. All six fields; base and head as full shas.
//   superpowers  string  optional  superpowers skills dir. Default below; the installed one is
//                                  `ls -d ~/.claude/plugins/cache/claude-plugins-official/superpowers/*/skills | sort -V | tail -1`.
//   repo         string  optional  default '/Volumes/S1/code/preview-stacks'.
// Returns { results, final, wave, rere, residual, outOfBand }, or { stoppedAt, results, outOfBand } when a task
// cannot proceed (the ledger's 'STOPPED' line says why).
//
// Every seat runs on opus (slices 2 and 3 of the Loki build did); ledger appends are mechanical and run on haiku.
// Agent prompts are the resume cache keys: the same args replay every finished agent, and changing
// rulings[n] re-runs from task n's first agent onward.

for (const k of ['plan', 'spec', 'branch', 'mergeBase', 'total']) {
  if (!args || args[k] == null || args[k] === '') throw new Error(`args.${k} is required (see the header of build.js)`)
}

const REPO = args.repo || '/Volumes/S1/code/preview-stacks'
const PLAN = args.plan
const SPEC = args.spec
const SECTION = args.specSection ? `, section "${args.specSection}"` : ''
const SKILL = args.superpowers || '/Users/samimishal/.claude/plugins/cache/claude-plugins-official/superpowers/6.3.0/skills'
const SDD = `${SKILL}/subagent-driven-development`
const WS = args.ws || `${REPO}/.superpowers/sdd/${PLAN.split('/').pop().replace(/\.md$/, '')}`
const LEDGER = `${WS}/progress.md`
const BRANCH = args.branch
const MERGE_BASE = args.mergeBase
const RULINGS = args.rulings || {}
const TOTAL = Number(args.total)
const GC = `awk '/^## Global Constraints/{p=1;print;next} p&&/^## /{exit} p' ${PLAN}`
const OUT_OF_BAND = []

const COMMON = `Repo: ${REPO} (work from there). Plan: ${PLAN}. Spec (binding authority): ${SPEC}${SECTION}. Branch: ${BRANCH} (GitButler).
Global constraints: read ONLY the plan's "## Global Constraints" section: \`${GC}\`. Pre-flight rulings that AMEND the plan are in the ledger ${LEDGER} under "## Pre-flight rulings"; the ones for your task are repeated below and override the brief where they conflict.
Hard rules: GitButler only for VCS writes — \`but status\`, then \`but commit -b ${BRANCH} -m "<type(scope): summary>" <file ids>\` naming this task's file ids explicitly (with no ids, but commits EVERY uncommitted change). If \`-b ${BRANCH}\` is refused ("Run but status for applicable targets"), pass the branch's short id from \`but status\` instead. Never git add/commit/push/checkout/rebase/stash/reset. Never commit packages/conformance/golden/host/db/*, anything under .superpowers/, or credential/sealed files (dns_token, .config*.yaml, hetzner*.yml). No Co-Authored-By or other attribution footers. Do not push, open PRs, delete branches or run destructive commands; do not dispatch subagents. UI/CLI copy is terse: state, not explanation (docs/ui-rules.md). When a brief asks for a real-browser check, follow ${REPO}/.claude/skills/pstack-ui-verify/SKILL.md if it exists and put the screenshot paths in your report.`

const IMPL = { type: 'object', properties: { status: { type: 'string', enum: ['DONE', 'DONE_WITH_CONCERNS', 'BLOCKED', 'NEEDS_CONTEXT'] }, base: { type: 'string' }, head: { type: 'string' }, commits: { type: 'array', items: { type: 'string' } }, tests: { type: 'string' }, concerns: { type: 'array', items: { type: 'string' } } }, required: ['status', 'base', 'head', 'commits', 'tests', 'concerns'] }
const REVIEW = { type: 'object', properties: { specCompliant: { type: 'boolean' }, qualityApproved: { type: 'boolean' }, findings: { type: 'array', items: { type: 'object', properties: { severity: { type: 'string', enum: ['Critical', 'Important', 'Minor'] }, text: { type: 'string' }, conflictsWithPlanText: { type: 'boolean' } }, required: ['severity', 'text', 'conflictsWithPlanText'] } }, cannotVerify: { type: 'array', items: { type: 'string' } }, packagePath: { type: 'string' } }, required: ['specCompliant', 'qualityApproved', 'findings', 'cannotVerify', 'packagePath'] }
const RERE = { type: 'object', properties: { verdicts: { type: 'array', items: { type: 'object', properties: { finding: { type: 'string' }, addressed: { type: 'boolean' } }, required: ['finding', 'addressed'] } }, newBreakage: { type: 'array', items: { type: 'object', properties: { severity: { type: 'string', enum: ['Critical', 'Important', 'Minor'] }, text: { type: 'string' } }, required: ['severity', 'text'] } }, outOfScope: { type: 'array', items: { type: 'string' } } }, required: ['verdicts', 'newBreakage', 'outOfScope'] }
const ADJ = { type: 'object', properties: { decisions: { type: 'array', items: { type: 'object', properties: { finding: { type: 'string' }, action: { type: 'string', enum: ['fix', 'park', 'dismiss', 'outOfBand'] }, ruling: { type: 'string' } }, required: ['finding', 'action', 'ruling'] } } }, required: ['decisions'] }
const LOG = { type: 'object', properties: { ok: { type: 'boolean' } }, required: ['ok'] }

// Non-fatal: a ledger helper that returned no StructuredOutput once killed a whole run.
const ledger = (lines, label, phase = 'Tasks') => agent(`Append these exact lines to ${LEDGER} (create nothing else, change nothing else). Use python3 so no shell quoting mangles them: open the file in append mode and write each line followed by a newline. Lines (JSON array): ${JSON.stringify(lines)}`, { label: `ledger:${label}`, phase, schema: LOG, model: 'haiku', effort: 'low' }).catch(() => null)

// Re-reviewers sometimes return a finding's text shortened or truncated.
const matches = (x, f) => x.finding === f.text || (x.finding && f.text.startsWith(x.finding)) || x.finding.startsWith(f.text.slice(0, 60))

const rulingsFor = n => (RULINGS[String(n)] || []).join('\n') || '(none)'

// A check the build seats cannot perform (a real-browser check) is never a code gap. Sent into the fix loop it spun
// five rounds with no commit twice (Loki slice 1 T11, slice 3 T10); the controller runs it after the run instead.
const OOB_RULE = `Action "outOfBand" is for a check only the controller can run after the build — a manual or real-browser UI check the implementer did not perform or left without screenshot evidence. It is never "fix": the fix loop cannot produce that evidence and spins without a commit. Say in the ruling exactly what to check.`
const verb = a => (a === 'outOfBand' ? 'OUT-OF-BAND' : a.toUpperCase())
const noteOutOfBand = (n, decs) => { for (const d of decs) if (d.action === 'outOfBand') OUT_OF_BAND.push({ task: n, finding: d.finding, ruling: d.ruling }) }

async function runTask(n) {
  const tag = `T${n}`
  // 1. implement (args.pre short-circuits a task whose implementer committed but whose result was lost)
  let impl = (args.pre && args.pre[String(n)]) || await agent(`${COMMON}

You are the IMPLEMENTER for Task ${n} of ${TOTAL} of the plan ${PLAN}. Your instructions are the template ${SDD}/implementer-prompt.md — read it first and follow it exactly (TDD, self-review, report format), filling its placeholders from here.
- Brief (read this next — it is your requirements, with the exact values to use verbatim): run \`${SDD}/scripts/task-brief ${PLAN} ${n}\` from ${REPO}; it prints the brief path.
- Before changing anything record BASE: \`git rev-parse ${BRANCH}\`. After your last commit record HEAD the same way.
- Rulings for this task (override the brief where they conflict):
${rulingsFor(n)}
- Report file: ${WS}/task-${n}-report.md (full report there).
Return the short contract: status, base, head, commit subjects, one-line test summary, concerns.`, { label: `impl:${tag}`, phase: 'Tasks', schema: IMPL, model: 'opus' })

  if (impl && (impl.status === 'BLOCKED' || impl.status === 'NEEDS_CONTEXT')) {
    await ledger([`Task ${n}: implementer ${impl.status} — ${impl.concerns.join(' / ')}; re-dispatching a fresh implementer with the prior report`], `${tag}-blocked`)
    impl = await agent(`${COMMON}

You are a more capable IMPLEMENTER taking over Task ${n} of ${TOTAL}; a prior implementer reported ${impl.status}: ${JSON.stringify(impl.concerns)}. Read ${WS}/task-${n}-report.md (if present) for what was tried. Follow ${SDD}/implementer-prompt.md. Brief: \`${SDD}/scripts/task-brief ${PLAN} ${n}\`. The spec ${SPEC} is the binding authority; where the plan is wrong, decide against the spec, say so in the report as "Ruling: <what> — <why> — <cost if wrong>". Rulings for this task:
${rulingsFor(n)}
Record BASE before changing anything (\`git rev-parse ${BRANCH}\`) — if the earlier attempt committed, use the BASE it recorded in the report. Report file: ${WS}/task-${n}-report.md (append).`, { label: `impl2:${tag}`, phase: 'Tasks', schema: IMPL, model: 'opus' })
    if (!impl || impl.status === 'BLOCKED' || impl.status === 'NEEDS_CONTEXT') {
      await ledger([`Task ${n}: STOPPED — implementer still ${impl ? impl.status : 'no result'}: ${impl ? impl.concerns.join(' / ') : ''}`], `${tag}-stop`)
      return { stopped: true, n, impl }
    }
  }
  if (!impl) { await ledger([`Task ${n}: STOPPED — implementer returned nothing`], `${tag}-stop`); return { stopped: true, n } }

  const base = impl.base
  let head = impl.head

  // 2. task review
  const review = await agent(`${COMMON}

You are the TASK REVIEWER for Task ${n} of ${TOTAL}. Your instructions are ${SDD}/task-reviewer-prompt.md — read and follow it exactly.
- Generate your review package: from ${REPO} run \`${SDD}/scripts/review-package ${PLAN} ${base} ${head}\` and Read the file it prints (commit list, stat, full diff).
- Brief: run \`${SDD}/scripts/task-brief ${PLAN} ${n}\` and read the printed path.
- Implementer report: ${WS}/task-${n}-report.md.
- Binding constraints (your attention lens): the plan's Global Constraints section (\`${GC}\`) plus these pre-flight rulings for this task, which amend the brief:
${rulingsFor(n)}
Return the verdicts as structured data: specCompliant, qualityApproved, findings (severity; conflictsWithPlanText=true when the finding contradicts what the brief's text requires), cannotVerify items, and the package path.`, { label: `review:${tag}`, phase: 'Tasks', schema: REVIEW, model: 'opus' })

  if (!review) { await ledger([`Task ${n}: STOPPED — reviewer returned nothing (commits ${base.slice(0, 7)}..${head.slice(0, 7)})`], `${tag}-stop`); return { stopped: true, n } }

  const minors = review.findings.filter(f => f.severity === 'Minor')
  if (minors.length) await ledger(minors.map(f => `Task ${n}: minor (deferred): ${f.text}`), `${tag}-minor`)

  let open = review.findings.filter(f => f.severity !== 'Minor')
  if (!review.specCompliant && open.length === 0) open.push({ severity: 'Important', text: 'Reviewer marked spec non-compliant without an itemised finding — re-check the brief against the diff and fix what is missing.', conflictsWithPlanText: false })

  // plan-conflict findings and cannot-verify items → controller-grade adjudication before the loop
  const needsRuling = open.filter(f => f.conflictsWithPlanText)
  if (needsRuling.length || review.cannotVerify.length) {
    const adj = await agent(`${COMMON}

You are the CONTROLLER'S ADJUDICATOR for Task ${n}. You hold cross-task context: read the ledger ${LEDGER} (pre-flight table + rulings), the brief (\`${SDD}/scripts/task-brief ${PLAN} ${n}\`), the spec section, and read neighbouring task briefs with the same script when a finding spans tasks. The review package is ${review.packagePath}.
(a) Findings that conflict with plan text — decide each with the SPEC as binding authority: action "fix" (it enters the fix loop; your ruling text tells the fixer exactly what to do) or "dismiss" (the plan/code stands). (b) "Cannot verify from diff" items — check each yourself in the tree; action "fix" if it is a real gap, "dismiss" if verified fine. ${OOB_RULE}
Every decision needs a ruling in the form "<what you decided> — <why> — <what it costs if wrong>".
Plan-conflict findings: ${JSON.stringify(needsRuling)}
Cannot-verify items: ${JSON.stringify(review.cannotVerify)}`, { label: `adjudicate:${tag}`, phase: 'Tasks', schema: ADJ, model: 'opus' })
    // No adjudicator result: park the conflicts and cannot-verify items with a ledger line rather than drop them silently.
    const decs = adj ? adj.decisions : [...needsRuling.map(f => f.text), ...review.cannotVerify].map(t => ({ finding: t, action: 'park', ruling: 'adjudicator returned nothing — parked unresolved' }))
    noteOutOfBand(n, decs)
    if (decs.length) await ledger(decs.map(d => d.action === 'park' ? `Task ${n}: parked — ${d.finding} — Ruling: ${d.ruling}` : `Task ${n}: Ruling: ${d.finding} — ${verb(d.action)}: ${d.ruling}`), `${tag}-rulings`)
    const conflictTexts = new Set(needsRuling.map(f => f.text))
    open = open.filter(f => !conflictTexts.has(f.text))
    for (const d of decs) if (d.action === 'fix') open.push({ severity: 'Important', text: `${d.finding} — RULING: ${d.ruling}`, conflictsWithPlanText: false })
  }

  // 3. fix loop
  let round = 0
  let fixBase = head
  while (open.length && round < 5) {
    round++
    const framing = round <= 3
      ? `You are the IMPLEMENTER for Task ${n}, fix round ${round}/5.`
      : `A prior implementer attempted Task ${n}'s fixes ${round - 1} times; you own it now. Read the report file for what was tried.`
    const fix = await agent(`${COMMON}

${framing} Follow ${SDD}/implementer-prompt.md's fix-round rules: fix every open finding below, re-run the tests covering the amended code, APPEND a fix report to ${WS}/task-${n}-report.md naming the covering test files, the exact command and its output, commit with GitButler, and return the short contract (base = the head before your fix: ${fixBase}; head = \`git rev-parse ${BRANCH}\` after your commit).
Brief: \`${SDD}/scripts/task-brief ${PLAN} ${n}\`. Rulings for this task:
${rulingsFor(n)}
OPEN FINDINGS (verbatim):
${open.map((f, i) => `${i + 1}. [${f.severity}] ${f.text}`).join('\n')}`, { label: `fix${round}:${tag}`, phase: 'Tasks', schema: IMPL, model: 'opus' })
    if (!fix) break
    const newHead = fix.head
    const rr = await agent(`${COMMON}

You are the SCOPED RE-REVIEWER for Task ${n}, fix round ${round}. Instructions: ${SDD}/re-review-prompt.md — read and follow it exactly.
- Fix diff package: from ${REPO} run \`${SDD}/scripts/review-package ${PLAN} ${fixBase} ${newHead}\` and read the printed file.
- Brief: \`${SDD}/scripts/task-brief ${PLAN} ${n}\`. Report file (fix reports appended at the end): ${WS}/task-${n}-report.md.
- Findings to verdict:
${open.map((f, i) => `${i + 1}. [${f.severity}] ${f.text}`).join('\n')}
Return verdicts (addressed true/false per finding, same text), new breakage in the fix diff only, and out-of-scope observations.`, { label: `rereview${round}:${tag}`, phase: 'Tasks', schema: RERE, model: 'opus' })
    const stillOpen = rr ? open.filter(f => { const v = rr.verdicts.find(x => matches(x, f)); return !v || !v.addressed }) : open
    const breakage = rr ? rr.newBreakage.filter(b => b.severity !== 'Minor').map(b => ({ severity: b.severity, text: `(fix-round ${round} breakage) ${b.text}`, conflictsWithPlanText: false })) : []
    const deferred = rr ? [...rr.outOfScope.map(o => `Task ${n}: minor (deferred): ${o}`), ...rr.newBreakage.filter(b => b.severity === 'Minor').map(b => `Task ${n}: minor (deferred): ${b.text}`)] : []
    const addressedCount = open.length - stillOpen.length
    open = [...stillOpen, ...breakage]
    await ledger([`Task ${n}: fix round ${round}/5 (${addressedCount} addressed, ${open.length} open — ${open.map(f => f.text.slice(0, 90)).join(' | ') || 'none'}; commits ${fixBase.slice(0, 7)}..${newHead.slice(0, 7)})`, ...deferred], `${tag}-round${round}`)
    fixBase = newHead
    head = newHead
  }

  // 4. breaker
  if (open.length) {
    const adj = await agent(`${COMMON}

You are the CONTROLLER'S ADJUDICATOR at the fix-loop cap for Task ${n}: five rounds left these findings open. Read the ledger ${LEDGER}, the brief (\`${SDD}/scripts/task-brief ${PLAN} ${n}\`), the report ${WS}/task-${n}-report.md, and the current code. For each: "park" (reviewer wrong/contestable, or real but nothing downstream builds on it) or "fix" (real and load-bearing — give the SMALLEST change that unblocks later tasks; it will be carried into the next task's dispatch). ${OOB_RULE} Rulings as "<what> — <why> — <cost if wrong>".
Open findings: ${JSON.stringify(open)}`, { label: `breaker:${tag}`, phase: 'Tasks', schema: ADJ, model: 'opus' })
    const decs = adj ? adj.decisions : open.map(f => ({ finding: f.text, action: 'park', ruling: 'adjudicator returned nothing — parked unresolved' }))
    noteOutOfBand(n, decs)
    await ledger(decs.map(d => d.action === 'park' ? `Task ${n}: parked — ${d.finding} — Ruling: ${d.ruling}` : `Task ${n}: Ruling: ${d.finding} — ${verb(d.action)}: ${d.ruling}`), `${tag}-breaker`)
    // Carried rulings live only in this run's memory; a new run with startAt must re-add them from the ledger.
    const carry = decs.filter(d => d.action === 'fix').map(d => `Carried from Task ${n}: ${d.ruling}`)
    if (carry.length && n < TOTAL) RULINGS[String(n + 1)] = [...(RULINGS[String(n + 1)] || []), ...carry]
    await ledger([`Task ${n}: complete (commits ${base.slice(0, 7)}..${head.slice(0, 7)}, ${decs.length} parked/ruled)`], `${tag}-done`)
  } else {
    await ledger([`Task ${n}: complete (commits ${base.slice(0, 7)}..${head.slice(0, 7)}, review clean${round ? ` after ${round} fix round(s)` : ''})`], `${tag}-done`)
  }
  return { n, base, head, rounds: round }
}

phase('Tasks')
const results = []
for (let n = (args.startAt || 1); n <= TOTAL; n++) {
  const r = await runTask(n)
  results.push(r)
  if (r && r.stopped) return { stoppedAt: n, results, outOfBand: OUT_OF_BAND }
  log(`Task ${n} complete`)
}

phase('Final review')
const FINAL = { type: 'object', properties: { readyToMerge: { type: 'string' }, findings: { type: 'array', items: { type: 'object', properties: { severity: { type: 'string', enum: ['Critical', 'Important', 'Minor'] }, text: { type: 'string' } }, required: ['severity', 'text'] } }, deferredTriage: { type: 'array', items: { type: 'string' } }, packagePath: { type: 'string' } }, required: ['readyToMerge', 'findings', 'deferredTriage', 'packagePath'] }
// This repo's plans end with a full-gate task (CHANGELOG, docs, `bun run check`), so its report carries the gate evidence.
const fin = await agent(`${COMMON}

You are the FINAL WHOLE-BRANCH CODE REVIEWER for the plan ${PLAN}. Instructions: ${SKILL}/requesting-code-review/code-reviewer.md — read and follow it. Package: from ${REPO} run \`${SDD}/scripts/review-package ${PLAN} ${MERGE_BASE} $(git rev-parse ${BRANCH})\` and read the printed file. Requirements: the spec${SECTION} and the plan. Triage the ledger's "minor (deferred)" and "parked" lines (${LEDGER}) — say which must be fixed before merge (promote them to findings). Also confirm \`cd ${REPO} && bun run check\` evidence exists in the last task's report (${WS}/task-${TOTAL}-report.md); run it yourself only if missing.`, { label: 'final-review', phase: 'Final review', schema: FINAL, model: 'opus' })

let wave = null, rere = null, residual = null
const blocking = fin ? fin.findings.filter(f => f.severity !== 'Minor') : []
if (fin) await ledger([`Final review: ready=${fin.readyToMerge}; ${fin.findings.length} findings (${blocking.length} Critical/Important)`, ...fin.findings.filter(f => f.severity === 'Minor').map(f => `Final: minor (deferred): ${f.text}`)], 'final', 'Final review')
if (blocking.length) {
  wave = await agent(`${COMMON}

You are the FINAL FIX implementer for the plan ${PLAN}. Fix ALL of these whole-branch review findings in one wave (TDD where behaviour changes), run the covering tests and \`cd ${REPO} && bun run check\`, append a report to ${WS}/final-fix-report.md with commands and output, commit with GitButler. Record base = \`git rev-parse ${BRANCH}\` BEFORE changing anything and head after.
FINDINGS:
${blocking.map((f, i) => `${i + 1}. [${f.severity}] ${f.text}`).join('\n')}`, { label: 'final-fix', phase: 'Final review', schema: IMPL, model: 'opus' })
  if (wave) {
    rere = await agent(`${COMMON}

You are the SCOPED RE-REVIEWER of the final fix wave. Instructions: ${SDD}/re-review-prompt.md. Package: \`${SDD}/scripts/review-package ${PLAN} ${wave.base} ${wave.head}\`. Report: ${WS}/final-fix-report.md. Findings:
${blocking.map((f, i) => `${i + 1}. [${f.severity}] ${f.text}`).join('\n')}`, { label: 'final-rereview', phase: 'Final review', schema: RERE, model: 'opus' })
    const left = rere ? blocking.filter(f => { const v = rere.verdicts.find(x => matches(x, f)); return !v || !v.addressed }) : blocking
    const brk = rere ? rere.newBreakage.filter(b => b.severity !== 'Minor') : []
    if (left.length || brk.length) {
      residual = await agent(`${COMMON}

You are the CONTROLLER'S ADJUDICATOR for residual final-review findings (no second fix wave exists). Read the ledger and code. For each: "park" with a ruling, or "fix" with the smallest change stated (it will be surfaced to the owner, not implemented now). ${OOB_RULE} Rulings as "<what> — <why> — <cost if wrong>".
Residual: ${JSON.stringify([...left, ...brk])}`, { label: 'final-adjudicate', phase: 'Final review', schema: ADJ, model: 'opus' })
      if (residual) {
        noteOutOfBand('final', residual.decisions)
        await ledger(residual.decisions.map(d => `Final: ${d.action === 'park' ? 'parked' : d.action === 'outOfBand' ? 'OUT-OF-BAND' : 'Ruling (surface to owner)'} — ${d.finding} — Ruling: ${d.ruling}`), 'final-residual', 'Final review')
      }
    }
    await ledger([`Final fix wave: commits ${wave.base.slice(0, 7)}..${wave.head.slice(0, 7)}`], 'final-wave', 'Final review')
  }
}
return { results, final: fin, wave, rere, residual, outOfBand: OUT_OF_BAND }
