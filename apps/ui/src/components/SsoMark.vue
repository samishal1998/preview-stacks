<script setup lang="ts">
/**
 * The identity-provider brand marks, drawn inline.
 *
 * Inline and bundled ON PURPOSE: the login page is the first thing a browser loads on a fresh
 * host, and fetching marks from a brand CDN would make signing in depend on a third party (and
 * tell that third party about every visit). So each mark is an SVG in this file, in its brand
 * colour — except GitHub and the generic key, whose marks are drawn in black-or-white and
 * therefore use `currentColor` to follow the theme.
 *
 * `preset` is the preset key the API reports (`config.provider`, or `preset` in the health
 * summary). Anything unrecognised — `custom`, a bare OIDC issuer's `''` — gets the generic key,
 * so a provider nobody drew a mark for still renders as *a* provider rather than as a gap.
 *
 * The Keycloak mark is a simplification (their hexagon-and-chevrons icon rebuilt from basic
 * shapes), not traced brand art; the rest follow the providers' published marks.
 */
withDefaults(defineProps<{ preset: string; size?: number }>(), { size: 18 });
</script>

<template>
  <!-- GitHub: the octocat mark ships in black or white only, so it follows the text colour. -->
  <svg v-if="preset === 'github'" :width="size" :height="size" viewBox="0 0 16 16" aria-hidden="true">
    <path
      fill="currentColor"
      d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"
    />
  </svg>

  <!-- Google: the 'G', four colours, per the sign-in branding assets. -->
  <svg v-else-if="preset === 'google'" :width="size" :height="size" viewBox="0 0 18 18" aria-hidden="true">
    <path fill="#4285F4" d="M17.64 9.2c0-.64-.06-1.25-.16-1.84H9v3.48h4.84c-.21 1.13-.84 2.08-1.8 2.72v2.26h2.92c1.7-1.57 2.68-3.88 2.68-6.62z" />
    <path fill="#34A853" d="M9 18c2.43 0 4.47-.8 5.96-2.18l-2.92-2.26c-.8.54-1.84.86-3.04.86-2.34 0-4.32-1.58-5.03-3.71H.96v2.33A8.997 8.997 0 0 0 9 18z" />
    <path fill="#FBBC05" d="M3.97 10.71A5.41 5.41 0 0 1 3.68 9c0-.59.1-1.17.28-1.71V4.96H.96A8.996 8.996 0 0 0 0 9c0 1.45.35 2.82.96 4.04l3.01-2.33z" />
    <path fill="#EA4335" d="M9 3.58c1.32 0 2.5.45 3.44 1.35l2.58-2.58C13.46.89 11.43 0 9 0A8.997 8.997 0 0 0 .96 4.96l3.01 2.33C4.68 5.16 6.66 3.58 9 3.58z" />
  </svg>

  <!-- GitLab: the single-colour tanuki. -->
  <svg v-else-if="preset === 'gitlab'" :width="size" :height="size" viewBox="0 0 24 24" aria-hidden="true">
    <path
      fill="#FC6D26"
      d="m23.6 9.593-.033-.086L20.3.98a.851.851 0 0 0-.336-.405.875.875 0 0 0-1 .054.875.875 0 0 0-.29.44l-2.207 6.748H7.537L5.33 1.07a.858.858 0 0 0-.29-.441.875.875 0 0 0-1-.054.86.86 0 0 0-.336.405L.437 9.502l-.032.086a6.066 6.066 0 0 0 2.012 7.01l.01.009.03.021 4.977 3.727 2.462 1.863 1.5 1.132a1.009 1.009 0 0 0 1.22 0l1.499-1.132 2.461-1.863 5.006-3.75.013-.01a6.068 6.068 0 0 0 2.005-7.002Z"
    />
  </svg>

  <!-- Bitbucket. -->
  <svg v-else-if="preset === 'bitbucket'" :width="size" :height="size" viewBox="0 0 24 24" aria-hidden="true">
    <path
      fill="#2684FF"
      d="M.778 1.213a.768.768 0 0 0-.768.892l3.263 19.81c.084.5.515.868 1.022.873H19.95a.772.772 0 0 0 .77-.646l3.27-20.03a.768.768 0 0 0-.768-.891zM14.52 15.53H9.522L8.17 8.466h7.561z"
    />
  </svg>

  <!-- Microsoft: the four squares. -->
  <svg v-else-if="preset === 'microsoft'" :width="size" :height="size" viewBox="0 0 21 21" aria-hidden="true">
    <rect x="1" y="1" width="9" height="9" fill="#F25022" />
    <rect x="11" y="1" width="9" height="9" fill="#7FBA00" />
    <rect x="1" y="11" width="9" height="9" fill="#00A4EF" />
    <rect x="11" y="11" width="9" height="9" fill="#FFB900" />
  </svg>

  <!-- Okta: the circle mark. -->
  <svg v-else-if="preset === 'okta'" :width="size" :height="size" viewBox="0 0 24 24" aria-hidden="true">
    <path
      fill="#007DC1"
      d="M12 0C5.389 0 0 5.35 0 12s5.35 12 12 12 12-5.35 12-12S18.611 0 12 0zm0 18c-3.325 0-6-2.675-6-6s2.675-6 6-6 6 2.675 6 6-2.675 6-6 6z"
    />
  </svg>

  <!-- Auth0: the shield. -->
  <svg v-else-if="preset === 'auth0'" :width="size" :height="size" viewBox="0 0 24 24" aria-hidden="true">
    <path
      fill="#EB5424"
      d="M21.98 7.448 19.62 0H4.347L2.02 7.448c-1.352 4.312.03 9.206 3.815 12.015L12.007 24l6.157-4.552c3.755-2.81 5.182-7.688 3.815-12.015l-6.16 4.58 2.343 7.45-6.157-4.597-6.158 4.58 2.358-7.433-6.188-4.55 7.63-.045L12.008 0l2.356 7.404 7.615.044z"
    />
  </svg>

  <!-- Keycloak: hexagon and double chevron, simplified — see the header. -->
  <svg
    v-else-if="preset === 'keycloak'"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="#00B8E3"
    stroke-width="1.8"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M7 3.2h10l5 8.8-5 8.8H7L2 12z" />
    <path d="M10.6 8 8.2 12l2.4 4M15 8l-2.4 4L15 16" stroke-linecap="square" />
  </svg>

  <!-- Everything else — custom OAuth2, a bare OIDC issuer, a preset this build has no mark for. -->
  <svg
    v-else
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="m21 2-9.6 9.6" />
    <circle cx="7.5" cy="15.5" r="5.5" />
    <path d="m15.5 7.5 3 3L22 7l-3-3" />
  </svg>
</template>
