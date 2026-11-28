<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref, watch } from "vue";
import Sortable from "sortablejs";
import { api, ApiError, type DevicePlacement, type DeviceSummary, type GroupView } from "../api";
import DeviceDetail from "./DeviceDetail.vue";
import DeviceGroupHead from "./DeviceGroupHead.vue";
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
  online: number;
  offline: number;
  pending: number;
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
const draggingGroups = ref(false);
// 折叠只在当前视图内有效：服务端没有这个字段，刷新后恢复全部展开。
const collapsed = ref<number[]>([]);
// 详情在行下方就地展开，同一时刻只展开一台。
const expandedId = ref<string | null>(null);
const groupList = ref<HTMLElement | null>(null);

const sortables = new Map<number, Sortable>();
let groupSortable: Sortable | null = null;
interface DragOrigin {
  item: HTMLElement;
  parent: HTMLElement;
  /** 起点下一位；收起详情时它可能被 Vue 摘掉，因此另记一份下标兜底。 */
  next: Node | null;
  index: number;
}
let dragOrigin: DragOrigin | null = null;
let groupDragOrigin: DragOrigin | null = null;
let highlighted: Element | null = null;
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
  const build = (id: number, name: string): Section => {
    // position 相同的设备保持服务端返回的创建顺序。
    const devices = [...(buckets.get(id) ?? [])].sort((a, b) => a.position - b.position);
    return {
      id,
      name,
      devices,
      online: devices.filter((device) => device.status === "online").length,
      offline: devices.filter((device) => device.status === "offline").length,
      pending: devices.filter((device) => device.status === "pending").length,
    };
  };
  return [build(0, "未分组"), ...groups.map((group) => build(group.id, group.name))];
});

const pinned = computed<Section>(() => sections.value[0]!);
const groupSections = computed(() => sections.value.slice(1));

const stats = computed(() => {
  const total = localDevices.value.length;
  const online = localDevices.value.filter((device) => device.status === "online").length;
  return {
    total,
    online,
    offline: localDevices.value.filter((device) => device.status === "offline").length,
    pending: localDevices.value.filter((device) => device.status === "pending").length,
    outdated: localDevices.value.filter((device) => !!device.appVersion && device.appState !== "current").length,
  };
});

const allOffline = computed(() => stats.value.total > 0 && stats.value.online === 0);

watch(
  () => props.devices,
  (next) => {
    if (dragging.value || draggingGroups.value || busy.value) return;
    localDevices.value = next.map((device) => ({ ...device }));
    // 设备从空变成非空时列表才出现，新容器要补挂拖拽。
    void nextTick(() => mountSortables());
  },
  { immediate: true },
);

watch(
  () => props.groups,
  (next) => {
    if (dragging.value || draggingGroups.value || busy.value) return;
    localGroups.value = next.map((group) => ({ ...group }));
    const ids = new Set(next.map((group) => group.id));
    collapsed.value = collapsed.value.filter((id) => id === 0 || ids.has(id));
    void nextTick(() => {
      mountGroupSortable();
      mountSortables();
    });
  },
  { immediate: true },
);

watch(busy, (value) => {
  for (const instance of sortables.values()) instance.option("disabled", value);
  groupSortable?.option("disabled", value);
});

