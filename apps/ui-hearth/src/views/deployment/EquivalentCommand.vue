<script setup lang="ts">
/** "Show equivalent command": the curl for the request the button beside it sends. The token stays a variable. */
import { computed, ref } from 'vue';
import { HButton, HCodeBlock, HDialog } from '@hearth-ui/vue';
import { curlFor } from '../../lib/deployment';

const props = defineProps<{ method: string; path: string; body?: unknown }>();
const open = ref(false);
const text = computed(() => curlFor(`${location.protocol}//${location.host}`, props.method, props.path, props.body));
</script>

<template>
  <HButton variant="ghost" icon="code" label="Equivalent command" @click="open = true" />
  <HDialog :open="open" title="Equivalent command" description="$PSTACK_TOKEN stays a variable." @close="open = false">
    <HCodeBlock :code="text" language="bash" title="curl" wrap />
  </HDialog>
</template>
