<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, ApiError } from "../api";
import { copyText } from "../clipboard";
import { validateToml } from "../format";

const emit = defineEmits<{
  close: [];
  created: [name: string];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

const name = ref("");
const toml = ref("");
const error = ref("");
const token = ref("");
const address = ref("");
const busy = ref(false);
const locked = ref(false);
const templateReady = ref(false);
const templateError = ref("");

// 默认模板只有服务端一份：取不到就不让建，否则会写出一份和全网不一致的配置。
async function loadTemplate() {
  templateError.value = "";
  try {
    toml.value = (await api.template()).toml;
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
  if (!templateReady.value) {
    error.value = "还没有读到默认模板，请重试";
    return;
  }
  if (!trimmed) {
    error.value = "请填写设备名称";
    return;
  }
  if (!toml.value.trim()) {
    error.value = "请填写配置内容";
    return;
  }
  const warning = validateToml(toml.value);
  if (warning) {
    error.value = warning;
    return;
  }
  busy.value = true;
  try {
    const created = await api.createDevice(trimmed, toml.value);
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
    <div class="dialog wide" role="dialog" aria-modal="true">
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
        <div class="field">
          <label class="field-label" for="new-device-toml">EasyTier 配置（TOML）</label>
          <textarea id="new-device-toml" v-model="toml" class="new-toml" :disabled="locked" spellcheck="false" />
        </div>
        <p class="hint">
          名称只用于控制台展示，可重复；同一网络内每台设备的 ipv4 必须唯一，不要填网段地址（末尾 .0）。
        </p>
        <p class="hint">
          配置里的 <code v-pre>{{ IP }}</code> 由控制面分配：取现有设备里最小那台所在的 /24，
          从它之后挑第一个没被占用的值。要自己指定地址就直接改写 ipv4 那一行。
        </p>
        <div v-if="templateError" class="err">
          {{ templateError }}
          <button class="ghost" type="button" @click="loadTemplate">重试</button>
        </div>
        <div v-if="token" class="token-box">
          <div class="token-label">
            一次性令牌（10 分钟内有效，注册成功后作废）
            <template v-if="address"> · 已分配 <code>{{ address }}</code></template>
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
