<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref, watch } from "vue";
import Sortable from "sortablejs";
import { api, ApiError, type DevicePlacement, type DeviceSummary, type GroupView } from "../api";
import DeviceDetail from "./DeviceDetail.vue";
import DeviceRow from "./DeviceRow.vue";

const props = defineProps<{ devices: DeviceSummary[]; groups: GroupView[] }>();
const emit = defineEmits<{
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
const creatingGroup = ref(false);
const newGroupName = ref("");
const editingGroup = ref<number | null>(null);
const editingName = ref("");
const confirmingGroup = ref<number | null>(null);
const busy = ref(false);
const dragging = ref(false);
// 详情在行下方就地展开，同一时刻只展开一台。
const expandedId = ref<string | null>(null);

const sortables = new Map<number, Sortable>();
let dragOrigin: { item: HTMLElement; parent: HTMLElement; next: Node | null } | null = null;
let lastDragEnd = 0;

/** 未分组固定在第一个分区，其余按 position 排列。 */
const sections = computed<Section[]>(() => {
  const groups = [...localGroups.value].sort((a, b) => a.position - b.position || a.id - b.id);
  const buckets = new Map<number, DeviceSummary[]>();
  buckets.set(0, []);
  for (const group of groups) buckets.set(group.id, []);
  for (const device of localDevices.value) {
    const bucket = buckets.get(device.groupId) ?? buckets.get(0);
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
  const total = localDevices.value.length;
  const online = localDevices.value.filter((d) => d.status === "online").length;
  const pending = localDevices.value.filter((d) => d.status === "pending").length;
  const parts = [total + " 台设备", online + " 在线"];
  if (pending > 0) parts.push(pending + " 待注册");
  return parts.join(" · ");
});

watch(
  () => props.devices,
  (next) => {
    if (dragging.value || busy.value) return;
    localDevices.value = next.map((device) => ({ ...device }));
    // 设备从空变成非空时表格才出现，新容器要补挂拖拽。
    void nextTick(() => mountSortables());
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
          // 展开的详情块是拖拽项的兄弟节点，拖起来只会碍事：一开始拖就收起。
          expandedId.value = null;
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
    for (const row of element.querySelectorAll<HTMLElement>("[data-device-id]")) {
      const id = row.dataset.deviceId;
      if (id) layout.push({ id, groupId: section.id, position: position++ });
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
  lastDragEnd = Date.now();
  if (layout) void saveLayout(layout);
}

function applyLayout(layout: DevicePlacement[]) {
  const byId = new Map(localDevices.value.map((device) => [device.id, device]));
  for (const entry of layout) {
    const device = byId.get(entry.id);
    if (!device) continue;
    device.groupId = entry.groupId;
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

function toggleDevice(id: string) {
  // 拖拽松手后浏览器还会补一个 click；刚拖过就不要再展开详情。
  if (Date.now() - lastDragEnd < 250) return;
  expandedId.value = expandedId.value === id ? null : id;
}

function forwardToast(title: string, message: string, kind: "ok" | "err") {
  emit("toast", title, message, kind);
}

function onDetailChanged() {
  emit("refresh");
}

function onEscape(event: KeyboardEvent) {
  if (event.key === "Escape" && expandedId.value) expandedId.value = null;
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
    creatingGroup.value = false;
    emit("refresh");
  } catch (err) {
    reportError(err, "创建分组失败");
  }
}

function startRename(section: Section) {
  editingGroup.value = section.id;
  editingName.value = section.name;
  confirmingGroup.value = null;
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
  try {
    await api.deleteGroup(section.id);
    confirmingGroup.value = null;
    emit("refresh");
  } catch (err) {
    reportError(err, "删除分组失败");
  }
}

onMounted(() => {
  mountSortables();
  window.addEventListener("keydown", onEscape);
});

// 新建分组、设备从空变非空都会让新的 .rows 出现；每次更新补挂一次，幂等。
onUpdated(() => {
  mountSortables();
});

onBeforeUnmount(() => {
  window.removeEventListener("keydown", onEscape);
  for (const instance of sortables.values()) instance.destroy();
  sortables.clear();
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div>
        <h1 class="page-title">设备</h1>
        <p class="page-sub">
          {{ counts }}<template v-if="localDevices.length"> · 拖动行可排序，拖到其它分组即改归属</template>
        </p>
      </div>
      <div class="page-actions">
        <button type="button" @click="emit('refresh')">刷新</button>
        <button type="button" @click="creatingGroup = !creatingGroup">新建分组</button>
        <button class="primary" type="button" @click="emit('create')">新建设备</button>
      </div>
    </div>

    <form v-if="creatingGroup" class="inline-form" @submit.prevent="createGroup">
      <input v-model="newGroupName" type="text" maxlength="64" placeholder="分组名称" autocomplete="off" />
      <button class="primary" type="submit" :disabled="!newGroupName.trim()">创建</button>
      <button type="button" @click="creatingGroup = false; newGroupName = ''">取消</button>
    </form>

    <div v-if="localDevices.length" class="table">
      <div class="thead" aria-hidden="true">
        <span>设备</span>
        <span>mesh 地址</span>
        <span>状态</span>
        <span>配置</span>
        <span>版本</span>
        <span>最后在线</span>
        <span></span>
      </div>
      <section v-for="section in sections" :key="section.id" class="group">
        <div class="group-head">
          <template v-if="editingGroup === section.id">
            <input
              v-model="editingName"
              type="text"
              maxlength="64"
              @keydown.enter.prevent="submitRename(section)"
              @keydown.esc="editingGroup = null"
              @blur="submitRename(section)"
            />
            <button class="ghost" type="button" @click="editingGroup = null">取消</button>
          </template>
          <template v-else>
            <span class="group-name">{{ section.name }}</span>
            <span class="group-count">{{ section.devices.length }} 台</span>
            <div class="group-actions">
              <template v-if="confirmingGroup === section.id">
                <span class="group-count">删除后组内设备回到未分组</span>
                <button class="ghost danger" type="button" @click="removeGroup(section)">确认删除</button>
                <button class="ghost" type="button" @click="confirmingGroup = null">取消</button>
              </template>
              <template v-else-if="section.id !== 0">
                <button class="ghost" type="button" @click="startRename(section)">改名</button>
                <button class="ghost danger" type="button" @click="confirmingGroup = section.id">删除</button>
              </template>
            </div>
          </template>
        </div>
        <div class="rows" :data-sortable-group="section.id">
          <div v-if="!section.devices.length" class="rows-empty">拖设备到这里</div>
          <template v-for="device in section.devices" :key="device.id">
            <DeviceRow :device="device" :expanded="expandedId === device.id" @open="toggleDevice" />
            <DeviceDetail
              v-if="expandedId === device.id"
              :id="device.id"
              :groups="localGroups"
              :devices="localDevices"
              @close="expandedId = null"
              @changed="onDetailChanged"
              @toast="forwardToast"
              @unauthorized="emit('unauthorized')"
            />
          </template>
        </div>
      </section>
    </div>

    <div v-else class="empty-state">
      <h2>还没有设备</h2>
      <p>用默认模板新建设备，把生成的一次性令牌交给客户端完成注册。</p>
      <button class="primary" type="button" @click="emit('create')">新建设备</button>
    </div>
  </section>
</template>
