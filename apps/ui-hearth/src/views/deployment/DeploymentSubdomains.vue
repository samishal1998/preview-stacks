<script setup lang="ts">
/** Each service with a generated host and its wildcard. A pick stages behind Save: arrowing a closed select changes it per key. */
import { computed, reactive, ref, watch } from 'vue';
import { HButton, HCard, HDataTable, HSelect, HStack, HText, type TableColumn, type TableRow } from '@hearth-ui/vue';
import { deploymentApi, type RoutedService } from '../../api-deployment';
import { DEPTHS, depthEditable, depthLabel, toDepth } from '../../lib/customRoutes';
import { problemText } from '../../lib/deployment';
import { can } from '../../lib/users';
import { auth, toast } from '../../state';

const props = defineProps<{ id: string }>();
const list = ref<RoutedService[]>([]);
/** A staged pick, by service; Save writes it. A refused save keeps it staged. */
const pick = reactive<Record<string, string>>({});
const busy = ref('');

// A failed read hides the card: a spec that needs variables cannot answer, and Overview already says why.
async function load(): Promise<void> {
  const mine = props.id;
  const r = await deploymentApi.subdomains(mine);
  if (mine === props.id) list.value = r.ok && Array.isArray(r.body) ? r.body : [];
}
watch(
  () => props.id,
  () => {
    list.value = [];
    void load();
  },
  { immediate: true },
);

async function save(service: string): Promise<void> {
  if (busy.value) return;
  busy.value = service;
  const r = await deploymentApi.setSubdomains(props.id, service, toDepth(pick[service] ?? ""));
  busy.value = '';
  if (!r.ok) return void toast(problemText(r.status, r.body.error, 'Not saved'), 'danger');
  toast(r.body.note, 'success');
  delete pick[service];
  await load();
}

const mayWrite = computed(() => can('developer', auth));
const columns: TableColumn[] = [
  { key: 'service', label: 'Service' },
  { key: 'host', label: 'Host' },
  { key: 'subdomains', label: 'Subdomains', minWidth: '10rem' },
];
const rows = computed<TableRow[]>(() => list.value.map((s) => ({ id: s.service, service: s.service, host: s.host, subdomains: s.subdomains ?? '' })));
const of = (row: TableRow) => list.value.find((s) => s.service === row.id);
</script>

<template>
  <HCard v-if="list.length" title="Subdomains">
    <HDataTable label="Subdomains" :rows="rows" :columns="columns">
      <template #cell="{ row, column, value }">
        <template v-if="column.key === 'subdomains' && of(row)">
          <HStack v-if="depthEditable(of(row)!.from, mayWrite)" direction="row" :gap="2" wrap align="center">
            <HSelect
              :label="`Subdomains for ${row.service}`"
              hide-label
              size="compact"
              :model-value="pick[String(row.id)] ?? String(value)"
              :options="DEPTHS"
              @update:model-value="(v: string) => (pick[String(row.id)] = v)"
            />
            <HButton
              v-if="pick[String(row.id)] !== undefined && pick[String(row.id)] !== String(value)"
              variant="secondary"
              size="compact"
              label="Save"
              :aria-label="`Save subdomains for ${row.service}`"
              :loading="busy === String(row.id)"
              @click="save(String(row.id))"
            />
          </HStack>
          <template v-else>
            {{ depthLabel(of(row)!.subdomains) }}
            <HText v-if="of(row)!.from === 'spec'" tone="muted" size="sm">Set in spec.</HText>
          </template>
        </template>
        <template v-else>{{ value }}</template>
      </template>
    </HDataTable>
  </HCard>
</template>
