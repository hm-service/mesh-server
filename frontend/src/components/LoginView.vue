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
  <main>
    <form class="login" @submit.prevent="onSubmit">
      <h1>管理员登录</h1>
      <p class="hint">输入部署时设置的 <code>AUTH_TOKEN</code>。</p>
      <label for="token">访问令牌</label>
      <input id="token" v-model="token" type="password" autocomplete="off" required />
      <div v-if="error" class="err">{{ error }}</div>
      <button class="primary" type="submit" :disabled="busy">{{ busy ? "登录中…" : "登录" }}</button>
    </form>
  </main>
</template>