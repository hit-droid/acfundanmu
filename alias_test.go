package acfundanmu

import (
	"testing"

	"github.com/orzogc/acfundanmu/acproto"
	"google.golang.org/protobuf/proto"
)

// TestProtoStringAliasing 验证 proto.Unmarshal 解析出的 string 字段是否
// 与输入缓冲共享底层字节（零拷贝）。若共享，池化内层消息并 Reset 后再复用
// 输入缓冲，会导致已派发弹幕的 string 字段被损坏。
func TestProtoStringAliasing(t *testing.T) {
	orig := "这是一条用于测试弹幕字符串别名的弹幕内容"
	src := &acproto.CommonActionSignalComment{
		SendTimeMs: 123,
		Content:    orig,
		UserInfo:   &acproto.ZtLiveUserInfo{UserId: 42, Nickname: "昵称"},
	}
	raw, err := proto.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}

	// 复制一份可变的输入缓冲（模拟 gzBuf 被复用后写入新数据）
	input := make([]byte, len(raw))
	copy(input, raw)

	msg := new(acproto.CommonActionSignalComment)
	if err := proto.Unmarshal(input, msg); err != nil {
		t.Fatal(err)
	}
	captured := msg.Content // 模拟 d.Content = comment.Content

	// 现在“复用”输入缓冲：把底层字节全部改成 'X'（如同 gzBuf 被新消息覆盖）
	for i := range input {
		input[i] = 'X'
	}

	if captured != orig {
		t.Fatalf("检测到字符串别名：解析出的 Content 与输入缓冲共享底层字节。"+
			"captured=%q 应为 %q", captured, orig)
	}
	t.Logf("OK: proto.Unmarshal 对字符串做了拷贝（无别名），池化内层消息安全。captured=%q", captured)
}
