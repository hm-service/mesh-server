<script setup lang="ts">
import { computed, ref } from "vue";
import {
  applyTemplate,
  FIELD_KEYS,
  fieldWarning,
  fieldsFor,
  maskField,
  readField,
  resolveTemplate,
  type TemplateField,
} from "../template";

// 「配置 | 配置文件」两栏、字段行、模板与表单值的同步、提交前的占位符拦截，新建与编辑
// 共用这一份：两个对话框的差别只剩 mode（文本里有没有占位符）和外面的提交逻辑。

const props = defineProps<{
  /** initialText 是创建时的内容；之后换内容（「重置为模板」）走下面 expose 的 setText。 */
  initialText: string;
  /** template：text 是带占位符的模板原文；config：text 是裸配置，先反解出字段值。 */
  mode: "template" | "config";
  /** suggestion 是 IP 的预填值：新建用算出来的建议地址，编辑用设备当前地址。 */
  suggestion: string;
  /** devices 只用于地址撞车提示：调用方负责把设备自己排除掉。 */
  devices: { name: string; ipv4: string }[];
  disabled?: boolean;
}>();

type Tab = "form" | "file";

const template = ref("");
const values = ref<Record<string, string>>({});
const tab = ref<Tab>("form");

// 表单按模板现扫：模板里没有的占位符不出现在「配置」里。
const fields = computed<TemplateField[]>(() => fieldsFor(template.value, props.suggestion));

// 撞车只提示、不阻断：同一个地址有时是有意为之（多网卡、迁移期并存、临时排查）。
const warnings = computed<Record<string, string>>(() => {
  const out: Record<string, string> = {};
  for (const field of fields.value) {
    const warning = fieldWarning(field.key, values.value[field.key] ?? "", props.devices);
    if (warning) out[field.key] = warning;
  }
  return out;
});

// 「配置文件」里显示什么：模板原文原样显示；裸配置把表单值填进去。有占位符还没值时
// 填不出来，就退回模板原文让占位符露出来——保存前的拦截会把 tab 指到这里。
const file = computed<string>({
  get() {
    if (props.mode === "template") return template.value;
    const filled: Record<string, string> = {};
    for (const [key, value] of Object.entries(values.value)) {
      if (value.trim()) filled[key] = value;
    }
    try {
      return applyTemplate(template.value, filled);
    } catch {
      return template.value;
    }
  },
  // 直接在「配置文件」里改：模板原文收下即可；裸配置要先反解出字段值再换回占位符。
  set(text: string) {
    absorb(text);
  },
});

// syncValues 按模板重新梳理 values：已有的 key 保留用户填的值，新出现的 key 用
// 调用方给的预填值（没有就留空），模板里删掉的 key 直接丢掉。
function syncValues() {
  const next: Record<string, string> = {};
  for (const field of fields.value) {
    next[field.key] = values.value[field.key] ?? field.value;
  }
  values.value = next;
}

// absorb 换一份内容：模板原文直接收下；裸配置先把认识的字段读回 values、把字面量换成
// 占位符，之后两个 tab 走的是同一套（values 只由表单和这段文本改）。
function absorb(text: string) {
  let masked = text;
  const found: Record<string, string> = {};
  if (props.mode === "config") {
    for (const key of FIELD_KEYS) {
      const value = readField(masked, key);
      if (!value) continue;
      found[key] = value;
      masked = maskField(masked, key);
    }
  }
  template.value = masked;
  // 只留这段文本里读得出来的值：重置为模板时不能把上一版填的地址留在表单里。
  values.value = found;
  syncValues();
}

absorb(props.initialText);

/** placeholderOf 把 key 还原成模板里的写法，用于界面提示。 */
function placeholderOf(key: string): string {
  return "{{ " + key + " }}";
}

// buildConfig 把表单值填进模板，得到要提交的裸配置；填不完整时把 tab 指到「配置文件」
// ——那里能直接看到还留着的是哪个占位符——并返回要显示的错误。
function buildConfig(): { config: string } | { error: string } {
  if (!template.value.trim()) return { error: "请填写配置内容" };
  const missing = resolveTemplate(template.value).find((key) => !(values.value[key] ?? "").trim());
  if (missing !== undefined) {
    tab.value = "file";
    return {
      error:
        "占位符 " + placeholderOf(missing) + " 还没有值：请在「配置」里填好，或者直接在「配置文件」里把它换成具体值",
    };
  }
  try {
    return { config: applyTemplate(template.value, values.value) };
  } catch (err) {
    tab.value = "file";
    return { error: err instanceof Error ? err.message : "配置模板里有没填完的占位符" };
  }
}

/** setText 换一份内容：打开时用 initialText，之后「重置为模板」用这个。 */
function setText(text: string) {
  absorb(text);
  tab.value = "form";
}

defineExpose({ buildConfig, setText });
</script>

<template>
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
    <div class="tab-actions"><slot name="actions" /></div>
  </div>

  <template v-if="tab === 'form'">
    <div v-for="field in fields" :key="field.key" class="field">
      <label class="field-label" :for="'config-field-' + field.key">{{ field.label }}</label>
      <input
        :id="'config-field-' + field.key"
        v-model="values[field.key]"
        type="text"
        :placeholder="field.example"
        autocomplete="off"
        :disabled="disabled"
      />
      <p class="field-hint">
        <template v-if="mode === 'template'">
          模板里写作 <code>{{ placeholderOf(field.key) }}</code>，会原样换成这一栏的值（例如
          <code>{{ field.example }}</code>）。
        </template>
        <template v-else>
          「配置文件」里对应的那一行会跟着换成这一栏的值（例如 <code>{{ field.example }}</code>）。
        </template>
      </p>
      <p v-if="warnings[field.key]" class="field-warn">{{ warnings[field.key] }}（仍可提交）</p>
    </div>
    <p v-if="!fields.length" class="hint">
      <template v-if="mode === 'template'">这份模板没有可填的占位符，直接提交即可。</template>
      <template v-else>这份配置里没有能识别的字段，请到「配置文件」里改。</template>
    </p>
    <p class="hint">地址按全网约定的 /24 写。同一个地址允许多台设备共用（迁移期新旧并存时就是这样），但一般应当各自不同。</p>
  </template>

  <template v-else>
    <textarea
      id="config-file"
      v-model="file"
      class="config-toml"
      :disabled="disabled"
      spellcheck="false"
    />
    <p class="hint">
      <template v-if="mode === 'template'">
        这是模板原文：占位符（形如 <code v-pre>{{ IP }}</code>）原样保留，提交前必须全部填完——在「配置」里填，或直接在这里改成具体值。
      </template>
      <template v-else>
        这是设备实际拿到的裸配置，表单里的值已经填好；也可以直接改这里，会重新解析回表单。
      </template>
    </p>
  </template>
</template>
