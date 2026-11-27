<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { api, ApiError, type DeviceSummary, type GroupView, type ReleasesPayload } from "./api";
import ConfigView from "./components/ConfigView.vue";
import DevicesView from "./components/DevicesView.vue";
import LoginView from "./components/LoginView.vue";
import NewDeviceDialog from "./components/NewDeviceDialog.vue";
import ToastHost, { type Toast } from "./components/ToastHost.vue";
import UpdatesView from "./components/UpdatesView.vue";

type Page = "devices" | "config" | "updates";

const POLL_MS = 5000;

const page = ref<Page>("devices");
const authed = ref(false);
const devices = ref<DeviceSummary[]>([]);
const groups = ref<GroupView[]>([]);
const releases = ref<ReleasesPayload | null>(null);
const showNew = ref(false);
const toast = ref<Toast | null>(null);
const loginError = ref("");
const loginBusy = ref(false);

let pollTimer: number | undefined;
let toastTimer: number | undefined;
// 会话代次：提交登录与结束会话各递增一次。请求记下发出时的代次，只有当前代次的
// 响应才写界面，换会话前后在途的请求一律丢弃。轮询因此不串行：串行只防同一会话内的
// 重叠，挡不住旧会话的响应落到新会话上。
let session = 0;

const deviceCount = computed(() => devices.value.length);

function showToast(title: string, message = "", kind: "ok" | "err" = "ok") {
  toast.value = { title, message, kind };
  if (toastTimer !== undefined) window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => {
    toast.value = null;
  }, kind === "err" ? 6000 : 3200);
}

function dismissToast() {
  toast.value = null;
  if (toastTimer !== undefined) window.clearTimeout(toastTimer);
}

/**
 * 会话失效的唯一出口：清凭证、回登录页、停轮询、丢界面缓存。
 * 只有当前会话的 401 或用户主动退出能到这里：自己的请求先按代次判定，
 * 子组件的 401 只会来自当前挂载的面板（换会话时面板整体卸载，旧实例的
 * 事件不再送达），因此都不会误清新会话的凭证。
 */
function endSession(): void {
  session += 1;
  api.logout();
  stopPolling();
  showNew.value = false;
  groups.value = [];
  releases.value = null;
  devices.value = [];
  authed.value = false;
}

function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401;
}

/**
 * 读取请求失败的统一判定：当前会话的 401 走 endSession，旧代次的响应直接丢弃，
 * 其余错误返回 false，由调用方决定怎么提示。
 */
function handleReadError(err: unknown, gen: number): boolean {
  if (gen !== session) return true;
  if (!isUnauthorized(err)) return false;
  endSession();
  return true;
}

function startPolling(gen: number): void {
  stopPolling();
  pollTimer = window.setInterval(() => {
    void loadDevices(gen).catch((err) => {
      // 轮询失败保持上一屏数据，下一轮再试。
      handleReadError(err, gen);
    });
  }, POLL_MS);
}

function stopPolling(): void {
  if (pollTimer !== undefined) {
    window.clearInterval(pollTimer);
    pollTimer = undefined;
  }
}

/** 取设备与分组。失败原样抛出，由调用方决定结束会话还是提示；旧代次的响应不落地。 */
async function loadDevices(gen: number): Promise<void> {
  const [nextDevices, nextGroups] = await Promise.all([api.listDevices(), api.listGroups()]);
  if (gen !== session) return;
  devices.value = nextDevices;
  groups.value = nextGroups;
}

async function loadReleases(gen: number): Promise<void> {
  try {
    const next = await api.getReleases();
    if (gen === session) releases.value = next;
  } catch (err) {
    handleReadError(err, gen);
  }
}

/** 登录成功的唯一路径：设备列表真的取到了才算进来，之后才开轮询。 */
async function enterConsole(gen: number): Promise<void> {
  await loadDevices(gen);
  if (gen !== session) return;
  authed.value = true;
  page.value = "devices";
  startPolling(gen);
  void loadReleases(gen);
}

async function login(token: string): Promise<void> {
  // 新会话从提交登录开始算：此前在途的请求（含首屏初始化）都属旧代次。
  session += 1;
  const gen = session;
  loginError.value = "";
  loginBusy.value = true;
  try {
    await api.auth(token);
  } catch (err) {
    loginError.value = isUnauthorized(err) ? "访问令牌错误" : loginFailureText(err);
    loginBusy.value = false;
    return;
  }
  try {
    await enterConsole(gen);
  } catch (err) {
    // 初始化没成功就不算登录：清掉刚换来的凭证，留在登录页。
    endSession();
    loginError.value = loginFailureText(err);
  }
  loginBusy.value = false;
}

