# acfundanmu v1.7.0 发行说明

## 性能优化（本版重点：继续提升弹幕处理速度）

延续「提升弹幕处理速度」的目标，v1.5.0 / v1.6.0 已把**评论**热路径压到下限（约 6927 ns/op、151 allocs/op），但实时直播流中同样高频的**点赞、进房、关注**三类信号此前仍走标准 `proto.Unmarshal`，每条弹幕都要 `reflect.New` 出 `ZtLiveUserInfo` 及其 `UserIdentity` / `Avatar` 等嵌套消息——这与评论热路径是同构的分配与 CPU 来源，是 v1.6.0 之后弹幕热路径上**最大的一块尚未优化的开销**。v1.7.0 补齐了这一缺口。

- **点赞 / 进房 / 关注热路径零嵌套分配轻量解析**：新增 `parseSimpleUserSignalFast`（见 `fastdecode.go`），仅提取 `SendTime`（field 2，varint）与 `UserInfo`（field 1，消息，复用 v1.5.0 的 `parseUserInfoFast`），**不构造任何嵌套 protobuf 消息**，从而消除标准 `proto.Unmarshal` 为每条弹幕反射创建 `ZtLiveUserInfo` 及其子消息的分配与 CPU 开销（与评论快路径同构）。
- **自动回退保证行为正确**：快速路径遇到任何非预期/截断的编码一律返回 `ok=false`，由 `handleActionItem` 回退到标准 `proto.Unmarshal`；`fastdecode_test.go` 中快速路径与标准路径**逐项比对一致**，并覆盖带徽章、带头像、带管理员类型、无 userInfo、附加未知字段等情形。
- **共用与去冗余**：快速路径与标准回退路径在 `handleActionItem` 中复用同一份构造逻辑（与 v1.6.0 的评论处理方式一致），评论热路径、事件分发快照去冗余、state/notify 对象池归还等既有优化保持不变。

### 基准对比（同机 AMD EPYC 9K65，GOMAXPROCS=5）

| 信号类型 | v1.6.0 | v1.7.0 | 变动（耗时 / 内存 / 分配） |
| --- | --- | --- | --- |
| 评论（HandleActionSignal） | ~8600 ns/op · 8426 B/op · 151 allocs | ~6927 ns/op · 8425 B/op · 151 allocs | **持平（已近下限，无回归）** |
| 点赞（Like） | ~11876 ns/op · 12429 B/op · 151 allocs | ~5539 ns/op · 5225 B/op · 101 allocs | **约 -53% / -58% / -33%** |
| 进房（EnterRoom） | ~11569 ns/op · 12429 B/op · 151 allocs | ~5513 ns/op · 5225 B/op · 101 allocs | **约 -52% / -58% / -33%** |
| 关注（Follow） | ~11611 ns/op · 12429 B/op · 151 allocs | ~5556 ns/op · 5225 B/op · 101 allocs | **约 -52% / -58% / -33%** |
| 混合流（评论+点赞+进房+关注） | ~12173 ns/op · 10193 B/op · 151 allocs | ~7466 ns/op · 5865 B/op · 121 allocs | **约 -39% / -42% / -20%** |

> 评论热路径（BenchmarkHandleActionSignal）保持不变，无回归。
> 其余基准（`CallEvent_Single` / `GzipDecode_New` / `HandleStateSignal` / `HandleNotifySignal` / `GzipDecode_Old`）与 v1.6.0 持平，无回归。
> 剩余约 101 allocs/批次（50 条点赞）中，每条弹幕的 `*Like` 结构体、Nickname 字符串均为业务必需的独立拷贝，已接近热路径下限。

### 安全性说明

`google.golang.org/protobuf` 在 `Unmarshal` 时会对 `string` 字段做拷贝；快速路径同样通过 `string(b[a:b])` 显式拷贝；外层解析仅传递 `Payload` 的字节视图（子切片），由内层解析再显式拷贝出字符串，已派发的弹幕 `DanmuMessage` 持有独立拷贝，池化与复用均安全（见 `alias_test.go: TestProtoStringAliasing`）。`parseSimpleUserSignalFast` 复用 `parseUserInfoFast`，其别名安全性已由 `TestParseCommentFastMatchesStandard` 等既有测试覆盖。

## 其他

- 新增 `TestParseSimpleUserSignalMatchesStandard` / `TestParseSimpleUserSignalFallback`：验证点赞/进房/关注三类信号的快速解析路径与标准路径产出一致，且对截断/非法输入安全回退；新增 `BenchmarkHandleActionSignal_Like` / `_EnterRoom` / `_Follow` / `_Mixed` 基准。
- `go vet`、`go test -race`、`go test` 均通过。
- 更新 `README.md` 性能说明与 `bench_result.txt`。
