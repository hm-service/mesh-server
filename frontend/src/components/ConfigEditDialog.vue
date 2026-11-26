<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { api, ApiError, type DeviceDetail, type DeviceSummary } from "../api";
import { validateToml } from "../format";
import ConfigTemplateEditor from "./ConfigTemplateEditor.vue";

const props = defineProps<{ device: DeviceDetail; devices: DeviceSummary[] }>();
const emit = defineEmits<{
  close: [];
  saved: [device: DeviceDetail];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

const name = ref(props.device.name);
// 撞车提示只跟别的设备比：自己不算冲突。
const others = computed(() => props.devices.filter((item) => item.id !== props.device.id));
const error = ref("");
const saving = ref(false);
const resetting = ref(false);
const editor = ref<InstanceType<typeof ConfigTemplateEditor> | null>(null);

/** Esc 只关这一层：捕获阶段截住，外层不会再跟着响应。 */
function onKey(event: KeyboardEvent) {
  if (event.key !== "Escape") return;
  event.stopPropagation();
  if (!saving.value) emit("close");
}

onMounted(() => window.addEventListener("keydown", onKey, true));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));

async function resetToTemplate() {
  if (resetting.value || !editor.value) return;
  error.value = "";
  resetting.value = true;
  try {
    // 只换文本：编辑器会按同一套规则把模板里的占位符配上这台设备当前的地址，
    // 「配置」一栏也跟着回到当前地址，不会停在上一版填的值上。
    editor.value.setText((await api.template()).template);
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
  if (!editor.value) return;
  if (!trimmed) {
    error.value = "请填写设备名称";
    return;
  }
  // 控制面原样收下配置，所以只能在这里拦：留了占位符，设备会拿到一段坏配置。
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
  saving.value = true;
  try {
    const saved = await api.updateDevice(props.device.id, trimmed, built.config);
    emit("saved", {
      ...props.device,
      name: trimmed,
      configToml: built.config,
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
    <div class="dialog wide scrolling" role="dialog" aria-modal="true">
      <h2 class="dialog-title">查看与编辑配置</h2>
      <div class="dialog-sub">{{ device.name }} · {{ device.deviceId || "未注册" }}</div>
      <div class="dialog-body">
        <div class="field">
          <label class="field-label" for="cfg-name">设备名称</label>
          <input
            id="cfg-name"
            v-model="name"
            type="text"
            maxlength="64"
            placeholder="例如：朋友的笔记本"
            autocomplete="off"
          />
        </div>

        <ConfigTemplateEditor
          ref="editor"
          mode="config"
          :initial-text="device.configToml"
          :suggestion="device.ipv4"
          :devices="others"
          :disabled="saving"
        >
          <template #actions>
            <button class="ghost" type="button" :disabled="resetting || saving" @click="resetToTemplate">
              {{ resetting ? "读取中…" : "重置为模板" }}
            </button>
          </template>
        </ConfigTemplateEditor>

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
