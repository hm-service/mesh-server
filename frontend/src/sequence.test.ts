// sequence.ts 是控制台里两种代次的唯一实现：会话代次与同一资源的请求代次。
// 这些用例盯的是「谁可以落地」这个行为，不是计数器的内部形状。
import { describe, expect, it } from "vitest";
import { Sequence } from "./sequence";

describe("Sequence", () => {
  it("每领一次号都不同，且没领过的号算过时", () => {
    const sequence = new Sequence();
    const first = sequence.next();
    const second = sequence.next();
    expect(second).not.toBe(first);
    expect(sequence.stale(first)).toBe(true);
    expect(sequence.stale(second)).toBe(false);
    expect(sequence.stale(0)).toBe(true);
  });

  it("current 不推进代次：还没领号的请求也能拿到当前会话", () => {
    const session = new Sequence();
    const gen = session.next();
    expect(session.current()).toBe(gen);
    expect(session.stale(gen)).toBe(false);
  });

  it("逆序完成时只有最新那次请求落地", () => {
    const session = new Sequence();
    const requests = new Sequence();
    const gen = session.next();
    // 轮询先发、手工刷新后发，后发的先回来。
    const polling = requests.next();
    const refresh = requests.next();
    const applies = (claim: number) => !session.stale(gen) && !requests.stale(claim);
    expect(applies(refresh)).toBe(true);
    // 先发的那次晚到：它的结果属于上一轮，不能覆盖新结果。
    expect(applies(polling)).toBe(false);
  });

  it("换会话之后旧会话里所有在途请求都作废", () => {
    const session = new Sequence();
    const oldGen = session.next();
    const requests = new Sequence();
    const inflight = requests.next();
    expect(!session.stale(oldGen) && !requests.stale(inflight)).toBe(true);

    const newGen = session.next();
    expect(session.stale(oldGen)).toBe(true);
    expect(session.stale(newGen)).toBe(false);
    // 新会话第一次列表请求也是新的一号，旧请求仍旧过时。
    const fresh = requests.next();
    expect(!session.stale(newGen) && !requests.stale(fresh)).toBe(true);
    expect(!session.stale(oldGen) && !requests.stale(inflight)).toBe(false);
  });
});
