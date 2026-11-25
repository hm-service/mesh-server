<script setup lang="ts">
import { computed, reactive, watch } from "vue";
import { api, ApiError, type ReleaseView, type ReleasesPayload } from "../api";

const props = defineProps<{ releases: ReleasesPayload | null }>();
const emit = defineEmits<{
  saved: [releases: ReleasesPayload];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

// 会发布的平台。服务端已有的平台也一并列出来，所有平台同时铺开，不做切换。
const KNOWN = ["windows/amd64", "linux/amd64"];

/** SlotDraft 是一个槽位的编辑状态：启动器与客户端包各自提交、各自报错。 */
interface SlotDraft {
  version: string;
  url: string;
  notes: string;
  busy: boolean;
  error: string;
  hint: string;
}

interface Draft {
  launcher: SlotDraft;
  client: SlotDraft;
}

const drafts = reactive<Record<string, Draft>>({});

const platforms = computed(() => {
  const seen = new Set<string>(KNOWN);
  for (const r of props.releases?.releases ?? []) seen.add(r.platform);
  return [...seen].sort();
});

function releaseIn(payload: ReleasesPayload | null, platform: string): ReleaseView | null {
  return payload?.releases.find((r) => r.platform === platform) ?? null;
}

function releaseOf(platform: string): ReleaseView | null {
  return releaseIn(props.releases, platform);
}

function emptySlot(): SlotDraft {
  return { version: "", url: "", notes: "", busy: false, error: "", hint: "" };
}

// 每个平台各存两份草稿；保存后按服务端返回的发布内容重置，不保留半截编辑。
function reset(platform: string, payload: ReleasesPayload | null = props.releases): Draft {
  const value = releaseIn(payload, platform);
  drafts[platform] = {
    launcher: { ...emptySlot(), version: value?.launcher_version ?? "", url: value?.launcher_url ?? "" },
    client: {
      ...emptySlot(),
      version: value?.app_version ?? "",
      url: value?.app_url ?? "",
      notes: value?.app_notes ?? "",
    },
  };
  return drafts[platform] as Draft;
}

watch(
  platforms,
  (list) => {
    for (const platform of list) if (!drafts[platform]) reset(platform);
  },
  { immediate: true },
);

function launcherLine(platform: string): string {
  const value = releaseOf(platform);
  if (!value || !value.launcher_version) return "尚未发布";
  const parts = [value.launcher_version];
  if (value.launcher_url) parts.push(value.launcher_url);
  return parts.join(" · ");
}

function clientLine(platform: string): string {
  const value = releaseOf(platform);
  if (!value || !value.app_version) return "尚未发布";
  const parts = [value.app_version];
  if (value.app_url) parts.push(value.app_url);
  return parts.join(" · ");
}

function fail(slot: SlotDraft, err: unknown) {
  if (err instanceof ApiError && err.status === 401) {
    emit("unauthorized");
    return;
  }
  slot.error = err instanceof Error ? err.message : "操作失败";
}

/** complete 校验两个必填输入，错误写回该槽位。 */
function complete(slot: SlotDraft): boolean {
  if (!slot.version.trim()) {
    slot.error = "请填写版本号";
    return false;
  }
  if (!slot.url.trim()) {
    slot.error = "请填写下载地址";
    return false;
  }
  return true;
}

async function saveLauncher(platform: string) {
  const draft = drafts[platform]!.launcher;
  if (draft.busy) return;
  draft.error = "";
  if (!complete(draft)) return;
  const version = draft.version.trim();
  draft.busy = true;
  draft.hint = "正在解析远程文件…（服务端下载并计算 sha256，最长约 60 秒）";
  try {
    const saved = await api.publishRelease({
      platform,
      launcher_version: version,
      launcher_url: draft.url.trim(),
    });
    emit("saved", saved);
    reset(platform, saved);
    emit("toast", "启动器已发布", platform + " " + version, "ok");
  } catch (err) {
    fail(draft, err);
  } finally {
    draft.busy = false;
    draft.hint = "";
  }
}

async function saveClient(platform: string) {
  const draft = drafts[platform]!.client;
  if (draft.busy) return;
  draft.error = "";
  if (!complete(draft)) return;
  const version = draft.version.trim();
  draft.busy = true;
  draft.hint = "正在解析远程文件…（服务端下载并计算 sha256，最长约 60 秒）";
  try {
    const saved = await api.publishRelease({
      platform,
      app_version: version,
      app_url: draft.url.trim(),
      app_notes: draft.notes,
    });
    emit("saved", saved);
    reset(platform, saved);
    emit("toast", "客户端包已发布", platform + " " + version, "ok");
  } catch (err) {
    fail(draft, err);
  } finally {
    draft.busy = false;
    draft.hint = "";
  }
}

async function clearLauncher(platform: string) {
  const draft = drafts[platform]!.launcher;
  const value = releaseOf(platform);
  if (draft.busy || !value || !value.launcher_version) return;
  draft.error = "";
  draft.busy = true;
  try {
    const saved = await api.deleteRelease(platform, "launcher");
    emit("saved", saved);
    reset(platform, saved);
    emit("toast", "已清除该平台的启动器", platform, "ok");
  } catch (err) {
    fail(draft, err);
  } finally {
    draft.busy = false;
  }
}

// 接口只提供整平台清空与只清启动器两种：这里清掉两个槽位。
async function clearPlatform(platform: string) {
  const draft = drafts[platform]!.client;
  if (draft.busy || !releaseOf(platform)) return;
  draft.error = "";
  draft.busy = true;
  try {
    const saved = await api.deleteRelease(platform);
    emit("saved", saved);
    reset(platform, saved);
    emit("toast", "已清除该平台的两个槽位", platform, "ok");
  } catch (err) {
    fail(draft, err);
  } finally {
    draft.busy = false;
  }
}
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div>
        <h1 class="page-title">客户端更新</h1>
        <p class="page-sub">启动器与客户端包各自发布；客户端按自己上报的 platform 取对应工件。</p>
      </div>
    </div>

    <div class="updates-grid">
      <section v-for="platform in platforms" :key="platform" class="panel">
        <div class="platform-head">
          <span class="platform-name">{{ platform }}</span>
          <span class="panel-sub">{{ releaseOf(platform) ? "已发布" : "尚未发布" }}</span>
        </div>

        <div class="slot">
          <div class="slot-head">
            <span class="slot-title">启动器</span>
            <span class="slot-state" :title="launcherLine(platform)">{{ launcherLine(platform) }}</span>
          </div>
          <div class="form-grid">
            <div class="field">
              <label :for="platform + '-launcher-version'">版本号</label>
              <input
                :id="platform + '-launcher-version'"
                v-model="drafts[platform]!.launcher.version"
                type="text"
                maxlength="64"
                placeholder="2026.11.25-36"
                autocomplete="off"
              />
            </div>
            <div class="field">
              <label :for="platform + '-launcher-url'">下载地址</label>
              <input
                :id="platform + '-launcher-url'"
                v-model="drafts[platform]!.launcher.url"
                type="text"
                placeholder="https://…/mesh.exe"
                autocomplete="off"
              />
            </div>
          </div>
          <div v-if="drafts[platform]!.launcher.hint" class="hint">{{ drafts[platform]!.launcher.hint }}</div>
          <div v-if="drafts[platform]!.launcher.error" class="err">{{ drafts[platform]!.launcher.error }}</div>
          <div class="actions">
            <button class="primary" type="button" :disabled="drafts[platform]!.launcher.busy" @click="saveLauncher(platform)">
              {{ drafts[platform]!.launcher.busy ? "解析中…" : "保存" }}
            </button>
            <button
              v-if="releaseOf(platform)?.launcher_version"
              class="danger"
              type="button"
              :disabled="drafts[platform]!.launcher.busy"
              @click="clearLauncher(platform)"
            >
              清除启动器
            </button>
          </div>
        </div>

        <div class="slot">
          <div class="slot-head">
            <span class="slot-title">客户端包</span>
            <span class="slot-state" :title="clientLine(platform)">{{ clientLine(platform) }}</span>
          </div>
          <div class="form-grid">
            <div class="field">
              <label :for="platform + '-version'">版本号</label>
              <input
                :id="platform + '-version'"
                v-model="drafts[platform]!.client.version"
                type="text"
                maxlength="64"
                placeholder="2026.11.25-12"
                autocomplete="off"
              />
            </div>
            <div class="field">
              <label :for="platform + '-url'">下载地址</label>
              <input
                :id="platform + '-url'"
                v-model="drafts[platform]!.client.url"
                type="text"
                placeholder="https://…/mesh-client.tar.zst"
                autocomplete="off"
              />
            </div>
            <div class="field full">
              <label :for="platform + '-notes'">说明（可选，随更新下发给客户端）</label>
              <textarea
                :id="platform + '-notes'"
                v-model="drafts[platform]!.client.notes"
                rows="2"
                maxlength="2000"
                spellcheck="false"
              />
            </div>
          </div>
          <div v-if="drafts[platform]!.client.hint" class="hint">{{ drafts[platform]!.client.hint }}</div>
          <div v-if="drafts[platform]!.client.error" class="err">{{ drafts[platform]!.client.error }}</div>
          <div class="actions">
            <button class="primary" type="button" :disabled="drafts[platform]!.client.busy" @click="saveClient(platform)">
              {{ drafts[platform]!.client.busy ? "解析中…" : "保存" }}
            </button>
            <button
              v-if="releaseOf(platform)"
              class="danger"
              type="button"
              title="同时清除该平台的启动器槽位"
              :disabled="drafts[platform]!.client.busy"
              @click="clearPlatform(platform)"
            >
              清除该平台
            </button>
          </div>
        </div>
      </section>
    </div>
  </section>
</template>
