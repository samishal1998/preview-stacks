<script setup lang="ts">
/** Preconditions: checked before anything is created, so a failure names a thing, not a stack trace. */
import { dep } from '../../composables/useDeployment';
</script>

<template>
  <section class="panel">
    <h2 class="section" style="margin-bottom: var(--s3)">
      Requires <span class="mute">(checked before anything is created)</span>
    </h2>

    <p v-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>
    <template v-else>
      <p class="hint" style="margin: 0 0 var(--s3)">
        Preconditions run first, in order, and <code>up</code> stops at the first one that fails —
        by name, before a single resource exists. The <code>assert</code> command itself stays on
        the host; only the name and its authored hint are sent here.
      </p>

      <table class="cards">
        <thead>
          <tr>
            <th>requirement</th>
            <th>hint shown on failure</th>
          </tr>
        </thead>
        <tbody class="stagger">
          <tr v-for="(r, i) in dep.detail.requires" :key="r.name" :style="{ '--i': i }">
            <td class="name" data-label="requirement">{{ r.name }}</td>
            <td class="dim" data-label="hint">{{ r.hint || '— (no hint authored)' }}</td>
          </tr>
          <tr v-if="!dep.detail.requires.length">
            <td colspan="2" class="mute">
              None declared — an isolated deployment that borrows shared infrastructure usually
              wants at least one.
            </td>
          </tr>
        </tbody>
      </table>
    </template>
  </section>
</template>
