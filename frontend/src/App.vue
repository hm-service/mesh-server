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

function startPolling() {
  stopPolling();
  pollTimer = window.setInterval(() => {
    void loadDevices().catch(() => {
      // 轮询失败保持上一屏数据，下一轮再试；401 已经在 loadDevices 里处理。
    });
  }, POLL_MS);
}

function stopPolling() {
  if (pollTimer !== undefined) {
    window.clearInterval(pollTimer);
    pollTimer = undefined;
  }
}

function handleUnauthorized() {
  stopPolling();
  showNew.value = false;
  groups.value = [];
  releases.value = null;
  devices.value = [];
  authed.value = false;
}

async function loadDevices() {
  try {
    const [nextDevices, nextGroups] = await Promise.all([api.listDevices(), api.listGroups()]);
    devices.value = nextDevices;
    groups.value = nextGroups;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      handleUnauthorized();
      return;
    }
    throw err;
  }
}

async function loadReleases() {
  try {
    releases.value = await api.getReleases();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) handleUnauthorized();
  }
}

async function login(token: string) {
  loginError.value = "";
  loginBusy.value = true;
  try {
    await api.auth(token);
    await loadDevices();
    authed.value = true;
    page.value = "devices";
    startPolling();
    void loadReleases();
  } catch (err) {
    loginError.value = err instanceof ApiError && err.status === 401 ? "访问令牌错误" : err instanceof Error ? err.message : "登录失败";
  } finally {
    loginBusy.value = false;
  }
}

function logout() {
  api.logout();
  handleUnauthorized();
}

async function refreshDevices() {
  try {
    await loadDevices();
  } catch (err) {
    showToast("刷新失败", err instanceof Error ? err.message : "", "err");
  }
}

function onCreated() {
  void loadDevices().catch(() => {});
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
  try {
    await loadDevices();
    authed.value = true;
    startPolling();
    void loadReleases();
  } catch {
    authed.value = false;
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
      <button type="button" @click="logout">退出</button>
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
          @unauthorized="handleUnauthorized"
        />
        <ConfigView
          v-else-if="page === 'config'"
          @saved="onSaved"
          @toast="showToast"
          @unauthorized="handleUnauthorized"
        />
        <UpdatesView
          v-else
          :releases="releases"
          @saved="onSaved"
          @toast="showToast"
          @unauthorized="handleUnauthorized"
        />
        </main>
      </div>
    </div>

    <NewDeviceDialog
      v-if="showNew"
      @close="showNew = false"
      @created="onCreated"
      @toast="showToast"
      @unauthorized="handleUnauthorized"
    />
  </template>

  <ToastHost :toast="toast" @close="dismissToast" />
</template>
