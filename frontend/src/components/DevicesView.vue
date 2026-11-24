<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import Sortable from "sortablejs";
import { api, ApiError, type DevicePlacement, type DeviceSummary, type GroupView } from "../api";
import DeviceCard from "./DeviceCard.vue";

const props = defineProps<{ devices: DeviceSummary[]; groups: GroupView[] }>();
const emit = defineEmits<{
  open: [id: string];
  create: [];
  refresh: [];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

interface Section {
  id: number;
  name: string;
  devices: DeviceSummary[];
}

const localDevices = ref<DeviceSummary[]>([]);
const localGroups = ref<GroupView[]>([]);
const detailed = ref(true);
const creating = ref(false);
const newGroupName = ref("");
const editingGroup = ref<number | null>(null);
const editingName = ref("");
const busy = ref(false);
const dragging = ref(false);

const sortables = new Map<number, Sortable>();
let dragOrigin: { item: HTMLElement; parent: HTMLElement; next: Node | null } | null = null;

/** 未分组固定在第一个分区，其余按 position 排列。 */
const sections = computed<Section[]>(() => {
  const groups = [...localGroups.value].sort((a, b) => a.position - b.position || a.id - b.id);
  const buckets = new Map<number, DeviceSummary[]>();
  buckets.set(0, []);
  for (const group of groups) buckets.set(group.id, []);
  for (const device of localDevices.value) {
    const bucket = buckets.get(device.group_id) ?? buckets.get(0);
    bucket?.push(device);
  }
  const build = (id: number, name: string): Section => ({
    id,
    name,
    // position 相同的设备保持服务端返回的创建顺序。
    devices: [...(buckets.get(id) ?? [])].sort((a, b) => a.position - b.position),
  });
  return [build(0, "未分组"), ...groups.map((group) => build(group.id, group.name))];
});

const counts = computed(() => {
  const online = localDevices.value.filter((d) => d.status === "online").length;
  const pending = localDevices.value.filter((d) => d.status === "pending").length;
  const parts = [localDevices.value.length + " 台", online + " 在线"];
  if (pending > 0) parts.push(pending + " 待注册");
  return parts.join(" · ");
});

watch(
  () => props.devices,
  (next) => {
    if (dragging.value || busy.value) return;
    localDevices.value = next.map((device) => ({ ...device }));
  },
  { immediate: true },
);

watch(
  () => props.groups,
  (next) => {
    if (dragging.value || busy.value) return;
    localGroups.value = next.map((group) => ({ ...group }));
    void nextTick(() => mountSortables());
  },
  { immediate: true },
);

watch(busy, (value) => {
  for (const instance of sortables.values()) instance.option("disabled", value);
});

function mountSortables() {
  if (dragging.value) return;
  const seen = new Set<number>();
  for (const element of document.querySelectorAll<HTMLElement>("[data-sortable-group]")) {
    const groupId = Number(element.dataset.sortableGroup);
    seen.add(groupId);
    if (sortables.has(groupId)) continue;
    sortables.set(
      groupId,
      Sortable.create(element, {
        group: "devices",
        draggable: "[data-device-id]",
        ghostClass: "drag-ghost",
        animation: 0,
        onStart: (event) => {
          dragging.value = true;
          dragOrigin = { item: event.item, parent: event.from, next: event.item.nextSibling };
        },
        onEnd: () => finishDrag(),
      }),
    );
  }
  for (const [groupId, instance] of sortables) {
    if (seen.has(groupId)) continue;
    instance.destroy();
    sortables.delete(groupId);
  }
}

/** 读取拖动后的 DOM 顺序；分组顺序来自当前 sections。 */
function readLayout(): DevicePlacement[] | null {
  const layout: DevicePlacement[] = [];
  for (const section of sections.value) {
    const element = document.querySelector<HTMLElement>(`[data-sortable-group="${section.id}"]`);
    if (!element) return null;
    let position = 0;
    for (const card of element.querySelectorAll<HTMLElement>("[data-device-id]")) {
      const id = card.dataset.deviceId;
      if (id) layout.push({ id, group_id: section.id, position: position++ });
    }
  }
  return layout;
}

function finishDrag() {
  const origin = dragOrigin;
  dragOrigin = null;
  const layout = readLayout();
  // Sortable 已经移动了真实 DOM，而 Vue 的 vnode 仍记得旧位置。先把节点放回
  // 原处，再让响应式数据驱动重排；提交失败时数据回滚即可恢复原样。
  if (origin) origin.parent.insertBefore(origin.item, origin.next);
  dragging.value = false;
  if (layout) void saveLayout(layout);
}

function applyLayout(layout: DevicePlacement[]) {
  const byId = new Map(localDevices.value.map((device) => [device.id, device]));
  for (const entry of layout) {
    const device = byId.get(entry.id);
    if (!device) continue;
    device.group_id = entry.group_id;
    device.position = entry.position;
  }
  localDevices.value = [...localDevices.value];
}

async function saveLayout(layout: DevicePlacement[]) {
  const snapshot = localDevices.value.map((device) => ({ ...device }));
  applyLayout(layout);
  busy.value = true;
  try {
    await api.saveLayout(layout);
  } catch (err) {
    localDevices.value = snapshot;
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    emit("toast", "排序未保存", err instanceof Error ? err.message : "请重试", "err");
  } finally {
    busy.value = false;
  }
}

function reportError(err: unknown, title: string) {
  if (err instanceof ApiError && err.status === 401) {
    emit("unauthorized");
    return;
  }
  emit("toast", title, err instanceof Error ? err.message : "", "err");
}

async function createGroup() {
  const name = newGroupName.value.trim();
  if (!name) return;
  try {
    await api.createGroup(name);
    newGroupName.value = "";
    creating.value = false;
    emit("refresh");
  } catch (err) {
    reportError(err, "创建分组失败");
  }
}

function startRename(section: Section) {
  editingGroup.value = section.id;
  editingName.value = section.name;
}

async function submitRename(section: Section) {
  if (editingGroup.value !== section.id) return;
  const name = editingName.value.trim();
  editingGroup.value = null;
  if (!name || name === section.name) return;
  try {
    await api.updateGroup(section.id, { name });
    emit("refresh");
  } catch (err) {
    reportError(err, "分组改名失败");
  }
}

async function removeGroup(section: Section) {
  if (!window.confirm("删除分组「" + section.name + "」？组内设备会回到未分组。")) return;
  try {
    await api.deleteGroup(section.id);
    emit("refresh");
  } catch (err) {
    reportError(err, "删除分组失败");
  }
}

onMounted(() => {
  mountSortables();
});

onBeforeUnmount(() => {
  for (const instance of sortables.values()) instance.destroy();
  sortables.clear();
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <h1>设备列表</h1>
      <span class="count">{{ counts }}</span>
    </div>
    <div class="toolbar">
      <span class="label">拖动卡片调整顺序，拖到其它分区即改变分组</span>
      <button type="button" @click="emit('refresh')">刷新</button>
      <button type="button" @click="creating = !creating">新建分组</button>
      <label class="switch">
        <span>显示详情</span>
        <input v-model="detailed" type="checkbox" />
      </label>
    </div>
    <div v-if="creating" class="toolbar create-group">
      <input
        v-model="newGroupName"
        type="text"
        maxlength="64"
        placeholder="分组名称"
        @keyup.enter="createGroup"
      />
      <button class="primary" type="button" @click="createGroup">创建</button>
      <button type="button" @click="creating = false; newGroupName = ''">取消</button>
    </div>

    <section v-for="section in sections" :key="section.id" class="group">
      <div class="toolbar group-head">
        <template v-if="editingGroup === section.id">
          <input
            v-model="editingName"
            type="text"
            maxlength="64"
            @keyup.enter="submitRename(section)"
            @keyup.escape="editingGroup = null"
            @blur="submitRename(section)"
          />
        </template>
        <template v-else>
          <span class="gname">{{ section.name }}</span>
          <span class="label">{{ section.devices.length }} 台</span>
        </template>
        <template v-if="section.id !== 0">
          <span class="spacer" />
          <button v-if="editingGroup !== section.id" type="button" @click="startRename(section)">改名</button>
          <button v-if="editingGroup !== section.id" class="danger" type="button" @click="removeGroup(section)">
            删除
          </button>
        </template>
      </div>
      <div class="board">
        <div class="cards" :data-sortable-group="section.id">
          <DeviceCard
            v-for="device in section.devices"
            :key="device.id"
            :data-device-id="device.id"
            :device="device"
            :detailed="detailed"
            @open="emit('open', $event)"
          />
        </div>
        <div v-if="section.id === 0" class="new-row">
          <article class="card new" @click="emit('create')">
            <div class="plus">＋</div>
            <div class="txt">新建设备</div>
          </article>
        </div>
      </div>
    </section>
  </section>
</template>
