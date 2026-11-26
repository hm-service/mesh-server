<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, ApiError } from "../api";
import { copyText } from "../clipboard";
import { validateToml } from "../format";
import { nextFreeAddress } from "../template";
import ConfigTemplateEditor from "./ConfigTemplateEditor.vue";

// devices 是控制台已经在手上的设备列表：建议地址与撞车提示都从它算，不再另开接口。
const props = defineProps<{ devices: { name: string; ipv4: string }[] }>();

const emit = defineEmits<{
  close: [];
  created: [name: string];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

const name = ref("");
// template 是服务端给的默认模板原文（带占位符），只交给编辑器做初值；
// 之后模板与表单值都由 ConfigTemplateEditor 管。
const template = ref("");
// suggestion 是加载时算出的建议地址，之后不再跟着轮询变。
const suggestion = ref("");
const error = ref("");
const token = ref("");
const address = ref("");
const busy = ref(false);
const locked = ref(false);
const templateReady = ref(false);
const templateError = ref("");
const editor = ref<InstanceType<typeof ConfigTemplateEditor> | null>(null);

// 默认模板只有服务端一份：取不到就不让建，否则会写出一份和全网不一致的配置。
async function loadTemplate() {
  templateError.value = "";
  try {
    const payload = await api.template();
    suggestion.value = nextFreeAddress(props.devices);
    template.value = payload.template;
    templateReady.value = true;
  } catch (err) {
    templateReady.value = false;
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    templateError.value = err instanceof ApiError ? err.message : "读取默认模板失败，请重试";
  }
}

onMounted(loadTemplate);

async function submit() {
  if (busy.value || locked.value) return;
  error.value = "";
  const trimmed = name.value.trim();
  if (!templateReady.value || !editor.value) {
    error.value = "还没有读到默认模板，请重试";
    return;
  }
  if (!trimmed) {
    error.value = "请填写设备名称";
    return;
  }
  const built = editor.value.buildConfig();
  if ("error" in built) {
    error.value = built.error;
    return;
  }
  const warning = validateToml(built.config);
  if (warning) {
    error.value = warning;
    return;
  }
  busy.value = true;
  try {
    const created = await api.createDevice(trimmed, built.config);
    token.value = created.token;
    address.value = created.ipv4 ?? "";
    locked.value = true;
    emit("toast", "设备已创建", trimmed, "ok");
    emit("created", trimmed);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "创建失败";
  } finally {
    busy.value = false;
  }
}

async function copy() {
  emit("toast", (await copyText(token.value)) ? "已复制" : "复制失败", "", "ok");
}
</script>

<template>
  <div class="modal" @click.self="emit('close')">
    <div class="dialog wide scrolling" role="dialog" aria-modal="true">
      <h2 class="dialog-title">新建设备</h2>
      <p class="dialog-sub">配置由默认模板预填。生成一次性令牌，设备注册后自动绑定，之后从心跳取配置。</p>
      <div class="dialog-body">
        <div class="field">
          <label class="field-label" for="new-device-name">设备名称</label>
          <input
            id="new-device-name"
            v-model="name"
            type="text"
            maxlength="64"
            placeholder="例如：朋友的笔记本"
            autocomplete="off"
            :disabled="locked"
          />
        </div>

        <ConfigTemplateEditor
          v-if="templateReady"
          ref="editor"
          mode="template"
          :initial-text="template"
          :suggestion="suggestion"
          :devices="devices"
          :disabled="locked"
        />

        <div v-if="templateError" class="err">
          {{ templateError }}
          <button class="ghost" type="button" @click="loadTemplate">重试</button>
        </div>
        <div v-if="token" class="token-box">
          <div class="token-label">
            一次性令牌（10 分钟内有效，注册成功后作废）
            <template v-if="address"> · 地址 <code>{{ address }}</code></template>
          </div>
          <div class="token-row">
            <code class="token-value">{{ token }}</code>
            <button type="button" @click="copy">复制</button>
          </div>
        </div>
        <div v-if="error" class="err">{{ error }}</div>
      </div>
      <div class="dialog-actions">
        <button type="button" @click="emit('close')">{{ locked ? "关闭" : "取消" }}</button>
        <button v-if="!locked" class="primary" type="button" :disabled="busy || !templateReady" @click="submit">
          {{ busy ? "生成中…" : "生成令牌" }}
        </button>
      </div>
    </div>
  </div>
</template>
