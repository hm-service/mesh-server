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

/** SlotDraft 是两个槽位共用的编辑状态：各自提交、各自报错。 */
interface SlotDraft {
  url: string;
  /** 离线发布私钥对这个槽位的签名（128 位十六进制），发布时必填。 */
  signature: string;
  busy: boolean;
  error: string;
  hint: string;
}

/** LauncherDraft 是启动器槽位：裸二进制没有清单，版本号由发布者填写。 */
interface LauncherDraft extends SlotDraft {
  version: string;
}

/** ClientDraft 是客户端包槽位：版本号由服务端从包内 version.json 读出，autoUpdate 跟着这个包一起发布。 */
interface ClientDraft extends SlotDraft {
  notes: string;
  autoUpdate: boolean;
}

interface Draft {
  launcher: LauncherDraft;
  client: ClientDraft;
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
  return { url: "", signature: "", busy: false, error: "", hint: "" };
}

// 每个平台各存两份草稿；保存后按服务端返回的发布内容重置，不保留半截编辑。
function reset(platform: string, payload: ReleasesPayload | null = props.releases): Draft {
  const value = releaseIn(payload, platform);
  drafts[platform] = {
    launcher: {
      ...emptySlot(),
      version: value?.launcherVersion ?? "",
      url: value?.launcherUrl ?? "",
      signature: value?.launcherSignature ?? "",
    },
    client: {
      ...emptySlot(),
      url: value?.appUrl ?? "",
      signature: value?.appSignature ?? "",
      notes: value?.appNotes ?? "",
      autoUpdate: value?.autoUpdate ?? false,
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
  if (!value || !value.launcherVersion) return "尚未发布";
  const parts = [value.launcherVersion];
  if (value.launcherUrl) parts.push(value.launcherUrl);
  return parts.join(" · ");
}

function clientLine(platform: string): string {
  const value = releaseOf(platform);
  if (!value || !value.appVersion) return "尚未发布";
  const parts = [value.appVersion];
  if (value.appUrl) parts.push(value.appUrl);
  parts.push(value.autoUpdate ? "自动安装" : "手动安装");
  return parts.join(" · ");
}

function fail(slot: SlotDraft, err: unknown) {
  if (err instanceof ApiError && err.status === 401) {
    emit("unauthorized");
    return;
  }
  slot.error = err instanceof Error ? err.message : "操作失败";
}

/** requireURL 校验下载地址，错误写回该槽位。 */
function requireURL(slot: SlotDraft): boolean {
  if (!slot.url.trim()) {
    slot.error = "请填写下载地址";
    return false;
  }
  return true;
}

/**
 * requireSignature 校验发布签名。签名覆盖槽位、平台、版本与摘要，服务端会重新
 * 下载同一个文件再对一遍：它不能在这里算出来，只能由持有私钥的人给出。
 */
function requireSignature(slot: SlotDraft): boolean {
  const value = slot.signature.trim().toLowerCase();
  if (!/^[0-9a-f]{128}$/.test(value)) {
    slot.error = "发布签名必须是 128 位十六进制（64 字节 Ed25519 签名）";
    return false;
  }
  slot.signature = value;
  return true;
}

async function saveLauncher(platform: string) {
  const draft = drafts[platform]!.launcher;
  if (draft.busy) return;
  draft.error = "";
  if (!draft.version.trim()) {
    draft.error = "请填写版本号";
    return;
  }
  if (!requireURL(draft)) return;
  if (!requireSignature(draft)) return;
  const version = draft.version.trim();
  draft.busy = true;
  draft.hint = "正在解析远程文件…（服务端下载并计算 sha256，最长约 60 秒）";
  try {
    const saved = await api.publishRelease({
      platform,
      launcherVersion: version,
      launcherUrl: draft.url.trim(),
      launcherSignature: draft.signature,
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
  if (!requireURL(draft)) return;
  if (!requireSignature(draft)) return;
  draft.busy = true;
  draft.hint = "正在解析远程文件…（服务端下载、读出包内版本并计算 sha256，最长约 60 秒）";
  try {
    // 版本号不从表单取：服务端从包内 version.json 读出，这里只回显它。
    const saved = await api.publishRelease({
      platform,
      appUrl: draft.url.trim(),
      appSignature: draft.signature,
      appNotes: draft.notes,
      autoUpdate: draft.autoUpdate,
    });
    emit("saved", saved);
    reset(platform, saved);
    const version = releaseIn(saved, platform)?.appVersion ?? "";
    emit("toast", "客户端包已发布", version ? platform + " " + version : platform, "ok");
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
  if (draft.busy || !value || !value.launcherVersion) return;
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
            <div class="field full">
              <label :for="platform + '-launcher-signature'">发布签名</label>
              <input
                :id="platform + '-launcher-signature'"
                v-model="drafts[platform]!.launcher.signature"
                type="text"
                placeholder="128 位十六进制"
                autocomplete="off"
                spellcheck="false"
              />
              <span class="hint">覆盖槽位、平台、版本与摘要；用 server/tools/signpkg 签出，服务端会重新下载同一份文件再对一遍。</span>
            </div>
          </div>
          <div v-if="drafts[platform]!.launcher.hint" class="hint">{{ drafts[platform]!.launcher.hint }}</div>
          <div v-if="drafts[platform]!.launcher.error" class="err">{{ drafts[platform]!.launcher.error }}</div>
          <div class="actions">
            <button class="primary" type="button" :disabled="drafts[platform]!.launcher.busy" @click="saveLauncher(platform)">
              {{ drafts[platform]!.launcher.busy ? "解析中…" : "保存" }}
            </button>
            <button
              v-if="releaseOf(platform)?.launcherVersion"
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
            <div class="field full">
              <label :for="platform + '-url'">下载地址</label>
              <input
                :id="platform + '-url'"
                v-model="drafts[platform]!.client.url"
                type="text"
                placeholder="https://…/mesh-client.tar.zst"
                autocomplete="off"
              />
              <span class="hint">版本号不在这里填：服务端从包内 version.json 读出，发布后按它比较设备版本。</span>
            </div>
            <div class="field full">
              <label :for="platform + '-app-signature'">发布签名</label>
              <textarea
                :id="platform + '-app-signature'"
                v-model="drafts[platform]!.client.signature"
                rows="2"
                placeholder="128 位十六进制"
                spellcheck="false"
              />
              <span class="hint">签的是包内 version.json 声明的版本与服务端算出的摘要；两项都在发布时才定，所以签名只能由发布私钥持有者事后给出（build/publish.ps1 会自动签）。</span>
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
            <div class="field full">
              <label class="check" :for="platform + '-auto-update'">
                <input
                  :id="platform + '-auto-update'"
                  v-model="drafts[platform]!.client.autoUpdate"
                  type="checkbox"
                />
                <span>客户端收到这个版本后自动安装（不勾选则只提示，等用户手动确认）</span>
              </label>
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
