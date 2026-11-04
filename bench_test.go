package acfundanmu

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/orzogc/acfundanmu/acproto"
	"github.com/valyala/bytebufferpool"
	"google.golang.org/protobuf/proto"
)

// rawActionSignal 返回包含 n 条评论的 action signal 原始 protobuf 字节（未压缩）
func rawActionSignal(n int) []byte {
	action := &acproto.ZtLiveScActionSignal{}
	for i := 0; i < n; i++ {
		comment := &acproto.CommonActionSignalComment{
			SendTimeMs: int64(i),
			Content:    "这是一条用于基准测试的弹幕内容",
			UserInfo:   &acproto.ZtLiveUserInfo{UserId: 123456, Nickname: "tester"},
		}
		b, err := proto.Marshal(comment)
		if err != nil {
			panic(err)
		}
		action.Item = append(action.Item, &acproto.ZtLiveActionSignalItem{
			SignalType: "CommonActionSignalComment",
			Payload:    [][]byte{b},
		})
	}
	raw, err := proto.Marshal(action)
	if err != nil {
		panic(err)
	}
	return raw
}

// gzippedActionSignal 返回压缩后的 action signal 字节
func gzippedActionSignal(n int) []byte {
	raw := rawActionSignal(n)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(raw); err != nil {
		panic(err)
	}
	if err := gw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func newBenchAc() *AcFunLive {
	ac := &AcFunLive{}
	ac.handlerMap = new(handlerMap)
	return ac
}

// BenchmarkHandleActionSignal 测量弹幕 action signal 的解析（protobuf 反序列化 + 排序）耗时
func BenchmarkHandleActionSignal(b *testing.B) {
	const n = 50
	payload := rawActionSignal(n)
	ac := newBenchAc()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ac.handleActionSignal(&payload, true)
	}
}

// gzipDecodeOld 旧实现：每条消息都新建 gzip.Reader
func gzipDecodeOld(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// BenchmarkGzipDecode_Old 旧实现（每条消息新建 gzip.Reader）
func BenchmarkGzipDecode_Old(b *testing.B) {
	data := gzippedActionSignal(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gzipDecodeOld(data); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGzipDecode_New 新实现（复用 gzip.Reader 池 + bytebufferpool 输出缓冲 + bytes.Reader 池）
func BenchmarkGzipDecode_New(b *testing.B) {
	data := gzippedActionSignal(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bb, err := decompressGzip(data)
		if err != nil {
			b.Fatal(err)
		}
		bytebufferpool.Put(bb)
	}
}

// BenchmarkCallEvent_Single 测量仅注册单个 handler 时的事件分发开销。
// 优化前每条弹幕都会为此新起一个 goroutine（协程抖动），优化后直接同步调用，
// 这里通过 allocs/op 与 ns/op 的下降佐证该开销被消除。
func BenchmarkCallEvent_Single(b *testing.B) {
	ac := newBenchAc()
	ac.OnComment(func(ac *AcFunLive, d *Comment) {})
	comment := &Comment{
		DanmuCommon: DanmuCommon{
			SendTime: 1,
			UserInfo: UserInfo{UserID: 123, Nickname: "tester"},
		},
		Content: "bench",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ac.callEvent(commentDanmu, comment)
	}
}
