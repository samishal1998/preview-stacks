<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router';
import { HButton, HEmptyState } from '@hearth-ui/vue';

const route = useRoute();
const router = useRouter();
</script>

<template>
  <HEmptyState title="404" icon="search" :description="`Nothing at ${route.fullPath}.`">
    <HButton label="Overview" @click="router.push('/')" />
    <HButton variant="secondary" label="Deployments" @click="router.push('/deployments')" />
    <HButton variant="secondary" label="Jobs" @click="router.push('/jobs')" />
  </HEmptyState>
</template>
