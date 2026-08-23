<script setup lang="ts">
/**
 * The server's own words, rendered whole.
 *
 * A pstack error message IS the documentation: a 400 from `PUT /api/deployments/:id` carries the
 * SpecError, which names the offending key or variable, prints the corrected form, and includes
 * the assert-lint warnings about an `assert_gone` that fails open. That text is the tool's main
 * teaching surface.
 *
 * So: never truncate it, never collapse it to one line, never replace it with "invalid spec".
 * Newlines and indentation are preserved, and it is the one element allowed to scroll sideways —
 * re-wrapping a shell snippet changes what it says.
 */
defineProps<{ text: string; title?: string }>();
</script>

<template>
  <div class="banner failed" role="alert">
    <b>{{ title ?? 'The server refused this.' }}</b>
    <pre class="raw">{{ text }}</pre>
  </div>
</template>
