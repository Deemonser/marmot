# 原版取样工具

对照 DaisyDisk 逐像素测量用的三个小工具。R-060 与 R-061 的全部数值都出自它们，
留在这里是为了那些结论可以被复现和推翻。

```
swiftc -O winlist.swift -o winlist && ./winlist          # 窗口 ID 与逻辑尺寸
swiftc -O grab.swift    -o grab                          # 抓帧
swiftc -O click.swift   -o click                         # 合成点击
./grab <windowID> <秒数> <输出目录>
./click <x> <y> <bundleID|->                             # "-" 表示不激活应用
```

- `winlist` 读窗口的**逻辑**尺寸；配合 `grab` 抓到的**像素**尺寸就能自证缩放系数。
  R-060 §2 记过一次教训：拿"假定尺寸的界面元素"（标题栏按钮）做定标是错的——
  它实测是 `13pt` 而非 `12pt`，据此我一度错误地否定了一张可用样本。
- `grab` 用 ScreenCaptureKit（`CGWindowListCreateImage` 已被移除），并且**只保存与上一帧不同的帧**，
  所以静止窗口几乎不占空间，可以挂几十秒等操作。两个坑：纯 CLI 进程要先触碰
  `NSApplication.shared`，否则 SCK 触发 `CGS_REQUIRE_INIT`；变化检测必须按 `bytesPerRow`
  取网格，用扁平步长会漂进位图的行填充区。
- `grab` 已限速（自身 CPU 约 `4.7%`）。**这仍不足以测时长**：R-061 §5 的时长是在忙循环版本下、
  在负载 `7.95`（8 核）的机器上测的，全部作废。测时长要空闲机器 ＋ 同一交互重复多次看离散度。
- `click` 的激活是可选的。每次点击都激活会让第一次点击被取焦吃掉，那正是我一度误判
  "合成点击无效"的原因。
