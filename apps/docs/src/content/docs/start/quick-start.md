---
title: Quick start
description: Install pstack, write a spec with one axis, bring it up, tear it down — and watch it catch a leak.
sidebar:
  order: 1
---

Ten minutes on any Linux or macOS machine with `docker` and the Compose v2 plugin. Every
transcript below is real output from pstack 0.41.0.

## 1. Install

```bash
curl -fsSL https://github.com/samishal1998/preview-stacks/releases/latest/download/install.sh | sh
pstack --help
```

The installer verifies the binary against the release's checksums and moves it into
`/usr/local/bin`. `PSTACK_INSTALL_DIR` changes where; `PSTACK_VERSION=0.41.0` pins a version. It
never uses sudo or edits your `PATH`. More in [Install & first run](/preview-stacks/guide/install-first-run/).

## 2. Write a spec

A spec wraps a Compose project and the resources Compose knows nothing about. This one has a single
**axis**: a directory under `/tmp`, standing in for the database branch or queue namespace a real
preview would need.

`docker-compose.preview.yml`:

```yaml
services:
  web:
    image: nginx:alpine
    profiles: [web]
```

`preview.yml`:

```yaml
version: 1
stack: demo-${PR}

compose:
  file: docker-compose.preview.yml
  profiles: [web]

axes:
  - name: scratch
    up: mkdir -p "/tmp/pstack-demo/$STACK"
    assert_live: test -d "/tmp/pstack-demo/$STACK"
    down: rm -rf "/tmp/pstack-demo/$STACK"
    assert_gone: test ! -e "/tmp/pstack-demo/$STACK"
```

- `stack: demo-${PR}` is the preview's identity. It becomes the Compose project name, and every hook
  sees it as `$STACK`.
- `up` creates the resource, and must be safe to run again. `assert_live` proves it exists.
- `down` destroys it. `assert_gone` proves it is gone — and this is the hook that matters.

Check it. `validate` touches nothing:

```console
$ PR=1 pstack validate
stack: demo-1
✓ spec parses — kind: isolated, 1 axis/axes, stack "demo-1"
  compose: docker-compose.preview.yml [web]
  - scratch: up, down, assert_gone, assert_live
```

Leave `PR` unset and pstack refuses, with exit code `3`, rather than deploying a stack named
`demo-` that every pull request would share.

## 3. Bring it up

```console
$ PR=1 pstack up
stack: demo-1
→ up: scratch
→ compose up (web)
  ✓ up           scratch
  ✓ assert_live  scratch
  ✓ compose      (compose)
```

Axes come up first, top to bottom, then Compose. Add `-v` to see every command and its output, or
`--dry-run` to see the plan without running anything.

## 4. Tear it down

```console
$ PR=1 pstack down
stack: demo-1
→ compose down (all profiles)
→ down: scratch
→ verify (asserting resources are gone)
  ✓ compose      (compose)
  ✓ down         scratch
  ✓ assert_gone  scratch
```

Compose goes first, with every profile the spec lists enabled, so nothing it started is left
running. Then the axes, in reverse. Then `verify`: every `assert_gone` must pass.

## 5. Watch it catch a leak

Break the `down` hook the way a real one breaks — a wrong name:

```diff
-    down: rm -rf "/tmp/pstack-demo/$STACK"
+    down: rm -rf "/tmp/pstack-demo/$STACK-typo"
```

```console
$ PR=1 pstack up
$ PR=1 pstack down
stack: demo-1
→ compose down (all profiles)
→ down: scratch
→ verify (asserting resources are gone)
  ✓ compose      (compose)
  ✓ down         scratch
  ✗ assert_gone  scratch  — LEAKED: resource still present after teardown
  1 leaked resource(s)
$ echo $?
2
```

`✓ down` over `✗ assert_gone` is what a leak looks like. The teardown command succeeded — deleting
something that doesn't exist is not an error — and the resource survived. A hand-written teardown
script would have reported success. Exit code `2` means "torn down, but something leaked", so CI
can treat it differently from an outright failure.

Clean up: `rm -rf /tmp/pstack-demo`.

## Next

- [Concepts](/preview-stacks/start/concepts/) — the model behind what you just ran.
- [Add your first axis](/preview-stacks/guide/add-your-first-axis/) — a real database branch, and the
  values a hook hands to Compose.
- [Prove your teardown works](/preview-stacks/guide/prove-your-teardown-works/) — the drill above, for
  every axis you write.
- [Bootstrap a host](/preview-stacks/operate/bootstrap/) — the control plane: an API, a UI and TLS on
  your own domain, so CI can deploy a preview per pull request.
