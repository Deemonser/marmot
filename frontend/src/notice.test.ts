import { test } from "node:test";
import assert from "node:assert/strict";
import { noticeTone, noticeDuration } from "./notice.ts";

test("confirmations are info", () => {
  for (const text of ["已删除，空间已释放", "Quick Look 已打开", "已在 Finder 中定位", "已连接 DeepSeek", "已加入 3 项 · 1.2 GB", "已断开 AI，仅使用本机规则。"]) {
    assert.equal(noticeTone(text), "info", text);
  }
});

test("refusals and failures are errors", () => {
  for (const text of [
    "分析失败：timeout", "无法收集该对象：x", "该对象不能预览。", "聚合对象和受限对象不能加入收集区。",
    "收集区只接受扫描结果中的对象，不接受从 Finder 拖入的路径", "校验未通过", "执行前校验失败，已中止。",
    "请先完成一次扫描。", "对象已变化，已停用文件操作。", "没有可加入的项。",
  ]) {
    assert.equal(noticeTone(text), "error", text);
  }
});

test("an error stays twice as long as a confirmation", () => {
  assert.equal(noticeDuration("error"), 2 * noticeDuration("info"));
});
