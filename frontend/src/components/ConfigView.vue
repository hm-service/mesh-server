<script setup lang="ts">
import { ref, watch } from "vue";
import { api, ApiError, type ReleasesPayload } from "../api";

const props = defineProps<{ releases: ReleasesPayload | null }>();
const emit = defineEmits<{
  saved: [releases: ReleasesPayload];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

const toml = ref("");
const error = ref("");
const busy = ref(false);

watch(
  () => props.releases,
  (value) => {
    if (value) toml.value = value.default_toml;
  },
  { immediate: true },
);

async function save() {
  if (busy.value) return;
  error.value = "";
  busy.value = true;
  try {
    const saved = await api.publishRelease({ default_toml: toml.value });
    emit("saved", saved);
    emit("toast", "默认配置已保存", "", "ok");
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "保存失败";
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="page">
    <div class="page-head">
      <h1>配置</h1>
      <span class="desc">新建设备对话框的初始内容</span>
    </div>
    <div class="panel narrow">
      <div class="panel-head">
        <h2>默认 EasyTier 配置</h2>
        <span class="desc">TOML</span>
      </div>
      <div class="panel-body">
        <textarea v-model="toml" class="big" spellcheck="false" />
        <p class="hint">只影响之后新建的设备；已建设备的配置在设备详情里单独修改。</p>
        <div v-if="error" class="err">{{ error }}</div>
        <div class="actions">
          <button class="primary" type="button" :disabled="busy" @click="save">{{ busy ? "保存中…" : "保存" }}</button>
        </div>
      </div>
    </div>
  </section>
</template>
