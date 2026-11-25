<script setup lang="ts">
import { onBeforeUnmount, onMounted } from "vue";

const props = defineProps<{
  title: string;
  confirmLabel: string;
  busy?: boolean;
  disabled?: boolean;
  danger?: boolean;
}>();
const emit = defineEmits<{ cancel: []; confirm: [] }>();

/** Esc 只关最上面这层：捕获阶段截住，抽屉的全局 Esc 不会再跟着响应。 */
function onKey(event: KeyboardEvent) {
  if (event.key !== "Escape") return;
  event.stopPropagation();
  if (!props.busy) emit("cancel");
}

onMounted(() => window.addEventListener("keydown", onKey, true));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));
</script>

<template>
  <div class="modal" @click.self="!busy && emit('cancel')">
    <div class="dialog" role="dialog" aria-modal="true">
      <h2 class="dialog-title">{{ title }}</h2>
      <div class="dialog-body">
        <slot />
      </div>
      <div class="dialog-actions">
        <button type="button" :disabled="busy" @click="emit('cancel')">取消</button>
        <button
          :class="danger ? 'danger solid' : 'primary'"
          type="button"
          :disabled="busy || disabled"
          @click="emit('confirm')"
        >
          {{ busy ? "处理中…" : confirmLabel }}
        </button>
      </div>
    </div>
  </div>
</template>
