<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { api, ApiError, type DeviceDetail, type GroupView, type ReleasesPayload } from "../api";
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
} from "../format";
import ConfigEditDialog from "./ConfigEditDialog.vue";
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
const editing = ref(false);
const token = ref("");
const tokenBusy = ref(false);
const confirm = ref<ConfirmKind>(null);
const rotating = ref(false);
const deleting = ref(false);
const actionError = ref("");
const savingAutoUpdate = ref(false);
// releases 只用于给「跟随发布包」标出当前生效值：取不到就不显示那半句。
const releases = ref<ReleasesPayload | null>(null);

/** 下拉框的三态取值：回落到 device.autoUpdate 的三个字符串。 */
const autoUpdateValue = computed(() => {
  const value = device.value?.autoUpdate;
  if (value === true) return "auto";
  if (value === false) return "manual";
  return "follow";
});

/** 包上的自动更新开关；该平台没有发布记录时为 null。 */
const packageAutoUpdate = computed<boolean | null>(() => {
  const platform = device.value?.lastPlatform ?? "";
  const release = releases.value?.releases.find((item) => item.platform === platform);
  return release ? release.autoUpdate : null;
});

/** autoUpdateHint 说明当前策略；「跟随」时点出实际生效的那一侧。 */
const autoUpdateHint = computed(() => {
  const value = device.value?.autoUpdate;
  if (value === true) return "覆盖发布包：设备拿到新版本会自己装。";
  if (value === false) return "覆盖发布包：设备只提示有新版本，等人工确认。";
  const fromPackage = packageAutoUpdate.value;
  if (fromPackage === null) return "跟随发布包的设置；这个平台还没有发布记录。";
  return "跟随发布包：当前是" + (fromPackage ? "自动安装" : "手动确认") + "。";
});

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
    device.value = await api.getDevice(props.id);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    error.value = err instanceof Error ? err.message : "加载失败";
  } finally {
    loading.value = false;
  }
  // 发布记录只是给「跟随」补一句当前生效值，取不到不影响详情。
  try {
    releases.value = await api.getReleases();
  } catch {
    releases.value = null;
  }
}

/** changeAutoUpdate 写设备级覆盖：follow 是 null，auto/manual 是强制值。 */
async function changeAutoUpdate(event: Event) {
  if (!device.value || savingAutoUpdate.value) return;
  const choice = (event.target as HTMLSelectElement).value;
  const value = choice === "auto" ? true : choice === "manual" ? false : null;
  savingAutoUpdate.value = true;
  actionError.value = "";
  try {
    await api.updateDevice(device.value.id, device.value.name, device.value.configToml, value);
    device.value.autoUpdate = value;
    emit("toast", "自动更新已设置", autoUpdateHint.value, "ok");
    emit("changed");
  } catch (err) {
    // 没保存成功：把下拉拨回真实状态，别让界面停在没生效的选项上。
    (event.target as HTMLSelectElement).value = autoUpdateValue.value;
    if (err instanceof ApiError && err.status === 401) {
      emit("unauthorized");
      return;
    }
    actionError.value = err instanceof Error ? err.message : "保存失败";
  } finally {
    savingAutoUpdate.value = false;
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

function forwardToast(title: string, message: string, kind: "ok" | "err") {
  emit("toast", title, message, kind);
}

function onSaved(next: DeviceDetail) {
  device.value = next;
  editing.value = false;
  emit("changed");
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
  <div class="dev-detail">
    <div class="detail-head">
      <div>
        <div class="detail-title">
          {{ device?.name ?? "设备详情" }}
          <span v-if="device" class="tag">{{ identityText(device.identity) }}</span>
        </div>
        <div v-if="device" class="detail-sub">
          <span :class="statusDotClass(device.status)" />
          {{ statusText(device.status) }}
          <template v-if="device.lastSeenAt"> · 最后心跳 {{ relTime(device.lastSeenAt, nowSec) }}</template>
        </div>
      </div>
      <button class="icon-btn" type="button" aria-label="收起" @click="emit('close')">×</button>
    </div>

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
        <div class="fact">
          <div class="k">凭据</div>
          <div class="v">{{ credentialText }}</div>
        </div>
        <div class="fact wide">
          <div class="k">公钥</div>
          <div class="v mono">{{ device.pubkey || "—" }}</div>
        </div>
      </div>

      <section class="section">
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
          <button class="ghost" type="button" @click="editing = true">查看与编辑配置</button>
        </div>
      </section>

      <section class="section">
        <div class="section-head"><span class="section-title">更新</span></div>
        <div class="field">
          <label class="field-label" for="device-auto-update">自动更新</label>
          <select
            id="device-auto-update"
            :value="autoUpdateValue"
            :disabled="savingAutoUpdate"
            @change="changeAutoUpdate"
          >
            <option value="follow">跟随发布包</option>
            <option value="auto">总是自动安装</option>
            <option value="manual">总是手动确认</option>
          </select>
          <p class="hint">{{ autoUpdateHint }}</p>
        </div>
      </section>

      <div class="danger-zone">
        <button class="danger" type="button" @click="confirm = 'delete'">撤销设备</button>
        <p class="hint">删除记录并撤销该设备的全部凭据；设备之后的签名请求会被拒绝，此操作不可撤销。</p>
      </div>
    </template>

    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="actionError" class="err">{{ actionError }}</div>

    <ConfigEditDialog
      v-if="editing && device"
      :device="device"
      @close="editing = false"
      @saved="onSaved"
      @toast="forwardToast"
      @unauthorized="emit('unauthorized')"
    />

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
  </div>
</template>