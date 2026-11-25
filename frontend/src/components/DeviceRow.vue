<script setup lang="ts">
import { computed } from "vue";
import type { DeviceSummary } from "../api";
import { nowSec } from "../clock";
import {
  appStateClass,
  appStateLabel,
  configStateClass,
  configStateText,
  meshAddress,
  relTime,
  statusDotClass,
  statusText,
} from "../format";

const props = defineProps<{ device: DeviceSummary }>();
const emit = defineEmits<{ open: [id: string] }>();

const seen = computed(() =>
  props.device.last_seen_at ? relTime(props.device.last_seen_at, nowSec.value) : "从未",
);
</script>

<template>
  <div
    class="dev-row"
    role="button"
    tabindex="0"
    :data-device-id="device.id"
    @click="emit('open', device.id)"
    @keydown.enter.prevent="emit('open', device.id)"
    @keydown.space.prevent="emit('open', device.id)"
  >
    <div class="cell name">
      <div class="name-line">
        <span class="nm">{{ device.name }}</span>
        <span v-if="device.identity === 'formal'" class="tag">正式</span>
      </div>
      <span class="sub">{{ device.hostname || device.device_id || "未注册" }}</span>
    </div>
    <div class="cell addr" data-label="地址">{{ meshAddress(device) }}</div>
    <div class="cell live" data-label="状态">
      <span :class="statusDotClass(device.status)" />
      {{ statusText(device.status) }}
    </div>
    <div class="cell cfg" data-label="配置">
      <span :class="configStateClass(device.config_state)">{{ configStateText(device.config_state) }}</span>
    </div>
    <div class="cell app" data-label="版本">
      <template v-if="device.app_version">
        <span class="version">{{ device.app_version }}</span>
        <span v-if="appStateLabel(device)" :class="appStateClass(device)">{{ appStateLabel(device) }}</span>
      </template>
      <span v-else-if="device.last_platform" class="muted">未发布</span>
      <span v-else class="muted">—</span>
    </div>
    <div class="cell seen" data-label="最后在线">{{ seen }}</div>
    <div class="cell go" aria-hidden="true">›</div>
  </div>
</template>
