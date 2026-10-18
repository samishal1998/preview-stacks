<script setup lang="ts">
import { computed, ref } from 'vue';
import { HAlertDialog, HButton, HCard, HInput, HList, HListItem, HStack, HText } from '@hearth-ui/vue';
import { controlApi, why, type DomainsStatus } from '../../api-control';
import { domainProblem } from '../../lib/control';
import { toast } from '../../state';

const props = defineProps<{ domains: DomainsStatus; canEdit: boolean }>();
const emit = defineEmits<{ reload: [] }>();

const next = ref('');
const busy = ref(false);
const removing = ref('');
const problem = computed(() => (next.value ? domainProblem(next.value, props.domains.domains, props.domains.primary) : ''));

/** The list replaces what is stored, so add and remove are the same call. */
async function save(list: string[]): Promise<boolean> {
  busy.value = true;
  const r = await controlApi.saveDomains(list);
  busy.value = false;
  if (!r.ok) {
    toast(why(r, 'Save the domains'), 'danger');
    return false;
  }
  toast('Domains saved.', 'success');
  emit('reload');
  return true;
}
async function add(): Promise<void> {
  if (problem.value || !next.value.trim()) return;
  if (await save([...props.domains.domains, next.value.trim().toLowerCase()])) next.value = '';
}
async function remove(): Promise<void> {
  const d = removing.value;
  await save(props.domains.domains.filter((x) => x !== d));
  removing.value = '';
}
</script>

<template>
  <HCard title="Domains">
    <HStack :gap="3" direction="column">
      <HList label="Domains">
        <HListItem :title="domains.primary || 'Not set'" description="Primary" />
        <HListItem v-for="d in domains.domains" :key="d" :title="d" :description="`control.${d} · api.${d}`">
          <template v-if="canEdit" #trailing>
            <HButton variant="ghost" size="compact" :label="`Remove ${d}`" :disabled="busy" @click="removing = d">Remove</HButton>
          </template>
        </HListItem>
      </HList>
      <HStack direction="row" v-if="canEdit" :gap="3" wrap align="end">
        <HInput v-model="next" label="Add a domain" placeholder="preview.example.com" :error="problem" @keyup.enter="add" />
        <HButton variant="primary" :loading="busy" :disabled="!next.trim() || !!problem" @click="add">Add</HButton>
      </HStack>
      <HText v-else tone="muted">Maintainer only.</HText>
    </HStack>
    <HAlertDialog
      :open="!!removing"
      danger
      :title="`Remove ${removing}?`"
      confirm-label="Remove"
      :busy="busy"
      @update:open="(o: boolean) => !o && (removing = '')"
      @confirm="remove"
    />
  </HCard>
</template>
