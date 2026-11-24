import { ref } from "vue";

/** 秒级时钟：相对时间靠它每秒重算，不必等下一次轮询。 */
export const nowSec = ref(Math.floor(Date.now() / 1000));

window.setInterval(() => {
  nowSec.value = Math.floor(Date.now() / 1000);
}, 1000);
