<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { api, ApiError, type DeviceDetail } from "../api";
import { validateToml } from "../format";
import { applyTemplate, remainingPlaceholder, resolveTemplate } from "../template";

const props = defineProps<{ device: DeviceDetail }>();
const emit = defineEmits<{
  close: [];
  saved: [device: DeviceDetail];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

const name = ref(props.device.name);
const toml = ref(props.device.configToml);
const error = ref("");
const saving = ref(false);
const resetting = ref(false);

/** Esc 只关这一层：捕获阶段截住，外层不会再跟着响应。 */
function onKey(event: KeyboardEvent) {
  if (event.key !== "Escape") return;
  event.stopPropagation();
  if (!saving.value) emit("close");
}

onMounted(() => window.addEventListener("keydown", onKey, true));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));

async function resetToTemplate() {
  if (resetting.value) return;
  error.value = "";
  resetting.value = true;
  try {
    const template = (await api.template()).template;
    // 模板里的占位符用这台设备当前的地址填掉：重置是「回到默认配置」，
    // 不该让用户自己再去找一遍地址。认不出的占位符保留原文，交给保存前的拦截。
    const keys = resolveTemplate(template);
    const values: Record<string, string> = {};
    for (const key of keys) {
      if (key === "IP" && props.device.ipv4) values[key] = props.device.ipv4;
    }
    toml.value = keys.every((key) => values[key]) ? applyTemplate(template, values) : template;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "读取默认模板失败";
  } finally {
    resetting.value = false;
  }
}

async function save() {
  if (saving.value) return;
  error.value = "";
  const trimmed = name.value.trim();
  if (!trimmed) {
    error.value = "请填写设备名称";
    return;
  }
  if (!toml.value.trim()) {
    error.value = "请填写配置内容";
    return;
  }
  // 控制面原样收下配置，所以只能在这里拦：留了占位符，设备会拿到一段坏配置。
  const left = remainingPlaceholder(toml.value);
  if (left) {
    error.value = "配置里还有占位符 " + left + "，保存前请换成具体值";
    return;
  }
  const warning = validateToml(toml.value);
  if (warning) {
    error.value = warning;
    return;
  }
  saving.value = true;
  try {
    const saved = await api.updateDevice(props.device.id, trimmed, toml.value);
    emit("saved", {
      ...props.device,
      name: trimmed,
      configToml: toml.value,
      configHash: saved.configHash,
      configState: saved.configHash === props.device.lastConfigHash ? "current" : "stale",
    });
    emit("toast", "配置已保存", "设备下一轮心跳会拉到新配置并重启 core", "ok");
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "保存失败";
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <div class="modal" @click.self="!saving && emit('close')">
    <div class="dialog wide" role="dialog" aria-modal="true">
      <h2 class="dialog-title">查看与编辑配置</h2>
      <div class="dialog-sub">{{ device.name }} · {{ device.deviceId || "未注册" }}</div>
      <div class="dialog-body">
        <label class="field-label" for="cfg-name">设备名称</label>
        <input id="cfg-name" v-model="name" type="text" maxlength="64" autocomplete="off" />
        <div class="cfg-head">
          <label class="field-label" for="cfg-toml">EasyTier 配置（TOML）</label>
          <button class="ghost" type="button" :disabled="resetting || saving" @click="resetToTemplate">
            {{ resetting ? "读取中…" : "重置为模板" }}
          </button>
        </div>
        <textarea id="cfg-toml" v-model="toml" class="toml-editor" spellcheck="false" />
        <p class="hint">「重置为模板」只替换编辑框内容，保存后才会下发给设备。</p>
        <p class="hint">
          这里编辑的是设备实际使用的裸配置：不能留 <code v-pre>{{ ... }}</code> 占位符。
          「重置为模板」会用这台设备当前的地址填掉占位符；认不出的占位符要自己换掉，否则保存会被拒绝。
          <template v-if="device.ipv4">这台设备现在是 <code>{{ device.ipv4 }}</code>。</template>
        </p>
        <div v-if="error" class="err">{{ error }}</div>
      </div>
      <div class="dialog-actions">
        <button type="button" :disabled="saving" @click="emit('close')">取消</button>
        <button class="primary" type="button" :disabled="saving" @click="save">
          {{ saving ? "保存中…" : "保存" }}
        </button>
      </div>
    </div>
  </div>
</template>