function loginFailureText(err: unknown): string {
  return err instanceof Error ? err.message : "登录失败";
}

async function refreshDevices(): Promise<void> {
  const gen = session;
  try {
    await loadDevices(gen);
  } catch (err) {
    if (handleReadError(err, gen)) return;
    showToast("刷新失败", err instanceof Error ? err.message : "", "err");
  }
}

function onCreated(): void {
  const gen = session;
  void loadDevices(gen).catch((err) => {
    // 新建设备已经成功，列表下一轮轮询也会刷新；这里只保证 401 能结束会话。
    handleReadError(err, gen);
  });
}

function onSaved(next: ReleasesPayload) {
  releases.value = next;
}

// 设备详情的内联展开由 DevicesView 自己关；这里只剩新建设备的对话框。
function closeOverlays() {
  if (showNew.value) showNew.value = false;
}

function onEscape(event: KeyboardEvent) {
  if (event.key === "Escape") closeOverlays();
}

onMounted(async () => {
  window.addEventListener("keydown", onEscape);
  const gen = session;
  try {
    await enterConsole(gen);
  } catch (err) {
    // 初始化没成功就不进控制台：401 结束会话，其余错误停在登录页。
    handleReadError(err, gen);
  }
});

onBeforeUnmount(() => {
  window.removeEventListener("keydown", onEscape);
  stopPolling();
  if (toastTimer !== undefined) window.clearTimeout(toastTimer);
});
</script>

<template>
  <LoginView v-if="!authed" :error="loginError" :busy="loginBusy" @submit="login" />

  <template v-else>
    <div class="frame">
      <header class="app-header">
      <div class="brand">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round">
          <path d="M12 4.4 19.3 17.9H4.7z" />
          <circle cx="12" cy="4.4" r="2.1" fill="#fff" />
          <circle cx="19.3" cy="17.9" r="2.1" fill="#fff" />
          <circle cx="4.7" cy="17.9" r="2.1" fill="#fff" />
          <circle cx="12" cy="13.2" r="1.6" fill="#fff" />
        </svg>
        Mesh <small>控制平台</small>
      </div>
      <div class="spacer" />
      <span class="who">admin</span>
      <button type="button" @click="endSession">退出</button>
      </header>

      <div class="shell">
        <aside class="side">
        <button class="nav" :class="{ on: page === 'devices' }" type="button" @click="page = 'devices'">
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5">
            <rect x="2.2" y="2.2" width="5" height="5" rx="1.2" />
            <rect x="8.8" y="2.2" width="5" height="5" rx="1.2" />
            <rect x="2.2" y="8.8" width="5" height="5" rx="1.2" />
            <rect x="8.8" y="8.8" width="5" height="5" rx="1.2" />
          </svg>
          设备
          <span class="count">{{ deviceCount || "" }}</span>
        </button>
        <button class="nav" :class="{ on: page === 'config' }" type="button" @click="page = 'config'">
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round">
            <path d="M3 3.5h10M3 8h10M3 12.5h10" />
            <circle cx="6.2" cy="3.5" r="1.4" fill="#fff" />
            <circle cx="9.8" cy="8" r="1.4" fill="#fff" />
            <circle cx="5" cy="12.5" r="1.4" fill="#fff" />
          </svg>
          默认模板
        </button>
        <button class="nav" :class="{ on: page === 'updates' }" type="button" @click="page = 'updates'">
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <path d="M8 2.6v5.8" />
            <path d="M5.5 6.2 8 8.7l2.5-2.5" />
            <path d="M2.9 11.3v1.4c0 .5.4.9.9.9h8.4c.5 0 .9-.4.9-.9v-1.4" />
          </svg>
          客户端更新
        </button>
      </aside>

      <main class="content">
        <DevicesView
          v-if="page === 'devices'"
          :devices="devices"
          :groups="groups"
          @create="showNew = true"
          @refresh="refreshDevices"
          @toast="showToast"
          @unauthorized="endSession"
        />
        <ConfigView
          v-else-if="page === 'config'"
          @saved="onSaved"
          @toast="showToast"
          @unauthorized="endSession"
        />
        <UpdatesView
          v-else
          :releases="releases"
          @saved="onSaved"
          @toast="showToast"
          @unauthorized="endSession"
        />
        </main>
      </div>
    </div>

    <NewDeviceDialog
      v-if="showNew"
      :devices="devices"
      @close="showNew = false"
      @created="onCreated"
      @toast="showToast"
      @unauthorized="endSession"
    />
  </template>

  <ToastHost :toast="toast" @close="dismissToast" />
</template>
