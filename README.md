# acfundanmu

[![PkgGoDev](https://pkg.go.dev/badge/github.com/orzogc/acfundanmu)](https://pkg.go.dev/github.com/orzogc/acfundanmu)

AcFun 直播 API，弹幕实现参照 [AcFunDanmaku](https://github.com/wpscott/AcFunDanmaku/tree/master/AcFunDanmu)

### 示例代码

#### 获取弹幕（非事件响应模式）

```go
// uid 为主播的 uid
ac, err := acfundanmu.NewAcFunLive(acfundanmu.SetLiverUID(uid))
if err != nil {
    log.Panicln(err)
}
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
ch := ac.StartDanmu(ctx, false)
for {
    if danmu := ac.GetDanmu(); danmu != nil {
        for _, d := range danmu {
            switch d := d.(type) {
            case *acfundanmu.Comment:
                log.Printf("%s（%d）：%s\n", d.Nickname, d.UserID, d.Content)
            case *acfundanmu.Like:
                log.Printf("%s（%d）点赞\n", d.Nickname, d.UserID)
            case *acfundanmu.EnterRoom:
                log.Printf("%s（%d）进入直播间\n", d.Nickname, d.UserID)
            case *acfundanmu.FollowAuthor:
                log.Printf("%s（%d）关注了主播\n", d.Nickname, d.UserID)
            case *acfundanmu.ThrowBanana:
                log.Printf("%s（%d）送出香蕉 * %d\n", d.Nickname, d.UserID, d.BananaCount)
            case *acfundanmu.Gift:
                log.Printf("%s（%d）送出礼物 %s * %d，连击数：%d\n", d.Nickname, d.UserID, d.GiftName, d.Count, d.Combo)
            case *acfundanmu.RichText:
                for _, r := range d.Segments {
                    switch r := r.(type) {
                    case *acfundanmu.RichTextUserInfo:
                        log.Printf("富文本用户信息：%+v\n", *r)
                    case *acfundanmu.RichTextPlain:
                        log.Printf("富文本文字：%s，颜色：%s\n", r.Text, r.Color)
                    case *acfundanmu.RichTextImage:
                        for _, image := range r.Pictures {
                            log.Printf("富文本图片：%s\n", image)
                        }
                        log.Printf("富文本图片另外的文字：%s，颜色：%s\n", r.AlternativeText, r.AlternativeColor)
                    }
                }
            case *acfundanmu.JoinClub:
                log.Printf("%s（%d）加入主播%s（%d）的守护团", d.FansInfo.Nickname, d.FansInfo.UserID, d.UperInfo.Nickname, d.UperInfo.UserID)
            }
            case *acfundanmu.ShareLive:
                log.Printf("%s（%d）分享直播间到 %d %s", d.Nickname, d.UserID, d.SharePlatform, d.SharePlatformIcon)
        }
    } else {
        if err = <-ch; err != nil {
            log.Panicln(err)
        } else {
            log.Println("直播结束")
        }
        break
    }
}
```

#### 采用事件响应模式

```go
// uid 为主播的 uid
ac, err := acfundanmu.NewAcFunLive(acfundanmu.SetLiverUID(uid))
if err != nil {
    log.Panicln(err)
}
ac.OnDanmuStop(func(ac *acfundanmu.AcFunLive, err error) {
    if err != nil {
        log.Println(err)
    } else {
        log.Println("直播结束")
    }
})
ac.OnComment(func(ac *acfundanmu.AcFunLive, d *acfundanmu.Comment) {
    log.Printf("%s（%d）：%s\n", d.Nickname, d.UserID, d.Content)
})
ac.OnLike(func(ac *acfundanmu.AcFunLive, d *acfundanmu.Like) {
    log.Printf("%s（%d）点赞\n", d.Nickname, d.UserID)
})
ac.OnEnterRoom(func(ac *acfundanmu.AcFunLive, d *acfundanmu.EnterRoom) {
    log.Printf("%s（%d）进入直播间\n", d.Nickname, d.UserID)
})
ac.OnFollowAuthor(func(ac *acfundanmu.AcFunLive, d *acfundanmu.FollowAuthor) {
    log.Printf("%s（%d）关注了主播\n", d.Nickname, d.UserID)
})
ac.OnThrowBanana(func(ac *acfundanmu.AcFunLive, d *acfundanmu.ThrowBanana) {
    log.Printf("%s（%d）送出香蕉 * %d\n", d.Nickname, d.UserID, d.BananaCount)
})
ac.OnGift(func(ac *acfundanmu.AcFunLive, d *acfundanmu.Gift) {
    log.Printf("%s（%d）送出礼物 %s * %d，连击数：%d\n", d.Nickname, d.UserID, d.GiftName, d.Count, d.Combo)
})
ac.OnJoinClub(func(ac *acfundanmu.AcFunLive, d *acfundanmu.JoinClub) {
    log.Printf("%s（%d）加入主播%s（%d）的守护团", d.FansInfo.Nickname, d.FansInfo.UserID, d.UperInfo.Nickname, d.UperInfo.UserID)
})
ac.OnShareLive(func(ac *acfundanmu.AcFunLive, d *acfundanmu.ShareLive) {
    log.Printf("%s（%d）分享直播间到 %d %s", d.Nickname, d.UserID, d.SharePlatform, d.SharePlatformIcon)
})
ac.OnLiveState(func(ac *acfundanmu.AcFunLive, s *acfundanmu.LiveState) {
    log.Printf("直播状态变化：a=%d b=%d c=%s", s.A, s.B, s.C)
})
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
_ = ac.StartDanmu(ctx, true)
// 做其他事情
```

#### 获取直播间状态信息（非事件模式）

```go
// uid 为主播的 uid
ac, err := acfundanmu.NewAcFunLive(acfundanmu.SetLiverUID(uid))
if err != nil {
    log.Panicln(err)
}
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
ch := ac.StartDanmu(ctx, false)
for {
    select {
    case <-ctx.Done():
        return
    default:
        // 循环获取 info 并处理
        time.Sleep(5 * time.Second)
        info := ac.GetLiveInfo()
        log.Printf("%+v\n", info)
    }
}
if err = <-ch; err != nil {
    log.Panicln(err)
} else {
    log.Println("直播结束")
}
```

#### 获取直播间排名前 50 的在线观众信息列表

```go
// uid 为主播的 uid
ac, err := acfundanmu.NewAcFunLive(acfundanmu.SetLiverUID(uid))
if err != nil {
    log.Panicln(err)
}
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
liveID := ac.GetLiveID()
go func() {
    for {
        select {
        case <-ctx.Done():
            return
        default:
            // 循环获取 watchingList 并处理
            watchingList, err := ac.GetWatchingList(liveID)
            if err != nil {
                log.Panicln(err)
            }
            log.Printf("%+v\n", *watchingList)
            time.Sleep(30 * time.Second)
        }
    }
}()
// 做其他事情
```

#### 将弹幕转换成 ass 字幕文件

```go
// uid 为主播的 uid
ac, err := acfundanmu.NewAcFunLive(acfundanmu.SetLiverUID(uid))
if err != nil {
    log.Panicln(err)
}
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
ch := ac.StartDanmu(ctx, false)
ac.WriteASS(ctx, acfundanmu.SubConfig{
    Title:     "foo",
    PlayResX:  1280, // 直播录播视频的分辨率
    PlayResY:  720,
    FontSize:  40,
    StartTime: time.Now().UnixNano()}, // 这里应该是开始录播的时间
    "foo.ass", true)
if err = <-ch; err != nil {
    log.Panicln(err)
} else {
    log.Println("直播结束")
}
```

#### 性能

本仓库在弹幕热路径上做了多项性能优化，单条弹幕的处理耗时与内存分配显著下降：

- **复用 gzip.Reader**：压缩弹幕解压改用 `sync.Pool` 复用 `gzip.Reader`，避免每条消息重新分配，解压分配内存约为原来的 1/3、速度约提升至 1.6 倍。
- **避免逐条消息新建 goroutine**：原先每收到一条 `DownstreamPayload` 都会 `go` 一个新协程处理，高弹幕量下产生大量短生命周期协程、调度与 GC 开销；改为顺序处理，既消除协程抖动，也保证了握手 / 注册 / 心跳等写操作的先后顺序。
- **无反射排序**：弹幕按发送时间排序由 `sort.Slice`（基于反射）改为 `slices.SortFunc` + `slices.IsSortedFunc`，上游通常已按时间有序时可跳过排序，单批解析耗时下降约 28%。
- **复用 gzip 输出缓冲（bytebufferpool）+ bytes.Reader 池**：解压结果复用 `bytebufferpool` 的缓冲、读取器复用 `sync.Pool` 中的 `bytes.Reader`，解压路径几乎零分配（分配从约 8 次 / 17KB 降到 **0 次 / 0B**），单条压缩弹幕解压耗时较上一版再降约 **3.2 倍**（约 14µs → 4.4µs）。
- **事件分发无锁化**：事件模式下，原先每条弹幕分发都要对 handler 列表加一次 `RWMutex` 读写锁（pprof 显示其占 `HandleActionSignal` 总开销近四成）；现改为注册时写时复制（copy-on-write）生成 handler 列表的不可变快照，热路径通过 `atomic.Pointer` 免锁读取。单 handler 仍同步调用（无协程抖动、提供天然背压），多 handler 仍并发派发。`BenchmarkCallEvent_Single` 由约 16.3ns 降至约 8.8ns（约 -46%）。
- **Notify 信号复用对象池**：`handleNotifySignal` 此前未使用已声明的 `notifySignalPool` 等对象池、每条 notify 都新建 protobuf 消息；现已与 action / state 信号一致地池化，降低 notify 路径的分配与 GC 压力。
- **徽章解析去闭包**：`newUserInfo` 解析守护徽章时改用按字段直接取值，避免 `Visit` 闭包在每条带徽章弹幕上的堆分配。
- **弹幕切片预分配**：`handleActionSignal` 按 item 数量预分配弹幕切片，减少 `append` 过程中的多次扩容与再分配。

可用基准测试复现（需 Go 1.21+）：

```sh
go test -bench=. -benchmem
```

典型基准结果（AMD EPYC，单位 ns/op）：

| 基准 | 本次版本（v1.4.0） | 对照（v1.3.0） | 说明 |
| --- | --- | --- | --- |
| BenchmarkGzipDecode_New | 4275 ns/op · 0 B/op · 0 allocs/op | ~4462 ns/op | 复用输出缓冲 + Reader 池，零分配 |
| BenchmarkGzipDecode_Old（对照） | 28649 ns/op · 58640 B/op · 13 allocs/op | 同 | 每条消息新建 gzip.Reader |
| BenchmarkHandleActionSignal | 27019 ns/op · 26664 B/op · 408 allocs/op | 33450 ns/op · 26664 B/op · 408 allocs/op | 解析 + 排序，主成本在 protobuf 反序列化（字符串拷贝不可避免）；已去除每条弹幕的锁开销 |
| BenchmarkCallEvent_Single | 8.77 ns/op · 0 B/op · 0 allocs/op | 16.34 ns/op | 单 handler 同步分发，免锁读取快照，约 -46% |
