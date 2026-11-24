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

type Tab = "edit" | "detail";

const name = ref("");
const toml = ref("");
const tab = ref<Tab>("edit");
const error = ref("");
const token = ref("");
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

function showError(text: string, target: Tab) {
  tab.value = target;
  error.value = text;
}

async function submit() {
  if (busy.value || locked.value) return;
  error.value = "";
  const trimmed = name.value.trim();
  if (!templateReady.value) {
    showError("还没有读到默认模板，请重试", "edit");
    return;
  }
  if (!trimmed) {
    showError("请填写设备名称", "detail");
    return;
  }
  if (!toml.value.trim()) {
    showError("请填写配置内容", "edit");
    return;
  }
  const warning = validateToml(toml.value);
  if (warning) {
    showError(warning, "edit");
    return;
  }
  busy.value = true;
  try {
    const created = await api.createDevice(trimmed, toml.value);
    token.value = created.token;
    locked.value = true;
    tab.value = "detail";
    emit("toast", "设备已创建", trimmed, "ok");
    emit("created", trimmed);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    showError(err instanceof Error ? err.message : "创建失败", "edit");
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
    <div class="dialog wide">
      <h2>新建设备</h2>
      <div class="sub">配置由控制台的默认模板预填，保存后生成一次性令牌；设备注册后自动绑定。</div>
      <div class="cfg-tabs">
        <button type="button" class="cfg-tab" :class="{ on: tab === 'edit' }" @click="tab = 'edit'">配置</button>
        <button type="button" class="cfg-tab" :class="{ on: tab === 'detail' }" @click="tab = 'detail'">信息</button>
      </div>
      <div class="cfg-body" :class="tab === 'edit' ? 'show-edit' : 'show-detail'">
        <div class="cfg-pane cfg-pane-edit">
          <div class="cfg-pane-label">EasyTier 配置（TOML）</div>
          <textarea v-model="toml" :disabled="locked" spellcheck="false" />
          <div v-if="templateError" class="err">
            {{ templateError }}
            <button type="button" @click="loadTemplate">重试</button>
          </div>
        </div>
        <div class="cfg-pane cfg-pane-detail">
          <div class="cfg-pane-label">设备信息</div>
          <div class="detail-box">
            <label>设备名称</label>
            <input v-model="name" type="text" maxlength="64" placeholder="例如：朋友的笔记本" :disabled="locked" autocomplete="off" />
            <div class="kv">
              <div class="k">设备 ID</div>
              <div class="v">注册时生成</div>
              <div class="k">令牌有效期</div>
              <div class="v">10 分钟，一次性</div>
            </div>
            <p class="hint">
              名称只用于控制台展示，可重复；同一网络内每台设备的 ipv4 必须唯一，不要填网段地址（末尾 .0）。
            </p>
            <div v-if="token" class="token">
              <div class="label">一次性令牌（10 分钟内有效，注册成功后作废）</div>
              <div class="row2c">
                <code>{{ token }}</code>
                <button type="button" @click="copy">复制</button>
              </div>
            </div>
          </div>
        </div>
      </div>
      <div v-if="error" class="err">{{ error }}</div>
      <div class="actions">
        <button class="primary" type="button" :disabled="busy || locked || !templateReady" @click="submit">
          {{ busy ? "生成中…" : locked ? "令牌已生成" : "生成令牌" }}
        </button>
        <button type="button" @click="emit('close')">{{ locked ? "关闭" : "取消" }}</button>
      </div>
    </div>
  </div>
</template>
