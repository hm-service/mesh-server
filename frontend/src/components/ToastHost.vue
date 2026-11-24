<script setup lang="ts">
export interface Toast {
  title: string;
  message: string;
  kind: "ok" | "err";
}

defineProps<{ toast: Toast | null }>();
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <div v-if="toast" class="toast" :class="{ err: toast.kind === 'err' }">
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round">
      <circle cx="8" cy="8" r="6.3" />
      <path d="M8 4.9v3.6" />
      <circle cx="8" cy="11.1" r=".85" fill="currentColor" stroke="none" />
    </svg>
    <div class="body">
      <div class="title">{{ toast.title }}</div>
      <div v-if="toast.message" class="msg">{{ toast.message }}</div>
    </div>
    <button class="close" type="button" @click="emit('close')">×</button>
  </div>
</template>
