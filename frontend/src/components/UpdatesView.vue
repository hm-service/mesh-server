<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { api, ApiError, type ReleaseView, type ReleasesPayload } from "../api";
import { formatMB } from "../format";

const props = defineProps<{ releases: ReleasesPayload | null }>();
const emit = defineEmits<{
  saved: [releases: ReleasesPayload];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

// 预置两个我们会发布的平台；服务端已有的平台也一并列出来。
const KNOWN = ["windows/amd64", "linux/amd64"];

const platform = ref<string>(KNOWN[0] as string);
const mode = ref<"upload" | "url">("upload");
const version = ref("");
const urlVersion = ref("");
const url = ref("");
const notes = ref("");
const file = ref<File | null>(null);
const error = ref("");
const hint = ref("");
const busy = ref(false);

const platforms = computed(() => {
  const seen = new Set<string>(KNOWN);
  for (const r of props.releases?.releases ?? []) seen.add(r.platform);
  return [...seen].sort();
});

const current = computed<ReleaseView | null>(() => {
  return props.releases?.releases.find((r) => r.platform === platform.value) ?? null;
});

watch(
  [() => props.releases, platform],
  () => {
    const value = current.value;
    version.value = value?.app_version ?? "";
    urlVersion.value = value?.app_version ?? "";
    url.value = value?.app_source === "url" ? value.app_url : "";
    notes.value = value?.app_notes ?? "";
    mode.value = value?.app_source === "url" ? "url" : "upload";
  },
  { immediate: true },
);

const releaseLine = computed(() => {
  const value = current.value;
  if (!value || !value.app_version) return "该平台还没有发布更新";
  const parts = ["当前", value.app_source === "url" ? "外部地址" : "上传", value.app_version];
  if (value.app_source !== "url") parts.push(formatMB(value.app_size));
  if (value.app_hash) parts.push("hash " + value.app_hash);
  if (value.app_source === "url" && value.app_url) parts.push(value.app_url);
  return parts.join(" · ");
});

function onFile(event: Event) {
  const input = event.target as HTMLInputElement;
  file.value = input.files && input.files.length > 0 ? input.files[0] : null;
}

function fail(err: unknown) {
  if (err instanceof ApiError && err.status === 401) {
    emit("unauthorized");
    return;
  }
  error.value = err instanceof Error ? err.message : "操作失败";
}

async function save() {
  if (busy.value) return;
  error.value = "";
  const targetVersion = (mode.value === "url" ? urlVersion.value : version.value).trim();
  if (!targetVersion) {
    error.value = "请填写版本号";
    return;
  }
  busy.value = true;
  hint.value = "";
  try {
    let saved: ReleasesPayload;
    if (mode.value === "upload") {
      const chosen = file.value;
      if (!chosen) {
        error.value = "请选择更新包";
        return;
      }
      const form = new FormData();
      form.append("platform", platform.value);
      form.append("app_version", targetVersion);
      form.append("app_notes", notes.value);
      form.append("file", chosen, chosen.name);
      saved = await api.uploadRelease(form);
      file.value = null;
    } else {
      hint.value = "正在解析远程文件…（服务端下载并计算 hash，最长约 60 秒）";
      saved = await api.publishRelease({
        platform: platform.value,
        app_version: targetVersion,
        app_url: url.value.trim(),
        app_notes: notes.value,
      });
    }
    emit("saved", saved);
    emit("toast", "客户端更新已发布", platform.value + " " + targetVersion, "ok");
  } catch (err) {
    fail(err);
  } finally {
    busy.value = false;
    hint.value = "";
  }
}

async function remove() {
  if (busy.value || !current.value) return;
  error.value = "";
  busy.value = true;
  try {
    const next = await api.deleteRelease(platform.value);
    emit("saved", next);
    emit("toast", "已清除该平台的发布", platform.value, "ok");
  } catch (err) {
    fail(err);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="page">
    <div class="page-head">
      <h1>客户端更新</h1>
      <span class="desc">按平台发布，客户端用自己上报的 platform 取对应更新包</span>
    </div>
    <div class="panel narrow">
      <div class="panel-head">
        <h2>发布客户端</h2>
        <span class="desc">平台之间互不影响</span>
      </div>
      <div class="panel-body">
        <div class="row2">
          <div>
            <label for="release-platform">平台</label>
            <select id="release-platform" v-model="platform">
              <option v-for="item in platforms" :key="item" :value="item">{{ item }}</option>
            </select>
          </div>
          <div>
            <label>当前状态</label>
            <div class="release">{{ releaseLine }}</div>
          </div>
        </div>
        <div class="seg">
          <button type="button" :class="{ on: mode === 'upload' }" @click="mode = 'upload'">上传</button>
          <button type="button" :class="{ on: mode === 'url' }" @click="mode = 'url'">外部地址</button>
        </div>
        <div v-if="mode === 'upload'" class="row2">
          <div>
            <label for="upload-version">版本号</label>
            <input id="upload-version" v-model="version" type="text" maxlength="64" placeholder="2026.11.25-10" autocomplete="off" />
          </div>
          <div>
            <label for="upload-file">更新包</label>
            <input id="upload-file" type="file" accept=".zst,.tar.zst" @change="onFile" />
          </div>
        </div>
        <div v-else class="row2">
          <div>
            <label for="url-version">版本号</label>
            <input id="url-version" v-model="urlVersion" type="text" maxlength="64" placeholder="2026.11.25-10" autocomplete="off" />
          </div>
          <div>
            <label for="url-address">下载地址</label>
            <input id="url-address" v-model="url" type="text" placeholder="https://.../windows-amd64-2026.11.25-10-mesh-client.tar.zst" autocomplete="off" />
          </div>
        </div>
        <label class="gap" for="app-notes">说明（可选，随更新下发给客户端）</label>
        <textarea id="app-notes" v-model="notes" class="notes" maxlength="2000" spellcheck="false" />
        <div v-if="hint" class="hint">{{ hint }}</div>
        <div v-if="error" class="err">{{ error }}</div>
        <div class="actions">
          <button class="primary" type="button" :disabled="busy" @click="save">
            {{ busy ? (mode === "upload" ? "上传中…" : "解析中…") : "保存" }}
          </button>
          <button v-if="current" class="danger" type="button" :disabled="busy" @click="remove">清除该平台</button>
        </div>
      </div>
    </div>
  </section>
</template>
