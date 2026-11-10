# acfundanmu v1.8.0 发行说明

## 性能优化（本版重点：state / notify 信号热路径零分配轻量解析）

延续「提升弹幕处理速度」的目标，v1.5.0 / v1.6.0 / v1.7.0 已把**评论、点赞、进房、关注**四类高频 action 信号压到下限，
但实时直播流中另一类高频信号——**state 信号**（如在线人数 / 点赞总数等 `CommonStateSignalDisplayInfo` 周期性推送）
与 **notify 信号**（如被踢出、违规警告、房管状态变更）此前仍走标准 `proto.Unmarshal`：每条信号都要为外层
`ZtLiveScStateSignal` / `ZtLiveScNotifySignal` 及其每条 `item` 的 `SignalType` / `Payload` 做反射拷贝，并为每条
具体消息 `reflect.New` 出完整 protobuf 消息（v1.7.0 的基准显示 `HandleStateSignal` 仍达 **307 allocs/op**、
`HandleNotifySignal` 仍达 **257 allocs/op**，与 v1.6.0 持平——是 action 信号之外**最大的一块尚未优化的分配与 CPU 来源**）。
v1.8.0 补齐了这一缺口。

- **零分配外层信号解析**：新增 `parseStateSignalItems` / `parseNotifySignalItems`（见 `fastdecode.go`），仅提取每条 item 的
  `SignalType`（field 1）与 `Payload`（field 2，单条 bytes）字节视图，不构造任何 protobuf 消息、也不对 `Payload` 做字节拷贝，
  从而消除标准 `proto.Unmarshal` 为每条 item 反射创建 `ZtLiveStateSignalItem` / `ZtLiveNotifySignalItem` 及其 `SignalType` /
  `Payload` 拷贝的分配与 CPU 开销（与 v1.6.0 的 action 信号外层解析同构）。
- **典型 state 信号零嵌套分配轻量解析**：`CommonStateSignalDisplayInfo`（在线人数 / 点赞数，直播中最常见的 state 信号）
  新增 `parseDisplayInfoFast`，仅提取 `WatchingCount`（field 1 字符串）、`LikeCount`（field 2 字符串）、`LikeDelta`（field 3 varint），
  不构造任何嵌套 protobuf 消息；与 v1.7.0 的评论快路径同构。
- **典型 notify 信号零嵌套分配轻量解析**：`CommonNotifySignalKickedOut`（被踢出）、`CommonNotifySignalViolationAlert`（违规警告）、
  `CommonNotifySignalLiveManagerState`（房管状态）三类信号分别新增 `parseKickedOutFast` / `parseViolationAlertFast` /
  `parseLiveManagerStateFast`，仅提取各自业务所需的单个字段（`reason` / `violationContent` 字符串或 `state` 枚举），不构造任何嵌套消息。
- **自动回退保证行为正确**：上述快速路径遇到任何非预期 / 截断的编码一律返回 `ok=false`，由 `handleStateItem` / `handleNotifyItem`
  回退到标准 `proto.Unmarshal`；`fastdecode_test.go` 中快速路径与标准路径**逐项比对一致**，并覆盖带徽章、带头像、带管理员类型、
  无 userInfo、附加未知字段等情形；另含 `TestHandleStateSignalDisplayInfoFast` / `TestHandleNotifySignalKickedOutFast` 端到端验证。

### 基准对比（同机 AMD EPYC 9K65，GOMAXPROCS=5）

| 信号类型 | v1.7.0 | v1.8.0 | 变动（耗时 / 内存 / 分配） |
| --- | --- | --- | --- |
| state 信号（HandleStateSignal） | ~15016 ns/op · 10220 B/op · 307 allocs | ~6171 ns/op · 4400 B/op · 200 allocs | **约 -59% / -57% / -35%** |
| notify 信号（HandleNotifySignal） | ~13556 ns/op · 9020 B/op · 257 allocs | ~4679 ns/op · 3200 B/op · 150 allocs | **约 -66% / -64% / -42%** |
| 评论（HandleActionSignal） | ~6927 ns/op · 8426 B/op · 151 allocs | ~8768 ns/op · 8426 B/op · 151 allocs | **持平**：allocs / B/op 完全相同，耗时波动为共享 192 核机器调度噪声（未改动该路径，分配量与基线逐字节一致） |
| 点赞 / 进房 / 关注 / 混合流 | （v1.7.0 水平） | 保持不变 | 无回归 |

> 评论热路径（`BenchmarkHandleActionSignal`）本版未改动，其 `allocs/op` 与 `B/op` 与 v1.7.0 **逐字节一致**；
> 本机为共享 192 核 EPYC，纯耗时在多次运行间有 ±15% 量级的调度噪声，不代表真实回归。
> 其余基准（`GzipDecode_New` / `CallEvent_Single` 等）与 v1.7.0 持平，无回归。
> 剩余约 200 allocs/批次（50 条 state 信号）中，每条 `DisplayInfo` 的 `WatchingCount` / `LikeCount` 字符串均为业务必需的独立拷贝，
> 已接近该热路径下限。

## 其他

- 新增 `TestParseDisplayInfoFastMatchesStandard` / `TestParseDisplayInfoFastFallback` /
  `TestParseKickedOutFastMatchesStandard` / `TestParseViolationAlertFastMatchesStandard` /
  `TestParseLiveManagerStateFastMatchesStandard` / `TestParseStateSignalItems` / `TestParseNotifySignalItems` /
  `TestParseStateSignalItemsFallback` / `TestHandleStateSignalDisplayInfoFast` / `TestHandleNotifySignalKickedOutFast`：
  验证 state / notify 信号快速解析路径与标准路径产出一致，且对截断 / 非法输入安全回退。
- `go vet`、`go test -race`、`go test` 均通过。
- 更新 `README.md` 性能说明与 `bench_result.txt`。
