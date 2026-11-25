import type { DeviceSummary, NodeIdentity } from "./api";

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

export function statusDotClass(status: string): string {
  if (status === "online") return "dot";
  if (status === "pending") return "dot pending";
  return "dot off";
}

/** meshAddress 是列表里显示的地址：DHCP 分配时显示 DHCP，固定地址去掉前缀长度。 */
export function meshAddress(device: DeviceSummary): string {
  if (device.dhcp) return "DHCP";
  return stripPrefix(device.ipv4) || "—";
}

export function fixedAddress(device: DeviceSummary): string {
  if (device.dhcp) return "DHCP 分配";
  return device.ipv4 || "—";
}

function stripPrefix(addr: string): string {
  const slash = addr.indexOf("/");
  return slash === -1 ? addr : addr.slice(0, slash);
}

export function identityText(identity: NodeIdentity): string {
  return identity === "formal" ? "正式节点" : "凭据节点";
}

export function configStateText(value: string): string {
  return value === "current" ? "已同步" : "待同步";
}

export function configStateClass(value: string): string {
  return value === "current" ? "state current" : "state stale";
}

/** appStateLabel 只在有新版本可更新时给文案；没有发布或已同步时为空。 */
export function appStateLabel(device: DeviceSummary): string {
  if (!device.app_version || device.app_state === "current") return "";
  return "待更新";
}

export function appStateClass(device: DeviceSummary): string {
  return device.app_state === "current" ? "state current" : "state update";
}

export function relTime(sec: number, now: number): string {
  const diff = Math.max(0, now - sec);
  if (diff < 60) return diff + " 秒前";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return Math.floor(diff / 86400) + " 天前";
}

export function absTime(sec: number): string {
  if (!sec) return "—";
  return new Date(sec * 1000).toLocaleString("zh-CN", { hour12: false });
}
