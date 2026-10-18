<script setup lang="ts">
/**
 * The form slot of HAuthPage: password sign-in plus one button per enabled SSO provider.
 *
 * Providers come from /api/health, which answers before a session exists. A provider failure comes
 * back as ?sso_error= on /login (the callback is a redirect), and renders as text only.
 */
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { HAlert, HButton, HCodeBlock, HInput, HStack, HText } from '@hearth-ui/vue';
import { ssoApi, type Health } from '../api-sso';
import { safeNext } from '../lib/sso';
import SsoMark from './SsoMark.vue';

const props = defineProps<{ loading?: boolean; error?: string }>();
const emit = defineEmits<{ submit: [c: { username: string; password: string }]; token: [t: string] }>();

const route = useRoute();
const username = ref('');
const password = ref('');
const byToken = ref(false);
const token = ref('');
const health = ref<Health>({});

onMounted(async () => {
  const r = await ssoApi.health();
  if (r.ok) health.value = r.body;
});

/** Where to come back to: the bounced path on /login, otherwise where the person already is. */
const next = computed(() => safeNext(route.path === '/login' ? route.query.next : route.fullPath));
const providers = computed(() => health.value.sso?.providers ?? []);
const ssoError = computed(() => (typeof route.query.sso_error === 'string' ? route.query.sso_error : ''));
const ssoErrorTitle = computed(() => {
  const only = providers.value.length === 1 ? providers.value[0] : undefined;
  return only ? `Signing in with ${only.label} failed` : 'Signing in failed';
});
const bootstrap = computed(
  () => `curl -X POST ${location.origin}/api/auth/bootstrap \\\n  -H 'Authorization: Bearer $PSTACK_TOKEN' \\\n  -H 'Content-Type: application/json' \\\n  -d '{"username":"you","password":"..."}'`,
);

const ready = computed(() => !!username.value.trim() && !!password.value);
function submit(): void {
  if (!ready.value) return;
  emit('submit', { username: username.value.trim(), password: password.value });
}
function submitToken(): void {
  if (token.value.trim()) emit('token', token.value.trim());
}
// Cleared when the server refuses it (as the old login did), not before the answer is known.
watch(
  () => [props.loading, props.error] as const,
  ([loading, error]) => {
    if (!loading && error) password.value = token.value = '';
  },
);
function viaProvider(key: string): void {
  window.location.assign(ssoApi.startUrl(key, next.value));
}
</script>

<template>
  <HStack direction="column" :gap="4">
    <template v-if="health.hasUsers === false">
      <HAlert tone="info" title="No accounts yet" description="Create the first one with the host token." />
      <HCodeBlock :code="bootstrap" language="bash" title="Create the first account" />
    </template>

    <form v-if="byToken" @submit.prevent="submitToken">
      <HStack direction="column" :gap="3">
        <HInput v-model="token" label="Access token" type="password" autocomplete="off" required />
        <HAlert v-if="error" tone="danger" :title="error" />
        <HButton type="submit" variant="primary" :loading="loading" :disabled="!token.trim()" label="Use token" />
        <HButton variant="ghost" label="Use a password" @click="byToken = false" />
      </HStack>
    </form>
    <form v-else @submit.prevent="submit">
      <HStack direction="column" :gap="3">
        <HInput v-model="username" label="Username" autocomplete="username" required />
        <HInput v-model="password" label="Password" type="password" autocomplete="current-password" required />
        <HAlert v-if="error" tone="danger" :title="error" />
        <HButton type="submit" variant="primary" :loading="loading" :disabled="!ready" label="Sign in" />
        <HButton variant="ghost" label="Use a token" @click="byToken = true" />
      </HStack>
    </form>

    <template v-if="providers.length">
      <HText tone="muted" size="sm">or</HText>
      <HAlert v-if="ssoError" tone="danger" :title="ssoErrorTitle" :description="ssoError" />
      <HStack direction="column" :gap="2">
        <HButton v-for="p in providers" :key="p.key" variant="secondary" @click="viaProvider(p.key)">
          <template #icon><SsoMark :preset="p.preset" /></template>
          Continue with {{ p.label }}
        </HButton>
      </HStack>
    </template>
    <HAlert v-else-if="ssoError" tone="danger" :title="ssoErrorTitle" :description="ssoError" />
  </HStack>
</template>
