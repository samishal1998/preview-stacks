<script setup lang="ts">
/**
 * Catch-all route.
 *
 * Exists so a mistyped or stale deep link renders something with a way out, rather than the blank
 * screen a SPA gives you by default when no route matches.
 */
import { useRoute } from 'vue-router';

const route = useRoute();
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Not found</h1>
        <div class="sub">Nothing is routed at <code>{{ route.fullPath }}</code>.</div>
      </div>
    </div>

    <section class="panel">
      <p class="mute">
        If you followed a link to a deployment that has since been forgotten, it will not resolve —
        the control plane only knows deployments that are still in its registry.
      </p>
      <div class="row" style="margin-top: 12px; gap: 8px">
        <RouterLink class="btn" to="/">Dashboard</RouterLink>
        <RouterLink class="btn" to="/deployments">Deployments</RouterLink>
        <RouterLink class="btn" to="/jobs">Jobs</RouterLink>
      </div>
    </section>
  </div>
</template>
