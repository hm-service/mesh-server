<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { api, ApiError, type DeviceDetail } from "../api";
import { copyText } from "../clipboard";
import { nowSec } from "../clock";
import {
  absTime,
  appStateClass,
  appStateText,
  configStateClass,
  configStateText,
  relTime,
  shortHash,
  statusText,
  validateToml,
} from "../format";

const props = defineProps<{ id: string }>();
const emit = defineEmits<{
  close: [];
  changed: [];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

type Tab = "edit" | "detail";

const device = ref<DeviceDetail | null>(null);
const name = ref("");
const toml = ref("");
const tab = ref<Tab>("edit");
const error = ref("");
const token = ref("");
const loading = ref(true);
const saving = ref(false);

const configHash = computed(() => device.value?.config_hash ?? "—");
const configState = computed(() => device.value?.config_state ?? "stale");
const lastConfigHash = computed(() => device.value?.last_config_hash ?? "");

onMounted(async () => {
  try {
    const detail = await api.getDevice(props.id);
    device.value = detail;
    name.value = detail.name;
    toml.value = detail.config_toml ?? "";
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "加载失败";
  } finally {
    loading.value = false;
  }
});

function showError(text: string) {
  error.value = text;
}

async function save() {
  const current = device.value;
  if (!current || saving.value) return;
  error.value = "";
  const trimmed = name.value.trim();
  if (!trimmed) {
    showError("请填写设备名称");
    return;
  }
  if (!toml.value.trim()) {
    showError("请填写配置内容");
    return;
  }
  const warning = validateToml(toml.value);
  if (warning) {
    showError(warning);
    return;
  }
  saving.value = true;
  try {
    const saved = await api.updateDevice(current.id, trimmed, toml.value);
    device.value = { ...current, name: trimmed, config_toml: toml.value, config_hash: saved.config_hash, config_state: saved.config_hash === current.last_config_hash ? "current" : "stale" };
    emit("toast", "配置已保存", "hash " + shortHash(saved.config_hash), "ok");
    emit("changed");
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    showError(err instanceof Error ? err.message : "保存失败");
  } finally {
    saving.value = false;
  }
}

async function issueToken() {
  const current = device.value;
  if (!current) return;
  error.value = "";
  try {
    const issued = await api.issueToken(current.id);
    token.value = issued.token;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    showError(err instanceof Error ? err.message : "生成令牌失败");
  }
}

async function remove() {
  const current = device.value;
  if (!current) return;
  if (!window.confirm("撤销设备「" + current.name + "」？设备之后的签名请求会被拒绝。")) return;
  try {
    await api.deleteDevice(current.id);
    emit("toast", "已撤销", current.name, "ok");
    emit("changed");
    emit("close");
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    showError(err instanceof Error ? err.message : "撤销失败");
  }
}

async function copy() {
  emit("toast", (await copyText(token.value)) ? "已复制" : "复制失败", "", "ok");
}
</script>

<template>
  <div class="modal" @click.self="emit('close')">
    <div class="dialog wide">
      <h2>{{ device?.name ?? "设备详情" }}</h2>
      <div class="sub">设备详情 · 左侧编辑配置，右侧查看设备信息</div>
      <div v-if="loading" class="hint">加载中…</div>
      <template v-else-if="device">
        <div class="cfg-tabs">
          <button type="button" class="cfg-tab" :class="{ on: tab === 'edit' }" @click="tab = 'edit'">配置</button>
          <button type="button" class="cfg-tab" :class="{ on: tab === 'detail' }" @click="tab = 'detail'">详情</button>
        </div>
        <div class="cfg-body" :class="tab === 'edit' ? 'show-edit' : 'show-detail'">
          <div class="cfg-pane cfg-pane-edit">
            <div class="cfg-pane-label">EasyTier 配置（TOML）</div>
            <textarea v-model="toml" spellcheck="false" />
          </div>
          <div class="cfg-pane cfg-pane-detail">
            <div class="cfg-pane-label">设备详情</div>
            <div class="detail-box">
              <label>设备名称</label>
              <input v-model="name" type="text" maxlength="64" />
              <div class="kv">
                <div class="k">内部 ID</div>
                <div class="v">{{ device.id }}</div>
                <div class="k">设备 ID</div>
                <div class="v">{{ device.device_id || "未注册" }}</div>
                <div class="k">主机名</div>
                <div class="v">{{ device.hostname || "—" }}</div>
                <div class="k">状态</div>
                <div class="v">
                  {{ statusText(device.status) }}
                  <template v-if="device.last_seen_at"> · {{ relTime(device.last_seen_at, nowSec) }}</template>
                </div>
                <div class="k">配置 hash</div>
                <div class="v">{{ configHash }}</div>
                <div class="k">配置状态</div>
                <div class="v"><span :class="configStateClass(configState)">{{ configStateText(configState) }}</span></div>
                <div class="k">上报配置</div>
                <div class="v">{{ lastConfigHash || "—" }}</div>
                <div class="k">上报客户端</div>
                <div class="v">{{ device.last_app_hash || "—" }}</div>
                <div class="k">客户端状态</div>
                <div class="v"><span :class="appStateClass(device)">{{ appStateText(device) }}</span></div>
                <div class="k">公钥</div>
                <div class="v">{{ device.pubkey || "—" }}</div>
                <div class="k">最近 IP</div>
                <div class="v">{{ device.last_ip || "—" }}</div>
                <div class="k">创建时间</div>
                <div class="v">{{ absTime(device.created_at) }}</div>
                <div class="k">最后心跳</div>
                <div class="v">{{ device.last_seen_at ? absTime(device.last_seen_at) : "从未" }}</div>
              </div>
            </div>
          </div>
        </div>
        <div class="actions">
          <button class="primary" type="button" :disabled="saving" @click="save">保存配置</button>
          <button type="button" @click="issueToken">重新生成令牌</button>
          <button class="danger" type="button" @click="remove">撤销设备</button>
          <button type="button" @click="emit('close')">关闭</button>
        </div>
        <div v-if="token" class="token">
          <div class="label">一次性令牌（10 分钟内有效，注册成功后作废）</div>
          <div class="row2c">
            <code>{{ token }}</code>
            <button type="button" @click="copy">复制</button>
          </div>
        </div>
      </template>
      <div v-if="error" class="err">{{ error }}</div>
    </div>
  </div>
</template>
