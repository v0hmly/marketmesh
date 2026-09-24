<script setup lang="ts">
import { computed } from 'vue';
import { passwordChecks } from '../validation';
const props = defineProps<{ password: string; id: string }>();
const rules = computed(() => passwordChecks(props.password));
</script>
<template>
  <ul :id="id" class="password-requirements">
    <li
      v-for="rule in rules"
      :key="rule.id"
      :class="rule.ok ? 'requirement-met' : 'requirement-pending'"
    >
      <span aria-hidden="true">{{ rule.ok ? '✓' : '·' }}</span>
      <span
        >{{ rule.label
        }}<span class="visually-hidden">{{
          rule.ok ? ' — выполнено' : ' — не выполнено'
        }}</span></span
      >
    </li>
  </ul>
</template>
