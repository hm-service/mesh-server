// 配置模板里的占位符：{{ KEY }}，KEY 允许首尾空白。
//
// 控制面只保存模板原文、把配置原样收下：它不认识占位符，也不判断地址是否重复。
// 占位符的解析、表单展示、默认值与替换都在这里做；编辑既有设备时还要反过来，把裸配置
// 里的字面量读回表单值、换回占位符。这是控制台里唯一「针对 easytier 做具体实现」的
// 地方——加一种新协议，就在这里加一行 FIELDS。

/**
 * FIELDS 是认识的占位符及其展示元数据；不认识的 key 只能在「配置文件」里手填。
 * literal 是同一个字段写在裸配置里的样子：只认这一种写法，匹配不到就当作读不出来，
 * 不去猜（配置里本来就没有这一行、写成了数组或别的形式时都算读不出来）。
 */
const FIELDS: Record<string, { label: string; example: string; literal: RegExp }> = {
  IP: { label: "IP", example: "10.126.126.100/24", literal: /^[ \t]*ipv4[ \t]*=[ \t]*"([^"\n]*)"/m },
};

/** FIELD_KEYS 是认识的字段名：编辑裸配置时按它逐个反解。 */
export const FIELD_KEYS: readonly string[] = Object.keys(FIELDS);

/** TemplateField 是表单里的一行：key 来自模板，其余来自 FIELDS。 */
export interface TemplateField {
  key: string;
  label: string;
  example: string;
  /** value 是预填值：目前只有 IP 有，来自设备列表算出的建议地址。 */
  value: string;
}

/** resolveTemplate 按出现顺序返回模板里不重复的占位符 key。 */
export function resolveTemplate(content: string): string[] {
  const keys: string[] = [];
  let rest = content;
  for (;;) {
    const open = rest.indexOf("{{");
    if (open < 0) return keys;
    const close = rest.indexOf("}}", open + 2);
    // 没配对的 {{ 是语法错误；扫描到此为止，由 applyTemplate 报出来。
    if (close < 0) return keys;
    const key = rest.slice(open + 2, close).trim();
    if (key && !keys.includes(key)) keys.push(key);
    rest = rest.slice(close + 2);
  }
}

/** applyTemplate 把 values 填进模板；有占位符没有值就抛错。 */
export function applyTemplate(content: string, values: Record<string, string>): string {
  const parts: string[] = [];
  let rest = content;
  for (;;) {
    const open = rest.indexOf("{{");
    if (open < 0) {
      parts.push(rest);
      return parts.join("");
    }
    const close = rest.indexOf("}}", open + 2);
    if (close < 0) throw new Error("配置模板里有一个没配对的 {{，请补上 }} 或删掉它");
    const key = rest.slice(open + 2, close).trim();
    const value = values[key];
    if (value === undefined) throw new Error("配置模板里的占位符 {{ " + key + " }} 没有值");
    parts.push(rest.slice(0, open), value);
    rest = rest.slice(close + 2);
  }
}

/** literalIn 返回字段在内容里的字面量匹配，没这种字段或匹配不到时返回 null。 */
function literalIn(content: string, key: string): RegExpExecArray | null {
  const pattern = FIELDS[key]?.literal;
  return pattern ? pattern.exec(content) : null;
}

/**
 * readField 从裸配置里读出某个字段的值：只认 FIELDS 里那一种写法，读不到返回空串。
 * 值里还留着 {{ … }} 说明这一行本来就没填过，同样算读不到。
 */
export function readField(content: string, key: string): string {
  const value = literalIn(content, key)?.[1] ?? "";
  return value.includes("{{") ? "" : value;
}

/** maskField 把裸配置里那个字段的值换成占位符，返回模板文本；读不到就原样返回。 */
export function maskField(content: string, key: string): string {
  const match = literalIn(content, key);
  const value = match?.[1] ?? "";
  if (!match || !value || value.includes("{{")) return content;
  // match[0] 以收尾的双引号结束，值就紧挨在它前面：只换这段，行的其余部分原样保留。
  const start = match.index + match[0].length - 1 - value.length;
  return content.slice(0, start) + "{{ " + key + " }}" + content.slice(start + value.length);
}

/** fieldsFor 把模板里认识的占位符拼成表单字段；suggested 是 IP 的预填值。 */
export function fieldsFor(content: string, suggested: string): TemplateField[] {
  const fields: TemplateField[] = [];
  for (const key of resolveTemplate(content)) {
    const meta = FIELDS[key];
    // 不认识的占位符没有表单，得在「配置文件」里手填。
    if (!meta) continue;
    // 目前只有 IP 有预填值（下一台设备建议用的地址）。
    fields.push({ key, label: meta.label, example: meta.example, value: key === "IP" ? suggested : "" });
  }
  return fields;
}

/** DEFAULT_POOL 是一台设备都没有时用的参考网段：10.126.126.0。 */
const DEFAULT_POOL = [10, 126, 126, 0].reduce((value, byte) => value * 256 + byte, 0);

/** toInt 把点分十进制转成无符号整数，不是 IPv4 时返回 null。 */
function toInt(host: string): number | null {
  const parts = host.split(".");
  if (parts.length !== 4) return null;
  let value = 0;
  for (const part of parts) {
    if (!/^\d{1,3}$/.test(part)) return null;
    const byte = Number(part);
    if (byte > 255) return null;
    value = value * 256 + byte;
  }
  return value;
}

/** hostOf 把无符号整数还原成点分十进制。 */
function hostOf(value: number): string {
  const bytes: number[] = [];
  for (const scale of [256 ** 3, 256 ** 2, 256, 1]) {
    bytes.push(Math.floor(value / scale) % 256);
  }
  return bytes.join(".");
}

/** hostAddress 取地址的主机部分（去掉前缀长度），不是 IPv4 时返回空串。 */
function hostAddress(value: string): string {
  const host = value.split("/")[0]?.trim() ?? "";
  return toInt(host) === null ? "" : host;
}

/**
 * nextFreeAddress 从设备列表算出建议地址：取现有设备里最小那台所在的 /24，从它
 * 之后找第一个没被占用的主机地址（跳过网络地址与广播地址）；一台设备都没有时从
 * 10.126.126.1 开始。返回值带 /24，网段用尽时是空串。
 */
export function nextFreeAddress(devices: { ipv4: string }[]): string {
  const taken = new Set<number>();
  let lowest: number | null = null;
  for (const device of devices) {
    const value = toInt(hostAddress(device.ipv4));
    if (value === null) continue;
    taken.add(value);
    if (lowest === null || value < lowest) lowest = value;
  }
  const network = lowest === null ? DEFAULT_POOL : lowest - (lowest % 256);
  const start = Math.max((lowest ?? network) + 1, network + 1);
  for (let value = start; value < network + 255; value++) {
    if (!taken.has(value)) return hostOf(value) + "/24";
  }
  return "";
}

/**
 * fieldWarning 返回某个字段值的提示。目前只有 IP 有：与已有设备撞车。提示不阻断
 * 提交——同一个地址在多网卡、迁移期新旧设备并存或临时排查时可能是有意为之。
 */
export function fieldWarning(key: string, value: string, devices: { name: string; ipv4: string }[]): string {
  if (key !== "IP") return "";
  const host = hostAddress(value);
  if (host === "") return "";
  const owner = devices.find((device) => hostAddress(device.ipv4) === host);
  return owner ? "这个地址已经被 " + owner.name + " 使用" : "";
}
