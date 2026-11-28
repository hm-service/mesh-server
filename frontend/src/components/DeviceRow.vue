<script setup lang="ts">
import { computed } from "vue";
import type { DeviceSummary } from "../api";
import { nowSec } from "../clock";
import {
  configStateClass,
  configStateText,
  meshAddress,
  relTime,
  statusDotClass,
  statusText,
} from "../format";

const props = defineProps<{ device: DeviceSummary; expanded?: boolean }>();
const emit = defineEmits<{ open: [id: string] }>();

const seen = computed(() =>
  props.device.lastSeenAt ? relTime(props.device.lastSeenAt, nowSec.value) : "从未",
);
const machine = computed(() => props.device.hostname || props.device.deviceId || "未注册");
// 上行是设备当前跑的版本，下行点明能升到哪一版；没有可更新版本时整块只剩一行。
const currentVersion = computed(() => {
  if (props.device.lastAppVersion) return props.device.lastAppVersion;
  if (props.device.appVersion) return "未上报";
  if (props.device.lastPlatform) return "未发布";
  return "—";
});
const updateTo = computed(() => {
  if (!props.device.appVersion || props.device.appState === "current") return "";
  return props.device.appVersion;
});
</script>

<template>
  <div
    class="dev-row"
    :class="{
      open: expanded,
      'is-offline': device.status === 'offline',
      'is-pending': device.status === 'pending',
    }"
    role="button"
    tabindex="0"
    :aria-expanded="expanded ? 'true' : 'false'"
    :aria-label="'查看 ' + device.name + ' 的详情'"
    :data-device-id="device.id"
    @click="emit('open', device.id)"
    @keydown.enter.prevent="emit('open', device.id)"
    @keydown.space.prevent="emit('open', device.id)"
  >
    <span
      class="drag-handle"
      data-device-handle
      title="拖动到其它分组"
      aria-hidden="true"
      @click.stop
    >
      <svg viewBox="0 0 8 14" fill="currentColor" aria-hidden="true">
        <circle cx="2" cy="2" r="1.3" />
        <circle cx="6" cy="2" r="1.3" />
        <circle cx="2" cy="7" r="1.3" />
        <circle cx="6" cy="7" r="1.3" />
        <circle cx="2" cy="12" r="1.3" />
        <circle cx="6" cy="12" r="1.3" />
      </svg>
    </span>

    <div class="cell name">
      <div class="name-line">
        <span class="nm" :title="device.name">{{ device.name }}</span>
        <span v-if="device.identity === 'formal'" class="tag tag-formal">正式</span>
        <span v-else-if="device.identity === ''" class="tag tag-bad">配置错误</span>
      </div>
      <span class="sub" :title="machine">{{ machine }}</span>
    </div>

    <div class="cell addr" data-label="地址">
      <span class="mono">{{ meshAddress(device) }}</span>
    </div>

    <div class="cell live" data-label="状态">
      <span :class="statusDotClass(device.status)" />
      <span class="live-text">{{ statusText(device.status) }}</span>
    </div>

    <div class="cell cfg" data-label="配置">
      <span :class="configStateClass(device.configState)">{{ configStateText(device.configState) }}</span>
    </div>

    <div class="cell app" data-label="版本">
      <div class="version-line">
        <span class="version" :class="{ 'is-empty': !device.lastAppVersion }">{{ currentVersion }}</span>
        <span v-if="updateTo" class="update-pill">待更新</span>
      </div>
      <span v-if="updateTo" class="update-to">可更新到 {{ updateTo }}</span>
    </div>

    <div class="cell seen" data-label="最后在线">{{ seen }}</div>

    <div class="cell go" :class="{ open: expanded }" aria-hidden="true">
      <span class="go-btn">
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
          <path d="M6 3.5 10.5 8 6 12.5" />
        </svg>
      </span>
    </div>
  </div>
</template>