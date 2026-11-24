import type { DeviceSummary } from "./api";

export const FALLBACK_TOML = [
  "# 测试网的默认配置。多台设备时 ipv4 必须各自不同（不要用 .0）。",
  'ipv4 = "10.126.126.2/24"',
  "",
  "[network_identity]",
  'network_name = "test-net"',
  'network_secret = "test-secret-2026"',
  "",
  "# 注意：这个段只设置开关，私钥由客户端启动 core 时用 --secure-mode 生成。",
  "[secure_mode]",
  "enabled = true",
  "",
  "[[peer]]",
  'uri = "tcp://172.19.230.131:11010"',
].join("\n");

/** 拦截最常见的一类配置错误：把网段当成节点地址填进 ipv4。 */
export function validateToml(text: string): string | null {
  const match = /^\s*ipv4\s*=\s*"([^"]*)"/m.exec(text);
  if (!match || match[1] === undefined) return null;
  const ip = match[1].split("/")[0]?.trim() ?? "";
  const parts = ip.split(".");
  if (parts.length === 4 && parts[3] === "0") {
    return "ipv4 填的是网络地址（末尾 .0），节点在链路上不可用。请改成网段内唯一的主机地址，例如 10.126.126.3/24";
  }
  return null;
}

export function statusText(status: string): string {
  if (status === "online") return "在线";
  if (status === "pending") return "待注册";
  return "离线";
}

export function statusRank(status: string): number {
  if (status === "online") return 0;
  if (status === "pending") return 1;
  return 2;
}

export function statusClass(status: string): string {
  if (status === "online") return "badge on";
  if (status === "pending") return "badge warn";
  return "badge off";
}

export function shortHash(hash: string): string {
  return hash ? hash.slice(0, 8) : "—";
}

export function shortDeviceID(id: string): string {
  if (!id) return "未注册";
  return id.slice(0, 8) + "…" + id.slice(-4);
}

export function configStateText(value: string): string {
  return value === "current" ? "已同步" : "待同步";
}

export function configStateClass(value: string): string {
  return value === "current" ? "badge on" : "badge warn";
}

export function appStateText(device: DeviceSummary): string {
  if (device.app_state === "current") return "已同步";
  return device.app_version ? "待更新 → " + device.app_version : "待更新";
}

export function appStateClass(device: DeviceSummary): string {
  return device.app_state === "current" ? "badge on" : "badge warn";
}

export function relTime(sec: number): string {
  const diff = Math.max(0, Math.floor(Date.now() / 1000) - sec);
  if (diff < 60) return diff + "s 前";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return Math.floor(diff / 86400) + " 天前";
}

export function absTime(sec: number): string {
  if (!sec) return "—";
  return new Date(sec * 1000).toLocaleString("zh-CN", { hour12: false });
}

export function formatMB(bytes: number): string {
  return (Math.max(0, bytes || 0) / (1024 * 1024)).toFixed(1) + " MB";
}
