<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { api, ApiError, type DeviceDetail, type GroupView } from "../api";
import { copyText } from "../clipboard";
import { nowSec } from "../clock";
import {
  absTime,
  appStateClass,
  appStateLabel,
  configStateClass,
  configStateText,
  fixedAddress,
  identityText,
  relTime,
  statusDotClass,
  statusText,
  validateToml,
} from "../format";
import ConfirmDialog from "./ConfirmDialog.vue";

const props = defineProps<{ id: string; groups: GroupView[] }>();
const emit = defineEmits<{
  close: [];
  changed: [];
  toast: [title: string, message: string, kind: "ok" | "err"];
  unauthorized: [];
}>();

/** 与控制面 app.CredentialNoneCooldown 一致：冷却内重复签发只会重新计时。 */
const ROTATE_COOLDOWN_SECONDS = 300;

type ConfirmKind = "rotate" | "delete" | null;

const device = ref<DeviceDetail | null>(null);
const loading = ref(true);
const error = ref("");
const mode = ref<"view" | "edit">("view");
const name = ref("");
const toml = ref("");
const saving = ref(false);
const resetting = ref(false);
const token = ref("");
const tokenBusy = ref(false);
const confirm = ref<ConfirmKind>(null);
const rotating = ref(false);
const deleting = ref(false);
const actionError = ref("");

const groupName = computed(() => {
  const group = props.groups.find((item) => item.id === device.value?.groupId);
  return group?.name ?? "未分组";
});

const cooling = computed(() => {
  const credential = device.value?.credential;
  if (!credential) return false;
  return nowSec.value - credential.issuedAt < ROTATE_COOLDOWN_SECONDS;
});

const coolingMinutes = computed(() => {
  const credential = device.value?.credential;
  if (!credential) return 0;
  return Math.max(1, Math.ceil((credential.issuedAt + ROTATE_COOLDOWN_SECONDS - nowSec.value) / 60));
});

const formal = computed(() => device.value?.identity === "formal");

const rotateHint = computed(() => {
  if (formal.value) return "正式节点用 network_secret 认证，不使用凭据，轮换没有意义。";
  if (cooling.value) {
    return `${relTime(device.value?.credential?.issuedAt ?? 0, nowSec.value)}刚签发，约 ${coolingMinutes.value} 分钟后才能再签；冷却内再签只会让等待重新计时。`;
  }
  return "签发后约 5 分钟冷却结束，设备在下一次心跳拿到新凭据并重启 core；旧凭据在设备确认后失效。";
});

const rotateDisabled = computed(() => formal.value || cooling.value);

const credentialText = computed(() => {
  if (!device.value) return "—";
  if (formal.value) return "不使用凭据（正式节点）";
  const credential = device.value.credential;
  if (!credential) return "尚未签发";
  return `${relTime(credential.issuedAt, nowSec.value)}签发 · ${absTime(credential.expiresAt)} 到期`;
});

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const detail = await api.getDevice(props.id);
    device.value = detail;
    name.value = detail.name;
    toml.value = detail.configToml ?? "";
    mode.value = "view";
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "加载失败";
  } finally {
    loading.value = false;
  }
}

watch(() => props.id, load, { immediate: true });

function showError(err: unknown, fallback: string) {
  if (err instanceof ApiError && err.status === 401) {
    emit("unauthorized");
    return;
  }
  error.value = err instanceof Error ? err.message : fallback;
}

function startEdit() {
  if (!device.value) return;
  name.value = device.value.name;
  toml.value = device.value.configToml;
  error.value = "";
  mode.value = "edit";
}

function cancelEdit() {
  if (!device.value) return;
  name.value = device.value.name;
  toml.value = device.value.configToml;
  error.value = "";
  mode.value = "view";
}

/** 只替换编辑器里的文本；要保存才下发。 */
async function resetToTemplate() {
  if (resetting.value) return;
  error.value = "";
  resetting.value = true;
  try {
    toml.value = (await api.template()).toml;
  } catch (err) {
    showError(err, "读取默认模板失败");
  } finally {
    resetting.value = false;
  }
}

async function save() {
  const current = device.value;
  if (!current || saving.value) return;
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
  const warning = validateToml(toml.value);
  if (warning) {
    error.value = warning;
    return;
  }
  saving.value = true;
  try {
    const saved = await api.updateDevice(current.id, trimmed, toml.value);
    device.value = {
      ...current,
      name: trimmed,
      configToml: toml.value,
      configHash: saved.configHash,
      configState: saved.configHash === current.lastConfigHash ? "current" : "stale",
    };
    mode.value = "view";
    emit("toast", "配置已保存", "设备下一轮心跳会拉到新配置并重启 core", "ok");
    emit("changed");
  } catch (err) {
    showError(err, "保存失败");
  } finally {
    saving.value = false;
  }
}

