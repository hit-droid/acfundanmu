package acfundanmu

import (
	"bytes"
	"testing"

	"github.com/orzogc/acfundanmu/acproto"
	"google.golang.org/protobuf/proto"
)

// writeVarint 以 protobuf 基线变长编码写入一个非负整数（测试用，构造手写 wire 字节）。
func writeVarint(buf *bytes.Buffer, n int) {
	v := uint64(n)
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

// buildActionSignalRaw 把若干 item 字节按外层 ZtLiveScActionSignal 的重复 field 1 拼装成完整消息。
func buildActionSignalRaw(items [][]byte) []byte {
	var buf bytes.Buffer
	for _, it := range items {
		buf.WriteByte(0x0a) // field 1, wire 2
		writeVarint(&buf, len(it))
		buf.Write(it)
	}
	return buf.Bytes()
}

// standardCommentDecode 复刻 handleActionSignal 中评论的“标准回退”解析路径，用于与快速路径对照。
func standardCommentDecode(pl []byte) *Comment {
	fc := getProto[*acproto.CommonActionSignalComment](commentMsgPool)
	if err := proto.Unmarshal(pl, fc); err != nil {
		putProto(commentMsgPool, fc)
		return nil
	}
	c := &Comment{
		DanmuCommon: DanmuCommon{
			SendTime: fc.SendTimeMs,
			UserInfo: newUserInfo(fc.UserInfo),
		},
		Content: fc.Content,
	}
	putProto(commentMsgPool, fc)
	return c
}

func commentEqual(a, b *Comment) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Content == b.Content &&
		a.SendTime == b.SendTime &&
		a.UserInfo.UserID == b.UserInfo.UserID &&
		a.UserInfo.Nickname == b.UserInfo.Nickname &&
		a.UserInfo.Avatar == b.UserInfo.Avatar &&
		a.UserInfo.ManagerType == b.UserInfo.ManagerType &&
		a.UserInfo.Medal.UperID == b.UserInfo.Medal.UperID &&
		a.UserInfo.Medal.UserID == b.UserInfo.Medal.UserID &&
		a.UserInfo.Medal.ClubName == b.UserInfo.Medal.ClubName &&
		a.UserInfo.Medal.Level == b.UserInfo.Medal.Level
}

// TestParseCommentFastMatchesStandard 验证快速路径与标准路径产出完全一致的 *Comment。
func TestParseCommentFastMatchesStandard(t *testing.T) {
	badgeJSON := `{"medalInfo":{"uperId":111,"userId":222,"clubName":"守护团","level":5}}`

	cases := []*acproto.CommonActionSignalComment{
		{Content: "普通弹幕", SendTimeMs: 123456, UserInfo: &acproto.ZtLiveUserInfo{UserId: 1001, Nickname: "昵称A"}},
		{Content: "带徽章", SendTimeMs: 7, UserInfo: &acproto.ZtLiveUserInfo{UserId: 2002, Nickname: "昵称B", Badge: badgeJSON, UserIdentity: &acproto.ZtLiveUserIdentity{ManagerType: 1}}},
		{Content: "带头像与徽章", SendTimeMs: 999, UserInfo: &acproto.ZtLiveUserInfo{
			UserId: 3003, Nickname: "昵称C",
			Avatar: []*acproto.ImageCdnNode{{Url: "http://avatar/1.png"}, {Url: "http://avatar/2.png"}},
			Badge:  badgeJSON,
		}},
		{Content: "仅 userId", SendTimeMs: 1, UserInfo: &acproto.ZtLiveUserInfo{UserId: 4004}},
		{Content: "空 userInfo", SendTimeMs: 2}, // UserInfo 字段缺失
	}

	for i, c := range cases {
		raw, err := proto.Marshal(c)
		if err != nil {
			t.Fatalf("case %d marshal error: %v", i, err)
		}
		// 额外追加一个未知字段，验证快速路径的 skip 逻辑
		rawWithUnknown := append([]byte{}, raw...)
		rawWithUnknown = append(rawWithUnknown, 0xAA, 0x06, 0x03, 'x', 'y', 'z') // field 99, wire 2, len 3

		fast, ok := parseCommentFast(rawWithUnknown)
		if !ok {
			t.Fatalf("case %d: parseCommentFast 应成功却返回 ok=false", i)
		}
		std := standardCommentDecode(rawWithUnknown)
		if std == nil {
			t.Fatalf("case %d: 标准解析失败", i)
		}
		if !commentEqual(fast, std) {
			t.Fatalf("case %d: 快速路径与标准路径结果不一致\nfast=%+v\nstd =%+v", i, fast, std)
		}
	}
}

// TestParseCommentFastFallback 验证对截断/非法输入快速路径返回 ok=false，从而触发标准回退。
func TestParseCommentFastFallback(t *testing.T) {
	c := &acproto.CommonActionSignalComment{Content: "x", SendTimeMs: 1, UserInfo: &acproto.ZtLiveUserInfo{UserId: 1}}
	raw, err := proto.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	// 截断到一半，必然非法
	if _, ok := parseCommentFast(raw[:len(raw)/2]); ok {
		t.Fatal("截断输入应返回 ok=false")
	}
	// 完全空输入也应安全返回（ok=false 或空 comment，不 panic）
	if _, ok := parseCommentFast(nil); !ok {
		// 空输入返回 false 是允许的（将回退到标准解析）
		return
	}
}

