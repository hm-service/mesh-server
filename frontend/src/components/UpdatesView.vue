<script setup lang="ts">
import { computed, reactive, watch } from "vue";
import { api, ApiError, type ReleaseView, type ReleasesPayload } from "../api";
import { formatMB } from "../format";

const props = defineProps<{ releases: ReleasesPayload | null }>();
const emit = defineEmits<{
  saved: [releases: ReleasesPayload];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

// 会发布的平台。服务端已有的平台也一并列出来，所有平台同时铺开，不做切换。
const KNOWN = ["windows/amd64", "linux/amd64"];

interface Draft {
  mode: "upload" | "url";
  version: string;
  url: string;
  notes: string;
  file: File | null;
  busy: boolean;
  error: string;
  hint: string;
}

const drafts = reactive<Record<string, Draft>>({});

const platforms = computed(() => {
  const seen = new Set<string>(KNOWN);
  for (const r of props.releases?.releases ?? []) seen.add(r.platform);
  return [...seen].sort();
});

function releaseOf(platform: string): ReleaseView | null {
  return props.releases?.releases.find((r) => r.platform === platform) ?? null;
}

// 每个平台各存一份草稿；刷新后回到服务端的当前发布，不保留半截编辑。
function reset(platform: string): Draft {
  const value = releaseOf(platform);
  drafts[platform] = {
    mode: value?.app_source === "url" ? "url" : "upload",
    version: value?.app_version ?? "",
    url: value?.app_source === "url" ? value.app_url : "",
    notes: value?.app_notes ?? "",
    file: null,
    busy: false,
    error: "",
    hint: "",
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

function stateLine(platform: string): string {
  const value = releaseOf(platform);
  if (!value || !value.app_version) return "尚未发布";
  const parts = [value.app_source === "url" ? "外部地址" : "上传", value.app_version];
  if (value.app_source !== "url") parts.push(formatMB(value.app_size));
  if (value.app_hash) parts.push("hash " + value.app_hash);
  if (value.app_source === "url" && value.app_url) parts.push(value.app_url);
  return parts.join(" · ");
}

function onFile(platform: string, event: Event) {
  const input = event.target as HTMLInputElement;
  drafts[platform]!.file = input.files && input.files.length > 0 ? input.files[0] : null;
}

function fail(platform: string, err: unknown) {
  if (err instanceof ApiError && err.status === 401) {
    emit("unauthorized");
    return;
  }
  drafts[platform]!.error = err instanceof Error ? err.message : "操作失败";
}

async function save(platform: string) {
  const draft = drafts[platform]!;
  if (draft.busy) return;
  draft.error = "";
  const version = draft.version.trim();
  if (!version) {
    draft.error = "请填写版本号";
    return;
  }
  draft.busy = true;
  draft.hint = "";
  try {
    let saved: ReleasesPayload;
    if (draft.mode === "upload") {
      const chosen = draft.file;
      if (!chosen) {
        draft.error = "请选择更新包";
        return;
      }
      const form = new FormData();
      form.append("platform", platform);
      form.append("app_version", version);
      form.append("app_notes", draft.notes);
      form.append("file", chosen, chosen.name);
      saved = await api.uploadRelease(form);
    } else {
      if (!draft.url.trim()) {
        draft.error = "请填写下载地址";
        return;
      }
      draft.hint = "正在解析远程文件…（服务端下载并计算 hash，最长约 60 秒）";
      saved = await api.publishRelease({
        platform,
        app_version: version,
        app_url: draft.url.trim(),
        app_notes: draft.notes,
      });
    }
    emit("saved", saved);
    reset(platform);
    emit("toast", "客户端更新已发布", platform + " " + version, "ok");
  } catch (err) {
    fail(platform, err);
  } finally {
    draft.busy = false;
    draft.hint = "";
  }
}

async function remove(platform: string) {
  const draft = drafts[platform]!;
  if (draft.busy || !releaseOf(platform)) return;
  draft.error = "";
  draft.busy = true;
  try {
    const next = await api.deleteRelease(platform);
    emit("saved", next);
    reset(platform);
    emit("toast", "已清除该平台的发布", platform, "ok");
  } catch (err) {
    fail(platform, err);
  } finally {
    draft.busy = false;
  }
}
</script>

<template>
  <section class="page">
    <div class="page-head">
      <h1>客户端更新</h1>
      <span class="desc">按平台发布，客户端用自己上报的 platform 取对应更新包</span>
    </div>
    <div class="grid">
      <div v-for="platform in platforms" :key="platform" class="panel">
        <div class="panel-head">
          <h2>{{ platform }}</h2>
          <span class="desc state" :title="stateLine(platform)">{{ stateLine(platform) }}</span>
        </div>
        <div class="panel-body">
          <div class="seg">
            <button type="button" :class="{ on: drafts[platform]!.mode === 'upload' }" @click="drafts[platform]!.mode = 'upload'">上传</button>
            <button type="button" :class="{ on: drafts[platform]!.mode === 'url' }" @click="drafts[platform]!.mode = 'url'">外部地址</button>
          </div>
          <div class="row2">
            <div class="field">
              <label :for="platform + '-version'">版本号</label>
              <input
                :id="platform + '-version'"
                v-model="drafts[platform]!.version"
                type="text"
                maxlength="64"
                placeholder="2026.11.25-12"
                autocomplete="off"
              />
            </div>
            <div v-if="drafts[platform]!.mode === 'upload'" class="field">
              <label :for="platform + '-file'">更新包</label>
              <input :id="platform + '-file'" type="file" accept=".zst,.tar.zst" @change="onFile(platform, $event)" />
            </div>
            <div v-else class="field">
              <label :for="platform + '-url'">下载地址</label>
              <input
                :id="platform + '-url'"
                v-model="drafts[platform]!.url"
                type="text"
                placeholder="https://…/mesh-client.tar.zst"
                autocomplete="off"
              />
            </div>
          </div>
          <div class="field">
            <label :for="platform + '-notes'">说明（可选，随更新下发给客户端）</label>
            <textarea :id="platform + '-notes'" v-model="drafts[platform]!.notes" class="notes" maxlength="2000" spellcheck="false" />
          </div>
          <div v-if="drafts[platform]!.hint" class="hint">{{ drafts[platform]!.hint }}</div>
          <div v-if="drafts[platform]!.error" class="err">{{ drafts[platform]!.error }}</div>
          <div class="actions">
            <button class="primary" type="button" :disabled="drafts[platform]!.busy" @click="save(platform)">
              {{ drafts[platform]!.busy ? (drafts[platform]!.mode === "upload" ? "上传中…" : "解析中…") : "保存" }}
            </button>
            <button v-if="releaseOf(platform)" class="danger" type="button" :disabled="drafts[platform]!.busy" @click="remove(platform)">
              清除该平台
            </button>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:16px;align-items:start}
.state{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:100%}
.field{margin-top:12px}
.row2 .field{margin-top:0}
@media (max-width:760px){.grid{grid-template-columns:minmax(0,1fr)}}
</style>