async function issueToken() {
  if (!device.value || tokenBusy.value) return;
  tokenBusy.value = true;
  error.value = "";
  try {
    token.value = (await api.issueToken(device.value.id)).token;
  } catch (err) {
    showError(err, "生成令牌失败");
  } finally {
    tokenBusy.value = false;
  }
}

async function copyToken() {
  emit("toast", (await copyText(token.value)) ? "已复制" : "复制失败", "", "ok");
}

async function rotate() {
  if (!device.value || rotating.value) return;
  rotating.value = true;
  actionError.value = "";
  try {
    await api.rotateCredential(device.value.id);
    confirm.value = null;
    await load();
    emit("toast", "凭据已签发", "冷却结束后的心跳会把新凭据下发给设备，设备会重启 core", "ok");
    emit("changed");
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    actionError.value = err instanceof Error ? err.message : "签发失败";
  } finally {
    rotating.value = false;
  }
}

async function remove() {
  if (!device.value || deleting.value) return;
  deleting.value = true;
  actionError.value = "";
  try {
    await api.deleteDevice(device.value.id);
    emit("toast", "已撤销", device.value.name, "ok");
    emit("changed");
    emit("close");
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    actionError.value = err instanceof Error ? err.message : "撤销失败";
  } finally {
    deleting.value = false;
  }
}
</script>

