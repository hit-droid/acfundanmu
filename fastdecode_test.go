package acfundanmu

import (
	"testing"

	"github.com/orzogc/acfundanmu/acproto"
	"google.golang.org/protobuf/proto"
)

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
