// 控制台的 HTTP 客户端：请求路径与响应字段直接对应 CONTRACT.md 1.4。
export type NodeIdentity = "formal" | "credential";

export interface DeviceSummary {
  id: string;
  name: string;
  hostname: string;
  device_id: string;
  status: "online" | "offline" | "pending";
  config_hash: string;
  last_config_hash: string;
  last_app_hash: string;
  config_state: "current" | "stale";
  app_state: "current" | "stale";
  app_version: string;
  last_platform: string;
  group_id: number;
  position: number;
  created_at: number;
  last_seen_at: number;
  /** identity 由配置里有没有 network_secret 派生：正式节点不用凭据。 */
  identity: NodeIdentity;
  /** ipv4 是配置里写的固定 mesh 地址（可能带前缀长度）；空表示没写。 */
  ipv4: string;
  /** dhcp = true 时 core 忽略 ipv4，地址由网络分配。 */
  dhcp: boolean;
}

/** CredentialTimes 是最新一张凭据的可见部分：只有时间，没有私钥。 */
export interface CredentialTimes {
  issued_at: number;
  expires_at: number;
}

/** GroupView 是设备分组：position 决定分组在控制台里的先后。 */
export interface GroupView {
  id: number;
  name: string;
  position: number;
}

/** DevicePlacement 是一次拖拽结束后某台设备的新归属与新位置。 */
export interface DevicePlacement {
  id: string;
  group_id: number;
  position: number;
}

export interface DeviceDetail extends DeviceSummary {
  pubkey: string;
  config_toml: string;
  token: string;
  token_expires_at: number;
  last_ip: string;
  credential: CredentialTimes | null;
}

/** ReleaseView 是某个平台已发布的两个更新包：客户端包与启动器。 */
export interface ReleaseView {
  platform: string;
  app_version: string;
  app_url: string;
  app_hash: string;
  app_sha256: string;
  app_notes: string;
  launcher_version: string;
  launcher_url: string;
  launcher_sha256: string;
}

/** ReleasePublish 是一次发布要写的槽位；每个槽位的版本号与地址必须成对给出。 */
export interface ReleasePublish {
  platform?: string;
  app_version?: string;
  app_url?: string;
  app_notes?: string;
  launcher_version?: string;
  launcher_url?: string;
  default_toml?: string;
}

/** ReleasesPayload 是管理端看到的全部平台发布状态，外加默认模板。 */
export interface ReleasesPayload {
  default_toml: string;
  releases: ReleaseView[];
}

/** The bearer credential lives in sessionStorage: gone when the tab closes. */
const CREDENTIAL_KEY = "mesh_credential";

function credential(): string {
  return sessionStorage.getItem(CREDENTIAL_KEY) ?? "";
}

function clearCredential(): void {
  sessionStorage.removeItem(CREDENTIAL_KEY);
}

function authHeaders(): Record<string, string> {
  const token = credential();
  return token ? { Authorization: "Bearer " + token } : {};
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string) {
    super(describe(status, code));
    this.status = status;
    this.code = code;
  }
}

function describe(status: number, code: string): string {
  if (status === 401) return "登录已失效，请重新登录";
  if (code === "device_limit") return "设备数量已达上限（200）";
  if (code === "device_conflict") return "该设备 ID 已绑定其它公钥";
  if (code === "invalid_token") return "令牌无效或已过期";
  if (code === "not_found") return "设备不存在";
  if (code === "group_not_found") return "分组不存在";
  if (code === "formal_node") return "正式节点不使用凭据";
  if (code === "credential_channel_unconfigured") return "控制面未配置凭据签发通道";
  if (code === "busy") return "服务繁忙，请稍后重试";
  if (status === 400) return "请求内容不合法";
  return "请求失败（HTTP " + status + "）";
}

/** errorFrom turns a non-2xx response into an ApiError, preferring the server text. */
async function errorFrom(res: Response): Promise<ApiError> {
  if (res.status === 401) {
    clearCredential();
    return new ApiError(401, "unauthorized");
  }
  const text = await res.text();
  let code = "";
  let message = "";
  try {
    const payload = JSON.parse(text) as { code?: string; message?: string };
    code = payload.code ?? "";
    message = payload.message ?? "";
  } catch {
    // 非 JSON 的错误页：保留状态码文案。
  }
  const error = new ApiError(res.status, code);
  if (message) error.message = message;
  return error;
}

