// 控制台的 HTTP 客户端：请求路径与响应字段直接对应 CONTRACT.md 1.4。
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
  created_at: number;
  last_seen_at: number;
}

export interface DeviceDetail extends DeviceSummary {
  pubkey: string;
  config_toml: string;
  token: string;
  token_expires_at: number;
  last_ip: string;
}

export interface ConsoleSettings {
  default_toml: string;
  app_version: string;
  app_url: string;
  app_hash: string;
  app_sha256: string;
  app_notes: string;
  app_source: string;
  app_file_name: string;
  app_size: number;
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
  if (code === "busy") return "服务繁忙，请稍后重试";
  if (status === 400) return "请求内容不合法";
  return "请求失败（HTTP " + status + "）";
}

async function decode<T>(res: Response): Promise<T> {
  if (res.status === 401) throw new ApiError(401, "unauthorized");
  if (!res.ok) {
    const text = await res.text();
    let code = "";
    let message = "";
    try {
      const payload = JSON.parse(text) as { code?: string; message?: string };
      code = payload.code ?? "";
      message = payload.message ?? "";
    } catch {
      code = "";
    }
    if (message) throw new ApiError(res.status, code);
    throw new ApiError(res.status, code);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

async function jsonRequest<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: "same-origin", headers: {} };
  if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  return decode<T>(await fetch(path, init));
}

/** 设置类请求的 400 会带 message，直接展示比通用文案有用。 */
async function settingsRequest(init: RequestInit): Promise<ConsoleSettings> {
  const res = await fetch("/api/admin/settings", { credentials: "same-origin", ...init });
  if (res.status === 401) throw new ApiError(401, "unauthorized");
  if (!res.ok) {
    const text = await res.text();
    let payload: { code?: string; message?: string } = {};
    try {
      payload = JSON.parse(text) as { code?: string; message?: string };
    } catch {
      payload = {};
    }
    if (payload.message) {
      const error = new ApiError(res.status, payload.code ?? "");
      error.message = payload.message;
      throw error;
    }
    throw new ApiError(res.status, payload.code ?? "");
  }
  return (await res.json()) as ConsoleSettings;
}

export const api = {
  login(password: string): Promise<void> {
    return jsonRequest<void>("POST", "/api/login", { password });
  },
  logout(): Promise<void> {
    return jsonRequest<void>("POST", "/api/logout");
  },
  listDevices(): Promise<DeviceSummary[]> {
    return jsonRequest<DeviceSummary[]>("GET", "/api/devices");
  },
  getDevice(id: string): Promise<DeviceDetail> {
    return jsonRequest<DeviceDetail>("GET", "/api/devices/" + encodeURIComponent(id));
  },
  createDevice(name: string, configToml: string): Promise<{ id: string; token: string }> {
    return jsonRequest<{ id: string; token: string }>("POST", "/api/devices", { name, config_toml: configToml });
  },
  updateDevice(id: string, name: string, configToml: string): Promise<{ config_hash: string }> {
    return jsonRequest<{ config_hash: string }>("PUT", "/api/devices/" + encodeURIComponent(id), {
      name,
      config_toml: configToml,
    });
  },
  deleteDevice(id: string): Promise<void> {
    return jsonRequest<void>("DELETE", "/api/devices/" + encodeURIComponent(id));
  },
  issueToken(id: string): Promise<{ token: string }> {
    return jsonRequest<{ token: string }>("POST", "/api/devices/" + encodeURIComponent(id) + "/token");
  },
  template(): Promise<{ toml: string }> {
    return jsonRequest<{ toml: string }>("GET", "/api/template");
  },
  getSettings(): Promise<ConsoleSettings> {
    return settingsRequest({ method: "GET" });
  },
  putSettings(fields: Record<string, string>): Promise<ConsoleSettings> {
    return settingsRequest({
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(fields),
    });
  },
  uploadRelease(form: FormData): Promise<ConsoleSettings> {
    return settingsRequest({ method: "PUT", body: form });
  },
};
