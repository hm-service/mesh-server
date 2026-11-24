<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { api, ApiError, type ConsoleSettings } from "../api";
import { formatMB } from "../format";

const props = defineProps<{ settings: ConsoleSettings | null }>();
const emit = defineEmits<{
  saved: [settings: ConsoleSettings];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

const mode = ref<"upload" | "url">("upload");
const version = ref("");
const urlVersion = ref("");
const url = ref("");
const notes = ref("");
const file = ref<File | null>(null);
const error = ref("");
const hint = ref("");
const busy = ref(false);

watch(
  () => props.settings,
  (value) => {
    if (!value) return;
    version.value = value.app_version;
    urlVersion.value = value.app_version;
    url.value = value.app_source === "url" ? value.app_url : "";
    notes.value = value.app_notes;
    mode.value = value.app_source === "url" ? "url" : "upload";
  },
  { immediate: true },
);

const releaseLine = computed(() => {
  const value = props.settings;
  if (!value || !value.app_version) return "当前未发布客户端更新";
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
    let saved: ConsoleSettings;
    if (mode.value === "upload") {
      const chosen = file.value;
      if (!chosen) {
        error.value = "请选择客户端 exe";
        return;
      }
      const form = new FormData();
      form.append("mode", "upload");
      form.append("app_version", targetVersion);
      form.append("app_notes", notes.value);
      form.append("file", chosen, chosen.name);
      saved = await api.uploadRelease(form);
      file.value = null;
    } else {
      hint.value = "正在解析远程文件…（服务端下载并计算 hash，最长约 60 秒）";
      saved = await api.putSettings({ app_version: targetVersion, app_url: url.value.trim(), app_notes: notes.value });
    }
    emit("saved", saved);
    emit("toast", "客户端更新已发布", saved.app_version, "ok");
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "保存失败";
  } finally {
    busy.value = false;
    hint.value = "";
  }
}
</script>

<template>
  <section class="page">
    <div class="page-head">
      <h1>客户端更新</h1>
      <span class="desc">客户端心跳时比对 hash，不一致就下发新版本</span>
    </div>
    <div class="panel narrow">
      <div class="panel-head">
        <h2>发布客户端</h2>
        <span class="desc">上传或指向外部地址</span>
      </div>
      <div class="panel-body">
        <div class="release">{{ releaseLine }}</div>
        <div class="seg">
          <button type="button" :class="{ on: mode === 'upload' }" @click="mode = 'upload'">上传</button>
          <button type="button" :class="{ on: mode === 'url' }" @click="mode = 'url'">外部地址</button>
        </div>
        <div v-if="mode === 'upload'" class="row2">
          <div>
            <label for="upload-version">版本号</label>
            <input id="upload-version" v-model="version" type="text" maxlength="64" placeholder="2026.11.24-3" autocomplete="off" />
          </div>
          <div>
            <label for="upload-file">客户端 exe</label>
            <input id="upload-file" type="file" accept=".exe" @change="onFile" />
          </div>
        </div>
        <div v-else class="row2">
          <div>
            <label for="url-version">版本号</label>
            <input id="url-version" v-model="urlVersion" type="text" maxlength="64" placeholder="2026.11.24-3" autocomplete="off" />
          </div>
          <div>
            <label for="url-address">下载地址</label>
            <input id="url-address" v-model="url" type="text" placeholder="http://.../mesh-client-2026.11.24-3.exe" autocomplete="off" />
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
        </div>
      </div>
    </div>
  </section>
</template>
