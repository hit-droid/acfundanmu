# acfundanmu v1.6.0 发行说明

## 性能优化（本版重点：继续提升弹幕处理速度）

延续「提升弹幕处理速度」的目标，v1.6.0 在 v1.5.0 的基础上，进一步削掉了弹幕热路径上**最大的一块分配与 CPU 来源**——对整个 `ZtLiveScActionSignal` 的标准 `proto.Unmarshal`：

- **外层 action signal 零分配轻量解析**：标准 `proto.Unmarshal` 反序列化整个 `ZtLiveScActionSignal` 时，会 `reflect.New` 出每条 item 的 `ZtLiveActionSignalItem`，并为其 `Payload` 拷贝出 `[][]byte`（pprof 显示该部分占 `HandleActionSignal` 分配量的过半、且是主要 CPU 来源）。v1.6.0 新增 `parseActionSignalItems` / `parseActionItem`（见 `fastdecode.go`），仅提取每条 item 的 `SignalType`（field 1）与各 `Payload`（field 2，repeated bytes）元素，**不构造任何 protobuf 消息、也不对 `Payload` 做字节拷贝**（直接以子切片视图传入内层解析）。
- **自动回退保证行为正确**：快速路径遇到任何非预期/截断的编码一律返回 `ok=false`，由 `handleActionSignal` 回退到标准 `proto.Unmarshal`；`fastdecode_test.go` 中快速路径与标准路径**逐项比对一致**，并覆盖字段乱序（payload 在 signalType 之前）、单 item 多 payload、截断输入等情形。
- **解析逻辑共用与去冗余**：原 `handleActionSignal` 内联的逐 signalType 分支被抽取为 `handleActionItem`，快速路径与标准回退路径共用同一份解析逻辑，避免重复与漂移；排序、事件分发快照去冗余、state/notify 对象池归还等 v1.5.0 优化保持不变。

### 基准对比（同机 AMD EPYC 9K65，GOMAXPROCS=5）

| 版本 | HandleActionSignal 耗时 | 内存 | 分配次数 |
| --- | --- | --- | --- |
| v1.5.0 | ~19300 ns/op | 19454 B/op | 358 allocs/op |
| v1.6.0 | ~8600 ns/op | 8426 B/op | 151 allocs/op |
| 变动 | **约 -55%** | **约 -57%** | **约 -58%**（每批次少约 207 次分配） |

> 其余基准（`CallEvent_Single` / `GzipDecode_New` / `HandleStateSignal` / `HandleNotifySignal` / `GzipDecode_Old`）与 v1.5.0 持平，无回归。
> 剩余约 151 allocs/批次（50 条评论）中，每条评论的 `*Comment` 结构体、Content 字符串、Nickname 字符串均为业务必需的独立拷贝，已接近热路径下限。

### 安全性说明

`google.golang.org/protobuf` 在 `Unmarshal` 时会对 `string` 字段做拷贝；快速路径同样通过 `string(b[a:b])` 显式拷贝；外层解析仅传递 `Payload` 的字节视图（子切片），由内层解析再显式拷贝出字符串，已派发的弹幕 `DanmuMessage` 持有独立拷贝，池化与复用均安全（见 `alias_test.go: TestProtoStringAliasing`）。

## 其他

- 新增 `TestParseActionSignalItems` / `TestParseActionSignalItemsFallback`：验证快速解析路径与标准路径产出一致，且对截断/非法/乱序输入安全回退。
- `go vet`、`go test -race`、`go test` 均通过。
- 更新 `README.md` 性能说明与 `bench_result.txt`。
