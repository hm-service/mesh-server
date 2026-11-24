<script setup lang="ts">
import { computed, ref } from "vue";
import type { DeviceSummary } from "../api";
import { statusRank } from "../format";
import DeviceCard from "./DeviceCard.vue";

const props = defineProps<{ devices: DeviceSummary[] }>();
const emit = defineEmits<{ open: [id: string]; create: []; refresh: [] }>();

const sortKey = ref<"hostname" | "seen" | "status">("hostname");
const detailed = ref(true);

const sorted = computed(() => {
  const list = [...props.devices];
  const byName = (a: DeviceSummary, b: DeviceSummary) =>
    (a.hostname || a.name).localeCompare(b.hostname || b.name, "zh-Hans-CN");
  list.sort((a, b) => {
    if (sortKey.value === "seen") return (b.last_seen_at || 0) - (a.last_seen_at || 0);
    if (sortKey.value === "status") {
      const rank = statusRank(a.status) - statusRank(b.status);
      return rank !== 0 ? rank : byName(a, b);
    }
    return byName(a, b);
  });
  return list;
});

const counts = computed(() => {
  const online = props.devices.filter((d) => d.status === "online").length;
  const pending = props.devices.filter((d) => d.status === "pending").length;
  const parts = [props.devices.length + " 台", online + " 在线"];
  if (pending > 0) parts.push(pending + " 待注册");
  return parts.join(" · ");
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <h1>设备列表</h1>
      <span class="count">{{ counts }}</span>
    </div>
    <div class="toolbar">
      <span class="label">排序依据：</span>
      <span class="select">
        <select v-model="sortKey">
          <option value="hostname">主机名</option>
          <option value="seen">最后心跳</option>
          <option value="status">在线状态</option>
        </select>
      </span>
      <button type="button" @click="emit('refresh')">刷新</button>
      <label class="switch">
        <span>显示详情</span>
        <input v-model="detailed" type="checkbox" />
      </label>
    </div>
    <div class="board">
      <div class="cards">
        <DeviceCard
          v-for="device in sorted"
          :key="device.id"
          :device="device"
          :detailed="detailed"
          @open="emit('open', $event)"
        />
        <article class="card new" @click="emit('create')">
          <div class="plus">＋</div>
          <div class="txt">新建设备</div>
        </article>
      </div>
    </div>
  </section>
</template>
