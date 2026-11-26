<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { api, ApiError } from "../api";
import { copyText } from "../clipboard";
import { validateToml } from "../format";
import { applyTemplate, fieldWarning, fieldsFor, nextFreeAddress, resolveTemplate, type TemplateField } from "../template";

// devices 是控制台已经在手上的设备列表：建议地址与撞车提示都从它算，不再另开接口。
const props = defineProps<{ devices: { name: string; ipv4: string }[] }>();

const emit = defineEmits<{
  close: [];
  created: [name: string];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

type Tab = "form" | "file";

const name = ref("");
// 状态只有两份：template 是唯一源，带着占位符；values 只放表单值。
const template = ref("");
// suggestion 是加载时算出的建议地址，之后不再跟着轮询变。
const suggestion = ref("");
const values = ref<Record<string, string>>({});
const tab = ref<Tab>("form");
const error = ref("");
const token = ref("");
const address = ref("");
const busy = ref(false);
const locked = ref(false);
const templateReady = ref(false);
const templateError = ref("");

// 表单按模板现扫：模板里没有的占位符不出现在「配置」里。
const fields = computed<TemplateField[]>(() => fieldsFor(template.value, suggestion.value));

// 撞车只提示、不阻断：同一个地址有时是有意为之（多网卡、迁移期并存、临时排查）。
const warnings = computed<Record<string, string>>(() => {
  const out: Record<string, string> = {};
  for (const field of fields.value) {
    const warning = fieldWarning(field.key, values.value[field.key] ?? "", props.devices);
    if (warning) out[field.key] = warning;
  }
  return out;
});

// syncValues 按模板重新梳理 values：已有的 key 保留用户填的值，新出现的 key 用
// 服务端给的默认值（没有就留空），模板里删掉的 key 直接丢掉。
function syncValues() {
  const next: Record<string, string> = {};
  for (const field of fields.value) {
    next[field.key] = values.value[field.key] ?? field.value;
  }
  values.value = next;
}

// 默认模板只有服务端一份：取不到就不让建，否则会写出一份和全网不一致的配置。
async function loadTemplate() {
  templateError.value = "";
  try {
    const payload = await api.template();
    template.value = payload.template;
    suggestion.value = nextFreeAddress(props.devices);
    syncValues();
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

// 用户在「配置文件」里改了模板：重新扫一遍占位符，表单跟着变，已填的值按 key 保住。
function onTemplateInput() {
  syncValues();
}

/** placeholderOf 把 key 还原成模板里的写法，用于界面提示。 */
function placeholderOf(key: string): string {
  return "{{ " + key + " }}";
}

// block 把问题指到「配置文件」：那里能直接看到还留着的是哪个占位符。
function block(message: string): null {
  error.value = message;
  tab.value = "file";
  return null;
}

// buildConfig 把表单值填进模板，得到要提交的裸配置。填不完整返回 null。
function buildConfig(): string | null {
  const missing = resolveTemplate(template.value).find((key) => !(values.value[key] ?? "").trim());
  if (missing !== undefined) {
    return block("占位符 " + placeholderOf(missing) + " 还没有值：请在「配置」里填好，或者直接在「配置文件」里把它换成具体值");
  }
  try {
    return applyTemplate(template.value, values.value);
  } catch (err) {
    return block(err instanceof Error ? err.message : "配置模板里有没填完的占位符");
  }
}

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
  if (!template.value.trim()) {
    error.value = "请填写配置内容";
    return;
  }
  const body = buildConfig();
  if (body === null) return;
  const warning = validateToml(body);
  if (warning) {
    error.value = warning;
    return;
  }
  busy.value = true;
  try {
    const created = await api.createDevice(trimmed, body);
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

        <div class="tab-bar" role="tablist">
          <button
            class="tab"
            :class="{ on: tab === 'form' }"
            type="button"
            role="tab"
            :aria-selected="tab === 'form'"
            @click="tab = 'form'"
          >
            配置
          </button>
          <button
            class="tab"
            :class="{ on: tab === 'file' }"
            type="button"
            role="tab"
            :aria-selected="tab === 'file'"
            @click="tab = 'file'"
          >
            配置文件
          </button>
        </div>

        <template v-if="tab === 'form'">
          <div v-for="field in fields" :key="field.key" class="field">
            <label class="field-label" :for="'new-device-' + field.key">{{ field.label }}</label>
            <input
              :id="'new-device-' + field.key"
              v-model="values[field.key]"
              type="text"
              :placeholder="field.example"
              autocomplete="off"
              :disabled="locked"
            />
            <p class="field-hint">
              模板里写作 <code>{{ placeholderOf(field.key) }}</code>，会原样换成这一栏的值（例如
              <code>{{ field.example }}</code>）。
            </p>
            <p v-if="warnings[field.key]" class="field-warn">{{ warnings[field.key] }}（仍可提交）</p>
          </div>
          <p v-if="!fields.length" class="hint">这份模板没有可填的占位符，直接提交即可。</p>
          <p class="hint">地址按全网约定的 /24 写。同一个地址允许多台设备共用（迁移期新旧并存时就是这样），但一般应当各自不同。</p>
        </template>

        <template v-else>
          <textarea
            id="new-device-toml"
            v-model="template"
            class="new-toml"
            :disabled="locked"
            spellcheck="false"
            @input="onTemplateInput"
          />
          <p class="hint">
            这是模板原文：占位符（形如 <code v-pre>{{ IP }}</code>）原样保留，提交前必须全部填完——在「配置」里填，或直接在这里改成具体值。
          </p>
        </template>

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