<template>
  <div class="drawer-layer" @click.self="emit('close')">
    <aside class="drawer" role="dialog" aria-modal="true">
      <header class="drawer-head">
        <div>
          <div class="drawer-title">
            {{ device?.name ?? "设备详情" }}
            <span v-if="device" class="tag">{{ identityText(device.identity) }}</span>
          </div>
          <div v-if="device" class="drawer-sub">
            <span :class="statusDotClass(device.status)" />
            {{ statusText(device.status) }}
            <template v-if="device.lastSeenAt"> · 最后心跳 {{ relTime(device.lastSeenAt, nowSec) }}</template>
          </div>
        </div>
        <button class="icon-btn" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </header>

      <div class="drawer-body">
        <div v-if="loading" class="hint">加载中…</div>

        <template v-else-if="device">
          <div class="facts">
            <div class="fact">
              <div class="k">身份</div>
              <div class="v">{{ identityText(device.identity) }}</div>
            </div>
            <div class="fact">
              <div class="k">mesh 地址</div>
              <div class="v">{{ fixedAddress(device) }}</div>
            </div>
            <div class="fact">
              <div class="k">设备 ID</div>
              <div class="v mono">{{ device.deviceId || "未注册" }}</div>
            </div>
            <div class="fact">
              <div class="k">主机名</div>
              <div class="v">{{ device.hostname || "—" }}</div>
            </div>
            <div class="fact">
              <div class="k">分组</div>
              <div class="v">{{ groupName }}</div>
            </div>
            <div class="fact">
              <div class="k">最近 IP</div>
              <div class="v">{{ device.lastIp || "—" }}</div>
            </div>
            <div class="fact">
              <div class="k">平台</div>
              <div class="v">{{ device.lastPlatform || "—" }}</div>
            </div>
            <div class="fact">
              <div class="k">配置 hash</div>
              <div class="v mono">{{ device.configHash }}</div>
            </div>
            <div class="fact">
              <div class="k">客户端</div>
              <div class="v">
                <template v-if="device.appVersion">
                  {{ device.appVersion }}
                  <span :class="appStateClass(device)">{{ appStateLabel(device) || "已同步" }}</span>
                </template>
                <template v-else-if="device.lastPlatform">未发布</template>
                <template v-else>未上报平台</template>
              </div>
            </div>
            <div class="fact">
              <div class="k">创建时间</div>
              <div class="v">{{ absTime(device.createdAt) }}</div>
            </div>
            <div class="fact">
              <div class="k">最后心跳</div>
              <div class="v">{{ device.lastSeenAt ? absTime(device.lastSeenAt) : "从未" }}</div>
            </div>
            <div class="fact wide">
              <div class="k">凭据</div>
              <div class="v">{{ credentialText }}</div>
            </div>
            <div class="fact wide">
              <div class="k">公钥</div>
              <div class="v mono">{{ device.pubkey || "—" }}</div>
            </div>
          </div>

          <section v-if="mode === 'view'" class="section">
            <div class="section-head"><span class="section-title">操作</span></div>
            <div class="op-grid">
              <div class="op">
                <button type="button" :disabled="rotateDisabled || rotating" @click="confirm = 'rotate'">轮换凭据</button>
                <p class="hint">{{ rotateHint }}</p>
              </div>
              <div class="op">
                <button type="button" :disabled="tokenBusy" @click="issueToken">
                  {{ tokenBusy ? "生成中…" : "签发注册令牌" }}
                </button>
                <p class="hint">10 分钟有效，一次性；设备注册成功后作废。</p>
              </div>
            </div>
            <div v-if="token" class="token-box">
              <div class="token-label">注册令牌（10 分钟有效，一次性）</div>
              <div class="token-row">
                <code class="token-value">{{ token }}</code>
                <button type="button" @click="copyToken">复制</button>
              </div>
            </div>
          </section>

          <section class="section">
            <div class="section-head">
              <span class="section-title">配置</span>
              <span :class="configStateClass(device.configState)">{{ configStateText(device.configState) }}</span>
              <span class="spacer" />
              <template v-if="mode === 'view'">
                <button class="ghost" type="button" @click="startEdit">编辑</button>
              </template>
              <template v-else>
                <button class="ghost" type="button" :disabled="resetting" @click="resetToTemplate">
                  {{ resetting ? "读取中…" : "重置为模板" }}
                </button>
              </template>
            </div>

            <pre v-if="mode === 'view'" class="config-preview">{{ device.configToml }}</pre>

            <div v-else class="edit-form">
              <label class="field-label" for="device-name">设备名称</label>
              <input id="device-name" v-model="name" type="text" maxlength="64" autocomplete="off" />
              <label class="field-label" for="device-toml">EasyTier 配置（TOML）</label>
              <textarea id="device-toml" v-model="toml" class="toml-editor" spellcheck="false" />
              <p class="hint">「重置为模板」会把编辑器内容替换成当前默认模板，保存后才会下发给设备。</p>
              <div class="actions">
                <button class="primary" type="button" :disabled="saving" @click="save">
                  {{ saving ? "保存中…" : "保存" }}
                </button>
                <button type="button" @click="cancelEdit">取消</button>
              </div>
            </div>
          </section>

          <div v-if="mode === 'view'" class="danger-zone">
            <button class="danger" type="button" @click="confirm = 'delete'">撤销设备</button>
            <p class="hint">删除记录并撤销该设备的全部凭据；设备之后的签名请求会被拒绝，此操作不可撤销。</p>
          </div>
        </template>

        <div v-if="error" class="err">{{ error }}</div>
        <div v-if="actionError" class="err">{{ actionError }}</div>
      </div>

      <ConfirmDialog
        v-if="confirm === 'rotate' && device"
        title="轮换凭据"
        confirm-label="签发新凭据"
        :busy="rotating"
        @cancel="confirm = null"
        @confirm="rotate"
      >
        <p class="hint">为「{{ device.name }}」签发一张新凭据。控制台不保存、也不显示凭据内容。</p>
        <ul class="consequences">
          <li>签发后进入 5 分钟冷却，冷却结束后的下一次心跳才把新凭据下发给设备。</li>
          <li>设备拿到新凭据后会重启 core，mesh 网络中断几秒。</li>
          <li>设备确认用上新凭据之后，旧凭据才失效——不是点击后立刻失效。</li>
        </ul>
        <div v-if="device.status !== 'online'" class="callout">设备当前离线，凭据会在它恢复心跳后下发。</div>
        <div v-if="device.credential" class="callout">
          最近签发：{{ absTime(device.credential.issuedAt) }}（{{ relTime(device.credential.issuedAt, nowSec) }}）
        </div>
        <div v-if="actionError" class="err">{{ actionError }}</div>
      </ConfirmDialog>

      <ConfirmDialog
        v-if="confirm === 'delete' && device"
        title="撤销设备"
        confirm-label="撤销设备"
        danger
        :busy="deleting"
        @cancel="confirm = null"
        @confirm="remove"
      >
        <p>
          撤销「{{ device.name }}」后，记录会被删除，名下凭据全部撤销，设备之后的签名请求会被拒绝。
        </p>
        <div v-if="actionError" class="err">{{ actionError }}</div>
      </ConfirmDialog>
    </aside>
  </div>
</template>