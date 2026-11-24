<script setup lang="ts">
import { computed } from "vue";
import type { DeviceSummary } from "../api";
import { nowSec } from "../clock";
import {
  appStateClass,
  appStateText,
  configStateClass,
  configStateText,
  relTime,
  shortDeviceID,
  shortHash,
  statusClass,
  statusText,
} from "../format";

const props = defineProps<{ device: DeviceSummary; detailed: boolean }>();
const emit = defineEmits<{ open: [id: string] }>();

const dotClass = computed(() => {
  if (props.device.status === "pending") return "dot warn";
  if (props.device.status !== "online") return "dot gray";
  return "dot";
});
</script>

<template>
  <article class="card" @click="emit('open', device.id)">
    <div class="card-head">
      <span :class="dotClass" />
      <span class="name">{{ device.name }}</span>
      <span :class="statusClass(device.status)">{{ statusText(device.status) }}</span>
    </div>
    <div class="card-body">
      <div class="row">
        <span class="k">设备 ID</span>
        <span class="v">{{ device.device_id ? shortDeviceID(device.device_id) : "未注册" }}</span>
      </div>
      <template v-if="detailed">
        <div class="row">
          <span class="k">主机名</span>
          <span class="v">{{ device.hostname || "—" }}</span>
        </div>
        <div class="row">
          <span class="k">配置</span>
          <span class="v">
            {{ shortHash(device.config_hash) }}
            <span :class="configStateClass(device.config_state)">{{ configStateText(device.config_state) }}</span>
          </span>
        </div>
        <div class="row">
          <span class="k">客户端</span>
          <span class="v">
            {{ device.last_app_hash ? shortHash(device.last_app_hash) : "—" }}
            <span :class="appStateClass(device)">{{ appStateText(device) }}</span>
          </span>
        </div>
      </template>
      <div class="row">
        <span class="k">最后心跳</span>
        <span class="v">{{ device.last_seen_at ? relTime(device.last_seen_at, nowSec) : "从未" }}</span>
      </div>
    </div>
  </article>
</template>
