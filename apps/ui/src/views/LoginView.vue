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
import { computed, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { authState, checkAuth, login } from '../composables/useAuth';
import { api } from '../api/client';
import ActionButton from '../components/ActionButton.vue';
import InfoHint from '../components/InfoHint.vue';
import EquivalentCommand from '../components/EquivalentCommand.vue';

const router = useRouter();
const route = useRoute();

const username = ref('');
const password = ref('');
const pending = ref(false);
const error = ref('');

/** Where the guard wanted to go. Also what the SSO round trip carries and comes back to. */
const next = computed(() =>
  typeof route.query.next === 'string' && route.query.next.startsWith('/') ? route.query.next : '/',
);

/**
 * The provider failed or refused. It arrives in the query string because the callback is a REDIRECT
 * — there is no fetch to read a body from — and it is the provider's own words, so it renders as
 * text and never as markup.
 */
const ssoError = computed(() => (typeof route.query.sso_error === 'string' ? route.query.sso_error : ''));

/**
 * A full navigation, not a fetch: the whole point is to leave this origin for the provider's
 * consent screen. `api.url` so a dev session pointed at a tunnelled host still reaches its API.
 */
function signInWithProvider(): void {
  window.location.assign(api.url(`/api/auth/sso/start?next=${encodeURIComponent(next.value)}`));
}

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
  void router.replace(next.value);
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
        <!--
          A curl block used to sit here permanently, on the FIRST screen anyone sees. It is needed
          exactly once per host and by one person; behind the button it costs a click then and
          nothing on every later visit — and it is generated, so it cannot drift from the route.
        -->
        <div class="banner plain" style="margin-top: var(--s3)">
          <b>Create the first account</b>
          <p>
            This has to be done with the host token, from a terminal — a browser has no way to prove
            it is allowed to claim a fresh server.
          </p>
          <EquivalentCommand
            what="creating the first account"
            method="POST"
            path="/api/auth/bootstrap"
            :body="{ username: 'you', password: '…' }"
          />
          <p class="mute" style="margin-top: var(--s3)">
            It can also be set up ahead of time when the host is first provisioned.
            <InfoHint label="how">
              Set the admin username and password in the environment before running the setup
              command on the host. They are honoured only while no account exists, so they cannot
              overwrite one later.
            </InfoHint>
          </p>
        </div>
      </template>

      <!-- ── the provider, above the form: for most people on a host with SSO it is the only
           control on this page they should touch. -->
      <template v-if="authState.sso?.enabled">
        <div v-if="ssoError" class="banner failed" style="margin-top: var(--s3)">
          <b>Signing in with {{ authState.sso.label }} did not work.</b>
          <p>{{ ssoError }}</p>
        </div>
        <ActionButton variant="primary" style="width: 100%; margin-top: var(--s4)" @click="signInWithProvider">
          Sign in with {{ authState.sso.label }}
        </ActionButton>
        <div class="row" style="margin: var(--s4) 0; align-items: center; gap: var(--s3)">
          <hr style="flex: 1; border: 0; border-top: 1px solid var(--line)" />
          <span class="mute" style="font-size: var(--t-sm)">or with a local account</span>
          <hr style="flex: 1; border: 0; border-top: 1px solid var(--line)" />
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
/* The shell centres this now — see `.shell.no-rail .view`. A second centring context here would
 * fight it, and `min-height: 70vh` was what pinned the card toward the top of the window. */
/* `100%`, not `92vw`: the shell already insets this by its own padding, so a viewport-relative
 * width added on top overflowed the right edge on a phone (measured 32px/0px at 390). */
.login-wrap {
  width: min(420px, 100%);
}
.login-card {
  width: 100%;
}
</style>
