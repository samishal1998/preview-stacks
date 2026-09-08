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

      <table role="table" class="cards">
        <thead role="rowgroup">
          <tr role="row">
            <th role="columnheader">Requirement</th>
            <th role="columnheader">Hint shown on failure</th>
          </tr>
        </thead>
        <tbody role="rowgroup" class="stagger">
          <tr v-for="(r, i) in dep.detail.requires" :key="r.name" role="row" :style="{ '--i': i }">
            <td role="cell" class="name" data-label="requirement">{{ r.name }}</td>
            <td role="cell" class="dim" data-label="hint">{{ r.hint || '— (no hint authored)' }}</td>
          </tr>
          <tr v-if="!dep.detail.requires.length" role="row">
            <td role="cell" colspan="2" class="mute">
              None declared — an isolated deployment that borrows shared infrastructure usually
              wants at least one.
            </td>
          </tr>
        </tbody>
      </table>
    </template>
  </section>
</template>
