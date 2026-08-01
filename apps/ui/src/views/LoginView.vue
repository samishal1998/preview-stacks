<script setup lang="ts">
/**
 * Sign in — the one page reachable without a session.
 *
 * Two states it must distinguish, because they need opposite instructions:
 *   - accounts exist         → the form
 *   - no accounts yet        → bootstrap guidance. A dead login form with no way to create the
 *                              account it asks for is the worst version of this page.
 *
 * A bearer token in Settings also authenticates (CI-style access); the link stays visible for
 * operators who have the machine token and no account yet.
 */
import { ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { authState, checkAuth, login } from '../composables/useAuth';
import ActionButton from '../components/ActionButton.vue';
import InfoHint from '../components/InfoHint.vue';

const router = useRouter();
const route = useRoute();

const username = ref('');
const password = ref('');
const pending = ref(false);
const error = ref('');

async function submit(): Promise<void> {
  pending.value = true;
  error.value = '';
  const failure = await login(username.value.trim(), password.value);
  pending.value = false;
  if (failure) {
    error.value = failure;
    password.value = '';
    return;
  }
  await checkAuth();
  // Back to wherever the guard bounced them from, or home.
  const dest = typeof route.query.next === 'string' && route.query.next.startsWith('/')
    ? route.query.next
    : '/';
  void router.replace(dest);
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-card panel">
      <h1 style="font-size: var(--t-xl); margin-bottom: var(--s2)">Sign in</h1>

      <template v-if="authState.hasUsers === false">
        <p class="dim">
          No accounts exist on this server yet — create the first one, then sign in.
        </p>
        <div class="banner plain" style="margin-top: var(--s3)">
          <b>Create the first account</b>
          <p>
            With the machine token from <code>pstack init</code>:
          </p>
          <pre class="code" style="max-height: none">curl -X POST https://api.&lt;domain&gt;/api/auth/bootstrap \
  -H "Authorization: Bearer $PSTACK_TOKEN" \
  -H 'content-type: application/json' \
  -d '{"username":"you","password":"…"}'</pre>
          <p class="mute">
            Or set <code>PSTACK_ADMIN_USER</code> / <code>PSTACK_ADMIN_PASSWORD</code> before
            <code>pstack init</code> — honoured only while no account exists.
          </p>
        </div>
      </template>

      <form @submit.prevent="submit">
        <div class="field" style="margin-top: var(--s4)">
          <label for="u">Username</label>
          <input id="u" v-model="username" type="text" autocomplete="username" spellcheck="false" />
        </div>
        <div class="field">
          <label for="p">Password</label>
          <input id="p" v-model="password" type="password" autocomplete="current-password" />
        </div>

        <div v-if="error" class="banner failed" style="margin-top: var(--s3)">
          <p>{{ error }}</p>
        </div>

        <div class="row" style="margin-top: var(--s4)">
          <ActionButton
            variant="primary"
            :pending="pending"
            :disabled="pending || !username.trim() || !password"
            @click="submit"
          >
            {{ pending ? 'Signing in…' : 'Sign in' }}
          </ActionButton>
          <span class="grow" />
          <RouterLink to="/settings" class="mute">
            Use a token instead
            <InfoHint label="token access" align="end">
              A bearer token in Settings — the machine token from <code>pstack init</code>, or a
              personal token from your account — authenticates every request without a session.
            </InfoHint>
          </RouterLink>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login-wrap {
  min-height: 70vh;
  display: grid;
  place-items: center;
}
.login-card {
  width: min(420px, 92vw);
}
</style>
