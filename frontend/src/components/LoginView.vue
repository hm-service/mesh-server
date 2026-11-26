<script setup lang="ts">
import { ref } from "vue";

const props = defineProps<{ error: string; busy: boolean }>();
const emit = defineEmits<{ submit: [token: string] }>();

const token = ref("");

function onSubmit() {
  if (!token.value || props.busy) return;
  emit("submit", token.value);
}
</script>

<template>
  <main class="login-wrap">
    <form class="login" @submit.prevent="onSubmit">
      <div class="brand">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round">
          <path d="M12 4.4 19.3 17.9H4.7z" />
          <circle cx="12" cy="4.4" r="2.1" fill="#fff" />
          <circle cx="19.3" cy="17.9" r="2.1" fill="#fff" />
          <circle cx="4.7" cy="17.9" r="2.1" fill="#fff" />
          <circle cx="12" cy="13.2" r="1.6" fill="#fff" />
        </svg>
        Mesh <small>控制平台</small>
      </div>
      <h1>管理员登录</h1>
      <p class="hint">输入部署时设置的 <code>AUTH_TOKEN</code>。</p>
      <label for="token">访问令牌</label>
      <input id="token" v-model="token" type="password" autocomplete="off" required />
      <div v-if="error" class="err">{{ error }}</div>
      <button class="primary" type="submit" :disabled="busy">{{ busy ? "登录中…" : "登录" }}</button>
    </form>
  </main>
</template>
