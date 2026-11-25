<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, ApiError, type ReleasesPayload } from "../api";

const emit = defineEmits<{
  saved: [releases: ReleasesPayload];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

const toml = ref("");
const error = ref("");
const busy = ref(false);

// 编辑器显示的是当前生效的模板：DATA_DIR/peer.default.toml，缺失或空白时是内置
// 模板（GET /api/admin/template 的语义）。只看发布记录里的 defaultToml 会在文件不存在
// 时显示成空，和「重置为模板」实际拿到的内容对不上。
onMounted(async () => {
  try {
    toml.value = (await api.template()).toml;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "读取默认模板失败";
  }
});

async function save() {
  if (busy.value) return;
  error.value = "";
  if (!toml.value.trim()) {
    error.value = "默认模板不能为空；留空保存不会改动现有模板。";
    return;
  }
  busy.value = true;
  try {
    const saved = await api.publishRelease({ defaultToml: toml.value });
    emit("saved", saved);
    emit("toast", "默认模板已保存", "之后新建设备与「重置为模板」都用它", "ok");
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
  <section class="page template-layout">
    <div class="page-head">
      <div>
        <h1 class="page-title">默认模板</h1>
        <p class="page-sub">新建设备的初始配置；设备详情里的「重置为模板」也读这一份。</p>
      </div>
    </div>

    <div class="panel">
      <div class="panel-head">
        <span class="panel-title">EasyTier 配置</span>
        <span class="panel-sub">TOML · DATA_DIR/peer.default.toml，缺失或空白时回退内置模板</span>
      </div>
      <div class="panel-body">
        <textarea v-model="toml" class="template-editor" spellcheck="false" />
        <p class="hint">只影响之后的新建设备与显式重置；已建设备的配置在设备详情里单独修改。</p>
        <div v-if="error" class="err">{{ error }}</div>
        <div class="actions">
          <button class="primary" type="button" :disabled="busy" @click="save">{{ busy ? "保存中…" : "保存" }}</button>
        </div>
      </div>
    </div>
  </section>
</template>