// TestParseMedal 验证徽章 json 解析正确。
func TestParseMedal(t *testing.T) {
	m := parseMedal(`{"medalInfo":{"uperId":10,"userId":20,"clubName":"测试团","level":3}}`)
	if m.UperID != 10 || m.UserID != 20 || m.ClubName != "测试团" || m.Level != 3 {
		t.Fatalf("parseMedal 结果错误：%+v", m)
	}
	if (parseMedal("")) != (MedalInfo{}) {
		t.Fatal("空徽章应返回零值")
	}
}

// TestParseActionSignalItems 验证零分配外层解析与标准 proto.Unmarshal 产出完全一致的
// (signalType, payload) 序列，覆盖普通顺序、单 item 多 payload、以及字段乱序（payload 在
// signalType 之前）的情况，确保快速路径与标准路径行为一致。
func TestParseActionSignalItems(t *testing.T) {
	c1 := &acproto.CommonActionSignalComment{SendTimeMs: 1, Content: "评论一", UserInfo: &acproto.ZtLiveUserInfo{UserId: 11, Nickname: "甲"}}
	b1, err := proto.Marshal(c1)
	if err != nil {
		t.Fatal(err)
	}
	c2 := &acproto.CommonActionSignalComment{SendTimeMs: 2, Content: "评论二", UserInfo: &acproto.ZtLiveUserInfo{UserId: 22, Nickname: "乙"}}
	b2, err := proto.Marshal(c2)
	if err != nil {
		t.Fatal(err)
	}
	// 普通顺序 item：signalType 在前、payload 在后，且本 item 含两个 payload
	normalItem, err := proto.Marshal(&acproto.ZtLiveActionSignalItem{SignalType: "CommonActionSignalComment", Payload: [][]byte{b1, b2}})
	if err != nil {
		t.Fatal(err)
	}
	// 乱序 item：先写 payload（field 2）再写 signalType（field 1）
	var reorderItem bytes.Buffer
	reorderItem.WriteByte(0x12) // field 2, wire 2
	writeVarint(&reorderItem, len(b1))
	reorderItem.Write(b1)
	st := "CommonActionSignalComment"
	reorderItem.WriteByte(0x0a) // field 1, wire 2
	writeVarint(&reorderItem, len(st))
	reorderItem.WriteString(st)

	// 点赞 item
	l1 := &acproto.CommonActionSignalLike{SendTimeMs: 3, UserInfo: &acproto.ZtLiveUserInfo{UserId: 33}}
	bl, err := proto.Marshal(l1)
	if err != nil {
		t.Fatal(err)
	}
	likeItem, err := proto.Marshal(&acproto.ZtLiveActionSignalItem{SignalType: "CommonActionSignalLike", Payload: [][]byte{bl}})
	if err != nil {
		t.Fatal(err)
	}

	raw := buildActionSignalRaw([][]byte{normalItem, reorderItem.Bytes(), likeItem})

	type pair struct {
		sig string
		pl  []byte
	}
	var fast []pair
	ok := parseActionSignalItems(raw, func(sigType, pl []byte) {
		p := make([]byte, len(pl))
		copy(p, pl)
		fast = append(fast, pair{string(sigType), p})
	})
	if !ok {
		t.Fatal("parseActionSignalItems 应成功却返回 ok=false")
	}

	stdMsg := new(acproto.ZtLiveScActionSignal)
	if err := proto.Unmarshal(raw, stdMsg); err != nil {
		t.Fatalf("标准解析失败: %v", err)
	}
	var std []pair
	for _, item := range stdMsg.Item {
		for _, pl := range item.Payload {
			p := make([]byte, len(pl))
			copy(p, pl)
			std = append(std, pair{item.SignalType, p})
		}
	}

	if len(fast) != len(std) {
		t.Fatalf("快速路径与标准路径数量不一致: fast=%d std=%d", len(fast), len(std))
	}
	for i := range fast {
		if fast[i].sig != std[i].sig || !bytes.Equal(fast[i].pl, std[i].pl) {
			t.Fatalf("第 %d 项不一致: fast=%q,%x  std=%q,%x", i, fast[i].sig, fast[i].pl, std[i].sig, std[i].pl)
		}
	}
}

// TestParseActionSignalItemsFallback 验证截断/非法输入时快速路径返回 ok=false（触发标准回退）。
func TestParseActionSignalItemsFallback(t *testing.T) {
	c1 := &acproto.CommonActionSignalComment{SendTimeMs: 1, Content: "x", UserInfo: &acproto.ZtLiveUserInfo{UserId: 1}}
	b1, _ := proto.Marshal(c1)
	normalItem, _ := proto.Marshal(&acproto.ZtLiveActionSignalItem{SignalType: "CommonActionSignalComment", Payload: [][]byte{b1}})
	raw := buildActionSignalRaw([][]byte{normalItem})
	if ok := parseActionSignalItems(raw[:len(raw)/2], func(_, _ []byte) {}); ok {
		t.Fatal("截断输入应返回 ok=false")
	}
	// 空输入是合法的（无 item），快速路径应成功（ok=true），不会误触发标准回退
	if ok := parseActionSignalItems(nil, func(_, _ []byte) {}); !ok {
		t.Fatal("空输入应返回 ok=true")
	}
}
