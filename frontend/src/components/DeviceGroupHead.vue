<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";

const props = defineProps<{
  name: string;
  count: number;
  online: number;
  offline: number;
  pending: number;
  collapsed: boolean;
  /** pinned 是「未分组」：不可拖动、不可改名删除，只保留折叠与统计。 */
  pinned?: boolean;
  editing?: boolean;
  confirming?: boolean;
}>();

const editingName = defineModel<string>("editingName", { default: "" });

const emit = defineEmits<{
  toggle: [];
  rename: [];
  submit: [];
  cancel: [];
  remove: [];
  confirmRemove: [];
  cancelRemove: [];
}>();

const initial = computed(() => (props.pinned ? "未" : props.name.trim().slice(0, 1).toUpperCase() || "?"));
const input = ref<HTMLInputElement | null>(null);

watch(
  () => props.editing,
  (editing) => {
    if (!editing) return;
    void nextTick(() => {
      input.value?.focus();
      input.value?.select();
    });
  },
  { immediate: true },
);
</script>

<template>
  <header class="group-head">
    <span v-if="pinned" class="group-handle-spacer" aria-hidden="true" />
    <span v-else class="group-handle" data-group-handle title="拖动调整分组顺序" aria-hidden="true">
      <svg viewBox="0 0 8 14" fill="currentColor" aria-hidden="true">
        <circle cx="2" cy="2" r="1.3" />
        <circle cx="6" cy="2" r="1.3" />
        <circle cx="2" cy="7" r="1.3" />
        <circle cx="6" cy="7" r="1.3" />
        <circle cx="2" cy="12" r="1.3" />
        <circle cx="6" cy="12" r="1.3" />
      </svg>
    </span>

    <button
      class="group-toggle"
      type="button"
      :aria-expanded="collapsed ? 'false' : 'true'"
      :aria-label="(collapsed ? '展开' : '收起') + name"
      :title="collapsed ? '展开分组' : '收起分组'"
      @click="emit('toggle')"
    >
      <svg class="chev" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M6 3.5 10.5 8 6 12.5" />
      </svg>
    </button>

    <span class="group-badge" :class="{ 'is-neutral': pinned }" aria-hidden="true">{{ initial }}</span>

    <template v-if="editing">
      <input
        ref="input"
        v-model="editingName"
        class="group-name-input"
        type="text"
        maxlength="64"
        autocomplete="off"
        aria-label="分组名称"
        @keydown.enter.prevent="emit('submit')"
        @keydown.esc.stop="emit('cancel')"
        @blur="emit('submit')"
      />
    </template>
    <button v-else class="group-name" type="button" :title="collapsed ? '展开分组' : '收起分组'" @click="emit('toggle')">
      {{ name }}
    </button>

    <span class="group-count">{{ count }} 台</span>
    <span v-if="online" class="group-stat"><i class="dot" />{{ online }} 在线</span>
    <span v-if="offline" class="group-stat is-off"><i class="dot off" />{{ offline }} 离线</span>
    <span v-if="pending" class="group-stat"><i class="dot pending" />{{ pending }} 待注册</span>
    <span v-if="!count" class="group-empty-tag">空</span>

    <div class="group-actions" @click.stop>
      <template v-if="confirming">
        <span class="group-confirm">删除后组内设备回到未分组</span>
        <button class="ghost danger" type="button" @click="emit('confirmRemove')">确认删除</button>
        <button class="ghost" type="button" @click="emit('cancelRemove')">取消</button>
      </template>
      <template v-else-if="!pinned">
        <button class="ghost" type="button" @click="emit('rename')">改名</button>
        <button class="ghost danger" type="button" @click="emit('remove')">删除</button>
      </template>
    </div>
  </header>
</template>