function mountSortables() {
  if (dragging.value || draggingGroups.value) return;
  const seen = new Set<number>();
  for (const element of document.querySelectorAll<HTMLElement>("[data-sortable-group]")) {
    const groupId = Number(element.dataset.sortableGroup);
    seen.add(groupId);
    if (sortables.has(groupId)) continue;
    sortables.set(
      groupId,
      Sortable.create(element, {
        // 只有设备行能在分组之间搬，整块分组不会被塞进某个分组里。
        group: {
          name: "devices",
          pull: true,
          put: (_to: Sortable, _from: Sortable, element: HTMLElement) => element.matches("[data-device-id]"),
        },
        draggable: "[data-device-id]",
        handle: "[data-device-handle]",
        ghostClass: "drag-ghost",
        chosenClass: "drag-chosen",
        animation: 120,
        onStart: (event) => {
          dragging.value = true;
          // 展开的详情块是拖拽项的兄弟节点，拖起来只会碍事：一开始拖就收起。
          expandedId.value = null;
          dragOrigin = {
            item: event.item,
            parent: event.from,
            next: event.item.nextSibling,
            index: [...event.from.children].indexOf(event.item),
          };
        },
        onMove: (event) => {
          highlightGroup(event.to);
          return true;
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

function mountGroupSortable() {
  const element = groupList.value;
  if (!element) {
    groupSortable?.destroy();
    groupSortable = null;
    return;
  }
  if (groupSortable?.el === element) return;
  groupSortable?.destroy();
  groupSortable = Sortable.create(element, {
    group: {
      name: "sections",
      pull: true,
      put: (_to: Sortable, _from: Sortable, node: HTMLElement) => node.classList.contains("group"),
    },
    draggable: ".group",
    handle: "[data-group-handle]",
    ghostClass: "group-ghost",
    chosenClass: "group-chosen",
    animation: 150,
    onStart: (event) => {
      draggingGroups.value = true;
      editingGroup.value = null;
      confirmingGroup.value = null;
      clearHighlight();
      expandedId.value = null;
      groupDragOrigin = {
        item: event.item,
        parent: event.from,
        next: event.item.nextSibling,
        index: [...event.from.children].indexOf(event.item),
      };
    },
    onEnd: () => finishGroupDrag(),
  });
}

/** 拖动时高亮落点所在的分组卡片。 */
function highlightGroup(target: HTMLElement | null) {
  const group = target?.closest(".group") ?? null;
  if (group === highlighted) return;
  highlighted?.classList.remove("drop-target");
  highlighted = group;
  highlighted?.classList.add("drop-target");
}

function clearHighlight() {
  highlighted?.classList.remove("drop-target");
  highlighted = null;
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

/** 把 Sortable 挪过的节点放回原处，让 Vue 从一致的 DOM 出发重排。 */
function restoreItem(origin: DragOrigin | null) {
  if (!origin) return;
  const anchor =
    origin.next && origin.next.parentNode === origin.parent
      ? origin.next
      : origin.parent.children[origin.index] ?? null;
  origin.parent.insertBefore(origin.item, anchor);
}

function finishDrag() {
  const origin = dragOrigin;
  dragOrigin = null;
  const draggedId = origin?.item.dataset.deviceId ?? null;
  const layout = readLayout();
  // Sortable 已经移动了真实 DOM，而 Vue 的 vnode 仍记得旧位置。先把节点放回
  // 原处，再让响应式数据驱动重排；提交失败时数据回滚即可恢复原样。
  restoreItem(origin);
  dragging.value = false;
  clearHighlight();
  lastDragEnd = Date.now();
  if (!layout) return;
  // 落进折叠的分组里，就把那一组展开，让用户看到搬过去的结果。
  const target = layout.find((entry) => entry.id === draggedId);
  if (target) collapsed.value = collapsed.value.filter((id) => id !== target.groupId);
  void saveLayout(layout);
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

/** 分组顺序同样落在服务端的 group.position 上，拖动后逐个提交新位置。 */
function readGroupOrder(): number[] {
  const element = groupList.value;
  if (!element) return [];
  return [...element.children]
    .map((child) => Number((child as HTMLElement).dataset.groupId))
    .filter((id) => Number.isFinite(id));
}

function finishGroupDrag() {
  const origin = groupDragOrigin;
  groupDragOrigin = null;
  const order = readGroupOrder();
  restoreItem(origin);
  draggingGroups.value = false;
  if (order.length) void saveGroupOrder(order);
}

function applyGroupOrder(order: number[]) {
  const byId = new Map(localGroups.value.map((group) => [group.id, group]));
  order.forEach((id, position) => {
    const group = byId.get(id);
    if (group) group.position = position;
  });
  localGroups.value = [...localGroups.value];
}

async function saveGroupOrder(order: number[]) {
  const snapshot = localGroups.value.map((group) => ({ ...group }));
  const previous = new Map(localGroups.value.map((group) => [group.id, group.position]));
  const changes = order
    .map((id, position) => ({ id, position }))
    .filter((entry) => previous.get(entry.id) !== entry.position);
  applyGroupOrder(order);
  if (!changes.length) return;
  busy.value = true;
  try {
    await Promise.all(changes.map((entry) => api.updateGroup(entry.id, { position: entry.position })));
  } catch (err) {
    localGroups.value = snapshot;
    reportError(err, "分组顺序未保存");
  } finally {
    busy.value = false;
  }
}

function isCollapsed(id: number): boolean {
  return collapsed.value.includes(id);
}

function toggleCollapse(id: number) {
  collapsed.value = isCollapsed(id)
    ? collapsed.value.filter((value) => value !== id)
    : [...collapsed.value, id];
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

function beginCreateGroup() {
  creatingGroup.value = true;
  newGroupName.value = "";
}

function cancelCreateGroup() {
  creatingGroup.value = false;
  newGroupName.value = "";
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
  mountGroupSortable();
  window.addEventListener("keydown", onEscape);
});

// 新建分组、设备从空变非空都会让新的 .rows 出现；每次更新补挂一次，幂等。
onUpdated(() => {
  mountGroupSortable();
  mountSortables();
});

onBeforeUnmount(() => {
  window.removeEventListener("keydown", onEscape);
  for (const instance of sortables.values()) instance.destroy();
  sortables.clear();
  groupSortable?.destroy();
  groupSortable = null;
});
</script>

<template>
  <section class="page devices-page">
    <div class="page-head">
      <div>
        <h1 class="page-title">设备</h1>
        <div class="device-stats">
          <span class="stat"><b>{{ stats.total }}</b> 台设备</span>
          <template v-if="stats.total">
            <span class="stat"><span class="dot" />{{ stats.online }} 在线</span>
            <span class="stat"><span class="dot off" />{{ stats.offline }} 离线</span>
            <span v-if="stats.pending" class="stat"><span class="dot pending" />{{ stats.pending }} 待注册</span>
            <span v-if="stats.outdated" class="stat is-update">{{ stats.outdated }} 台待更新</span>
          </template>
          <span class="stat is-muted">{{ localGroups.length }} 个分组</span>
        </div>
      </div>
      <div class="page-actions">
        <button class="btn-with-icon" type="button" title="立即刷新设备列表" @click="emit('refresh')">
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M13.2 7.2a5.2 5.2 0 1 1-1.55-3.67" />
            <path d="M13.4 2.4v3.4H10" />
          </svg>
          刷新
        </button>
        <button class="primary" type="button" @click="emit('create')">新建设备</button>
      </div>
    </div>

    <div v-if="allOffline" class="notice">
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true">
        <path d="M8 2.2 14.4 13.4H1.6z" stroke-linejoin="round" />
        <path d="M8 6.4v3" />
        <circle cx="8" cy="11.4" r="0.5" fill="currentColor" />
      </svg>
      <div>
        <b>当前没有在线设备</b>
        <span>控制面最近没有收到任何心跳。确认种子节点可达，或等待下一轮心跳上报。</span>
      </div>
    </div>

    <template v-if="localDevices.length">
      <div class="list-head">
        <span class="list-title">全部设备</span>
        <span class="list-hint">拖动分组左侧手柄调整顺序，拖动设备行左侧手柄改归属</span>
      </div>

      <div class="cols" aria-hidden="true">
        <span></span>
        <span>设备</span>
        <span>mesh 地址</span>
        <span>状态</span>
        <span>配置</span>
        <span>版本</span>
        <span>最后在线</span>
        <span></span>
      </div>

      <div class="groups" :class="{ 'is-dragging': dragging, 'is-sorting': draggingGroups }">
        <section class="group" :data-group-id="0" :class="{ collapsed: isCollapsed(0) }">
          <DeviceGroupHead
            pinned
            :name="pinned.name"
            :count="pinned.devices.length"
            :online="pinned.online"
            :offline="pinned.offline"
            :pending="pinned.pending"
            :collapsed="isCollapsed(0)"
            @toggle="toggleCollapse(0)"
          />
          <div
            v-show="!isCollapsed(0) || dragging"
            class="rows"
            :data-sortable-group="0"
            :data-group-name="pinned.name"
          >
            <div v-if="!pinned.devices.length" class="rows-empty" :class="{ active: dragging }">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M8 3v10M3 8h10" />
              </svg>
              <span>{{ dragging ? '松开，移入「' + pinned.name + '」' : '空分组，把设备拖到这里' }}</span>
            </div>
            <template v-for="device in pinned.devices" :key="device.id">
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

        <div ref="groupList" class="group-list">
          <section
            v-for="section in groupSections"
            :key="section.id"
            class="group"
            :data-group-id="section.id"
            :class="{ collapsed: isCollapsed(section.id) }"
          >
            <DeviceGroupHead
              :name="section.name"
              :count="section.devices.length"
              :online="section.online"
              :offline="section.offline"
              :pending="section.pending"
              :collapsed="isCollapsed(section.id)"
              :editing="editingGroup === section.id"
              :confirming="confirmingGroup === section.id"
              v-model:editing-name="editingName"
              @toggle="toggleCollapse(section.id)"
              @rename="startRename(section)"
              @submit="submitRename(section)"
              @cancel="editingGroup = null"
              @remove="confirmingGroup = section.id"
              @confirm-remove="removeGroup(section)"
              @cancel-remove="confirmingGroup = null"
            />
            <div
              v-show="!isCollapsed(section.id) || dragging"
              class="rows"
              :data-sortable-group="section.id"
              :data-group-name="section.name"
            >
              <div v-if="!section.devices.length" class="rows-empty" :class="{ active: dragging }">
                <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                  <path d="M8 3v10M3 8h10" />
                </svg>
                <span>{{ dragging ? '松开，移入「' + section.name + '」' : '空分组，把设备拖到这里' }}</span>
              </div>
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

        <button v-if="!creatingGroup" class="group-add" type="button" @click="beginCreateGroup">
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true">
            <path d="M8 3.2v9.6M3.2 8h9.6" />
          </svg>
          新建分组
        </button>
        <form v-else class="inline-form group-add-form" @submit.prevent="createGroup">
          <span class="group-add-label">新建分组</span>
          <input v-model="newGroupName" type="text" maxlength="64" placeholder="分组名称" autocomplete="off" />
          <button class="primary" type="submit" :disabled="!newGroupName.trim()">创建</button>
          <button type="button" @click="cancelCreateGroup">取消</button>
        </form>
      </div>
    </template>

    <div v-else class="empty-state">
      <span class="empty-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <rect x="3" y="4" width="18" height="6" rx="1.6" />
          <rect x="3" y="14" width="18" height="6" rx="1.6" />
          <path d="M7 7h.01M7 17h.01" />
        </svg>
      </span>
      <h2>还没有设备</h2>
      <p>新建设备时填入客户端设置页上的「本机公钥」，再让那台设备点「保存并连接」认领槽位；设备先落在「未分组」，之后拖进分组归档。</p>
      <button class="primary" type="button" @click="emit('create')">新建设备</button>
    </div>
  </section>
</template>