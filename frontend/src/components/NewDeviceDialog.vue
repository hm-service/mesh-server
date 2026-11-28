<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, ApiError } from "../api";
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
// 公钥是槽位的身份：设备侧在设置页显示，用户抄过来。它不是秘密，但抄错一个字符
// 就等于建了一个永远等不到认领的槽位，所以形状在提交前先判一遍。
const pubkey = ref("");
// template 是服务端给的默认模板原文（带占位符），只交给编辑器做初值；
// 之后模板与表单值都由 ConfigTemplateEditor 管。
const template = ref("");
// suggestion 是加载时算出的建议地址，之后不再跟着轮询变。
const suggestion = ref("");
const error = ref("");
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
  const key = pubkey.value.trim();
  if (!isDeviceKey(key)) {
    error.value = "公钥形状不对：应为客户端设置页上那串 44 字符的 base64";
    return;
  }
  busy.value = true;
  try {
    const created = await api.createDevice(trimmed, built.config, key);
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

/** 32 字节 Ed25519 公钥的标准 base64：44 个字符、带一个 '=' 填充。 */
function isDeviceKey(raw: string): boolean {
  return /^[A-Za-z0-9+/]{43}=$/.test(raw);
}
</script>

<template>
  <div class="modal" @click.self="emit('close')">
    <div class="dialog wide scrolling" role="dialog" aria-modal="true">
      <h2 class="dialog-title">新建设备</h2>
      <p class="dialog-sub">配置由默认模板预填。槽位绑定客户端的设备公钥，它第一次认领之后从心跳取配置。</p>
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
          <label class="field-label" for="new-device-key">设备公钥</label>
          <input
            id="new-device-key"
            v-model="pubkey"
            type="text"
            placeholder="客户端设置页「本机公钥」，44 字符 base64"
            autocomplete="off"
            spellcheck="false"
            :disabled="locked"
          />
          <span class="hint">槽位认的就是这把钥匙：谁持有对应私钥，谁就来认领这个槽位。</span>
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
        <div v-if="locked" class="key-box">
          <div class="key-label">
            已创建<template v-if="address">，地址 <code>{{ address }}</code></template>：到那台设备的设置页点「保存并连接」，它用这把钥匙认领之后就开始工作。
          </div>
        </div>
        <div v-if="error" class="err">{{ error }}</div>
      </div>
      <div class="dialog-actions">
        <button type="button" @click="emit('close')">{{ locked ? "关闭" : "取消" }}</button>
        <button v-if="!locked" class="primary" type="button" :disabled="busy || !templateReady" @click="submit">
          {{ busy ? "创建中…" : "创建设备" }}
        </button>
      </div>
    </div>
  </div>
</template>
