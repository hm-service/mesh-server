<script setup lang="ts">
import { ref } from "vue";

const props = defineProps<{ error: string; busy: boolean }>();
const emit = defineEmits<{ submit: [password: string] }>();

const password = ref("");

function onSubmit() {
  if (!password.value || props.busy) return;
  emit("submit", password.value);
}
</script>

<template>
  <main>
    <form class="login" @submit.prevent="onSubmit">
      <h1>管理员登录</h1>
      <p class="hint">首次启动时，服务端会在日志里打印 12 位随机管理员密码。</p>
      <label for="password">密码</label>
      <input id="password" v-model="password" type="password" autocomplete="current-password" required />
      <div v-if="error" class="err">{{ error }}</div>
      <button class="primary" type="submit" :disabled="busy">{{ busy ? "登录中…" : "登录" }}</button>
    </form>
  </main>
</template>