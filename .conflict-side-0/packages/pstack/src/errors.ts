/**
 * Error types shared across the spec layer.
 *
 * These live outside `spec.ts` for one reason: `subdomains.ts` throws `SpecError`, and `spec.ts`
 * calls into `subdomains.ts` — importing it from `spec.ts` would be a cycle. A cycle over a `class`
 * is worse than over a function, because a class binding is not hoisted: whichever module the
 * runtime evaluates second sees `undefined` at evaluation time, so it "works" only as long as every
 * use stays inside a function body that runs later. That is a trap to walk into, not a design.
 *
 * `spec.ts` re-exports `SpecError`, so every existing import path and every `instanceof` check keeps
 * working — including the one in `api.ts` that maps it to a 400.
 */

/** A spec that will not parse, or will not resolve. Always the caller's problem: mapped to 400. */
export class SpecError extends Error {}
