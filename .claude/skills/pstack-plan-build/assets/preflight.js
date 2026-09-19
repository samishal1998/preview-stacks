export const meta = {
  name: 'pstack-plan-preflight',
  description: 'Read-only pre-flight conflict scan of a task-by-task plan against its spec: a task-pair table, a per-task self-consistency table, and conflicts with proposed rulings',
  whenToUse: 'Before build.js runs a plan: its conflicts become the ledger rulings and args.rulings',
  phases: [{ title: 'Scan', detail: 'one read-only reader over the whole plan and spec' }],
}

// args — a JSON object:
//   plan         string  required  repo-relative plan, e.g. 'docs/loki-logging-slice-3-plan.md'
//   spec         string  required  the binding spec (durable path)
//   specSection  string  optional  heading inside `spec` when it covers more than this plan
//   repo         string  optional  default '/Volumes/S1/code/preview-stacks'
// Returns { pairRows, taskRows, conflicts }. The controller writes both tables to the ledger and rules on every conflict.

for (const k of ['plan', 'spec']) if (!args || !args[k]) throw new Error(`args.${k} is required`)
const REPO = args.repo || '/Volumes/S1/code/preview-stacks'
const SECTION = args.specSection ? `, section "${args.specSection}"` : ''

const SCHEMA = {
  type: 'object',
  properties: {
    pairRows: { type: 'array', items: { type: 'object', properties: { tasks: { type: 'string' }, shared: { type: 'string' }, produced: { type: 'string' }, consumed: { type: 'string' }, finding: { type: 'string' } }, required: ['tasks', 'shared', 'produced', 'consumed', 'finding'] } },
    taskRows: { type: 'array', items: { type: 'object', properties: { task: { type: 'string' }, selfConsistent: { type: 'boolean' }, finding: { type: 'string' } }, required: ['task', 'selfConsistent', 'finding'] } },
    conflicts: { type: 'array', items: { type: 'object', properties: { tasks: { type: 'array', items: { type: 'string' } }, what: { type: 'string' }, planText: { type: 'string' }, specSays: { type: 'string' }, proposedRuling: { type: 'string' }, costIfWrong: { type: 'string' } }, required: ['tasks', 'what', 'planText', 'specSays', 'proposedRuling', 'costIfWrong'] } },
  },
  required: ['pairRows', 'taskRows', 'conflicts'],
}

phase('Scan')
return await agent(`Pre-flight scan for subagent-driven execution. READ-ONLY: edit nothing, no git or but writes.
Repo: ${REPO}. Plan: ${args.plan} (read it all). Spec (binding authority): ${args.spec}${SECTION}. Repo rules: AGENTS.md (its Invariants bind every task). Check every claim against the CURRENT tree — the plan was written against an older one.
Produce:
1. pairRows — ONE row for EVERY pair of tasks that share a file or an interface: the two tasks, the shared file/interface, what one produces vs what the other consumes (exact names, signatures and strings: constants, exported functions, CLI/UI output lines a later golden or test asserts, command strings a fake docker answers, JSON keys, edit anchors — quoted text an earlier task moves or rewrites — and line-number references a later task invalidates), and what you found (consistent / mismatch with detail).
2. taskRows — ONE row per task: does its own text agree with itself (would the tests it specifies pass against the code it specifies? does each negative control actually fail its test? the files it creates vs the files it later touches; are its commands correct for this repo — go test -race -timeout 120s, bun run check, go generate for internal/apicli, golden regeneration)?
3. conflicts — every contradiction between tasks, with the plan's Global Constraints, with AGENTS.md, or with the spec; and anything the plan mandates that a reviewer would treat as a defect (a test asserting nothing, verbatim duplicated logic, a negative control that cannot fail, explanatory UI copy where docs/ui-rules.md wants terse state). For each: the task numbers it amends (strings, e.g. ["3","9"]), the plan text, what the spec says, a proposed ruling (the spec binds; the plan is its argument), and what it costs if wrong.
Be exhaustive and concrete; cite plan line numbers. "The scan is clean" without the rows is not a scan.`, { label: 'preflight', phase: 'Scan', schema: SCHEMA, model: 'opus' })
