import { describe, expect, it } from "vitest";
import { identityText } from "./format";

describe("identityText", () => {
  it("正式节点与凭据节点各自有文案", () => {
    expect(identityText("formal")).toBe("正式节点");
    expect(identityText("credential")).toBe("凭据节点");
  });

  it("空串表示配置解析失败，不能显示成凭据节点", () => {
    expect(identityText("")).toBe("配置无法解析");
  });
});