async function decode<T>(res: Response): Promise<T> {
  if (!res.ok) throw await errorFrom(res);
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

async function jsonRequest<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = authHeaders();
  const init: RequestInit = { method, headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  return decode<T>(await fetch(path, init));
}

/** 设置类请求的响应体固定是 ReleasesPayload，错误走同一套解析。 */
async function releasesRequest(path: string, init: RequestInit): Promise<ReleasesPayload> {
  const headers = new Headers(init.headers);
  for (const [name, value] of Object.entries(authHeaders())) headers.set(name, value);
  return decode<ReleasesPayload>(await fetch(path, { ...init, headers }));
}

export const api = {
  async auth(token: string): Promise<void> {
    const res = await fetch("/api/auth", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
    });
    if (res.status === 401) throw new ApiError(401, "invalid_token");
    if (!res.ok) throw new ApiError(res.status, "");
    const issued = (await res.json()) as { access_token: string };
    sessionStorage.setItem(CREDENTIAL_KEY, issued.access_token);
  },
  logout(): void {
    clearCredential();
  },
  listDevices(): Promise<DeviceSummary[]> {
    return jsonRequest<DeviceSummary[]>("GET", "/api/admin/devices");
  },
  getDevice(id: string): Promise<DeviceDetail> {
    return jsonRequest<DeviceDetail>("GET", "/api/admin/devices/" + encodeURIComponent(id));
  },
  createDevice(name: string, configToml: string): Promise<{ id: string; token: string }> {
    return jsonRequest<{ id: string; token: string }>("POST", "/api/admin/devices", { name, config_toml: configToml });
  },
  updateDevice(id: string, name: string, configToml: string): Promise<{ config_hash: string }> {
    return jsonRequest<{ config_hash: string }>("PUT", "/api/admin/devices/" + encodeURIComponent(id), {
      name,
      config_toml: configToml,
    });
  },
  deleteDevice(id: string): Promise<void> {
    return jsonRequest<void>("DELETE", "/api/admin/devices/" + encodeURIComponent(id));
  },
  issueToken(id: string): Promise<{ token: string }> {
    return jsonRequest<{ token: string }>("POST", "/api/admin/devices/" + encodeURIComponent(id) + "/token");
  },
  /**
   * 轮换凭据：服务端签发一张并把 id 记在设备名下，私钥只回给这一次请求。
   * 控制台不保存也不显示它，所以这里把响应丢掉。
   */
  async rotateCredential(id: string): Promise<void> {
    await jsonRequest<unknown>("POST", "/api/admin/devices/" + encodeURIComponent(id) + "/credential");
  },
  setDeviceGroup(id: string, groupId: number | null): Promise<void> {
    return jsonRequest<void>("PUT", "/api/admin/devices/" + encodeURIComponent(id) + "/group", { group_id: groupId });
  },
  saveLayout(devices: DevicePlacement[]): Promise<void> {
    return jsonRequest<void>("PUT", "/api/admin/layout", { devices });
  },
  listGroups(): Promise<GroupView[]> {
    return jsonRequest<GroupView[]>("GET", "/api/admin/groups");
  },
  createGroup(name: string): Promise<GroupView> {
    return jsonRequest<GroupView>("POST", "/api/admin/groups", { name });
  },
  updateGroup(id: number, patch: { name?: string; position?: number }): Promise<GroupView> {
    return jsonRequest<GroupView>("PUT", "/api/admin/groups/" + id, patch);
  },
  deleteGroup(id: number): Promise<void> {
    return jsonRequest<void>("DELETE", "/api/admin/groups/" + id);
  },
  template(): Promise<{ toml: string }> {
    return jsonRequest<{ toml: string }>("GET", "/api/admin/template");
  },
  getReleases(): Promise<ReleasesPayload> {
    return releasesRequest("/api/admin/releases", { method: "GET" });
  },
  publishRelease(fields: ReleasePublish): Promise<ReleasesPayload> {
    return releasesRequest("/api/admin/releases", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(fields),
    });
  },
  /** slot 不带时清掉该平台的两个槽位，带 "launcher" 时只清启动器。 */
  deleteRelease(platform: string, slot?: "launcher"): Promise<ReleasesPayload> {
    const query = slot ? "?slot=" + slot : "";
    return releasesRequest("/api/admin/releases/" + platform + query, { method: "DELETE" });
  },
};