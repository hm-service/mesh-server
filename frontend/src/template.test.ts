// template.ts 是控制台里唯一有业务逻辑的前端模块：占位符扫描/替换、字段元数据、
// 建议地址、撞车提示。这些用例盯的是边界，不是实现细节。
import { describe, expect, it } from "vitest";
import {
  applyTemplate,
  fieldWarning,
  fieldsFor,
  maskField,
  nextFreeAddress,
  readField,
  resolveTemplate,
} from "./template";

describe("resolveTemplate", () => {
  it("没有占位符时返回空数组", () => {
    expect(resolveTemplate("dhcp = false\n")).toEqual([]);
  });

  it("按出现顺序列出 key", () => {
    expect(resolveTemplate("a = {{ B }}\nb = {{ A }}\n")).toEqual(["B", "A"]);
  });

  it("同一个 key 只出现一次", () => {
    expect(resolveTemplate("a = {{ IP }}\nb = {{ IP }}\n")).toEqual(["IP"]);
  });

  it("key 两侧的空白不算名字的一部分", () => {
    expect(resolveTemplate("a = {{   IP   }}\n")).toEqual(["IP"]);
  });

  it("没有收尾的 }} 时扫描到此为止", () => {
    expect(resolveTemplate("a = {{ IP }}\nb = {{ HALF\n")).toEqual(["IP"]);
  });
});

describe("applyTemplate", () => {
  it("把值原样填进去", () => {
    expect(applyTemplate('ipv4 = "{{ IP }}"', { IP: "10.126.126.4/24" })).toBe('ipv4 = "10.126.126.4/24"');
  });

  it("同一个占位符出现多次全都替换", () => {
    expect(applyTemplate("{{ IP }} 和 {{ IP }}", { IP: "x" })).toBe("x 和 x");
  });

  it("没有占位符时原样返回", () => {
    expect(applyTemplate("dhcp = false\n", {})).toBe("dhcp = false\n");
  });

  it("占位符没有值就抛错，而不是留下半个模板", () => {
    expect(() => applyTemplate("{{ IP }}", {})).toThrow(/没有值/);
  });

  it("没配对的 {{ 也抛错", () => {
    expect(() => applyTemplate("{{ IP", { IP: "x" })).toThrow(/没配对/);
  });
});

describe("readField / maskField", () => {
  const config = 'dhcp = false\nipv4 = "10.126.126.1/24"\n[flags]\n';

  it("从裸配置里读出地址", () => {
    expect(readField(config, "IP")).toBe("10.126.126.1/24");
  });

  it("等号两侧的空格和行首缩进都认", () => {
    expect(readField('  ipv4   =   "10.126.126.9/24"\n', "IP")).toBe("10.126.126.9/24");
  });

  it("配置里没有这一行就读不到", () => {
    expect(readField("dhcp = false\n", "IP")).toBe("");
  });

  it("写成数组算读不到：只认一种写法，不猜", () => {
    expect(readField('ipv4 = ["10.126.126.1/24"]\n', "IP")).toBe("");
  });

  it("值里还留着占位符算读不到", () => {
    expect(readField('ipv4 = "{{ IP }}"\n', "IP")).toBe("");
  });

  it("maskField 把值换回占位符", () => {
    expect(maskField(config, "IP")).toBe('dhcp = false\nipv4 = "{{ IP }}"\n[flags]\n');
  });

  it("maskField 只换值，行尾注释与缩进原样保留", () => {
    expect(maskField('  ipv4 = "10.126.126.9/24"  # 固定地址\n', "IP")).toBe(
      '  ipv4 = "{{ IP }}"  # 固定地址\n',
    );
  });

  it("maskField 读不到时原样返回", () => {
    expect(maskField("dhcp = false\n", "IP")).toBe("dhcp = false\n");
  });

  it("来回一次不失真", () => {
    const masked = maskField(config, "IP");
    expect(applyTemplate(masked, { IP: readField(config, "IP") })).toBe(config);
  });
});

describe("fieldsFor", () => {
  it("认识的占位符出一行，IP 用传进来的建议值", () => {
    expect(fieldsFor('ipv4 = "{{ IP }}"', "10.126.126.4/24")).toEqual([
      { key: "IP", label: "IP", example: "10.126.126.100/24", value: "10.126.126.4/24" },
    ]);
  });

  it("没有占位符就没有表单", () => {
    expect(fieldsFor("dhcp = false\n", "x")).toEqual([]);
  });

  it("不认识的占位符不进表单，只能在配置文件里手填", () => {
    expect(fieldsFor("a = {{ NET }}\n", "x")).toEqual([]);
  });
});

describe("nextFreeAddress", () => {
  const of = (...hosts: string[]) => hosts.map((ipv4) => ({ ipv4 }));

  it("一台设备都没有时从默认网段的 .1 开始", () => {
    expect(nextFreeAddress([])).toBe("10.126.126.1/24");
  });

  it("从最小那台之后找第一个空的", () => {
    expect(nextFreeAddress(of("10.126.126.1/24"))).toBe("10.126.126.2/24");
    expect(nextFreeAddress(of("10.126.126.1/24", "10.126.126.2/24"))).toBe("10.126.126.3/24");
    expect(nextFreeAddress(of("10.126.126.1/24", "10.126.126.2/24", "10.126.126.3/24"))).toBe(
      "10.126.126.4/24",
    );
  });

  it("中间有洞就填洞，不是一路往后加", () => {
    expect(nextFreeAddress(of("10.126.126.1/24", "10.126.126.3/24"))).toBe("10.126.126.2/24");
  });

  it("只有一台大地址时从它之后接", () => {
    expect(nextFreeAddress(of("10.126.126.100/24"))).toBe("10.126.126.101/24");
  });

  it("带不带前缀长度都认成同一个主机地址", () => {
    expect(nextFreeAddress(of("10.126.126.1", "10.126.126.2/24"))).toBe("10.126.126.3/24");
  });

  it("解析不了的行跳过，不影响分配", () => {
    expect(nextFreeAddress(of("10.126.126.1/24", "", "dhcp", "10.126.126.300/24"))).toBe(
      "10.126.126.2/24",
    );
  });

  it("跟着最小那台所在的网段走", () => {
    expect(nextFreeAddress(of("10.0.0.5/24", "10.126.126.1/24"))).toBe("10.0.0.6/24");
  });

  it("整个 /24 用尽时返回空串", () => {
    const full = Array.from({ length: 254 }, (_, i) => ({ ipv4: `10.126.126.${i + 1}/24` }));
    expect(nextFreeAddress(full)).toBe("");
  });
});

describe("fieldWarning", () => {
  const devices = [
    { name: "tencent-seed", ipv4: "10.126.126.2/24" },
    { name: "ali-seed", ipv4: "10.126.126.1/24" },
  ];

  it("撞上已有设备时说出是谁", () => {
    expect(fieldWarning("IP", "10.126.126.2/24", devices)).toBe("这个地址已经被 tencent-seed 使用");
  });

  it("只写了主机部分也算撞车", () => {
    expect(fieldWarning("IP", "10.126.126.1", devices)).toBe("这个地址已经被 ali-seed 使用");
  });

  it("没撞车时不提示", () => {
    expect(fieldWarning("IP", "10.126.126.4/24", devices)).toBe("");
  });

  it("空值或看不懂的值不提示", () => {
    expect(fieldWarning("IP", "", devices)).toBe("");
    expect(fieldWarning("IP", "not-an-ip", devices)).toBe("");
  });
});
