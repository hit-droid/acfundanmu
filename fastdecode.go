package acfundanmu

import (
	"log"
)

// 以下为弹幕热路径的轻量 protobuf 解析：仅提取业务需要的字段，避免标准 proto.Unmarshal
// 在每条弹幕上反复 reflect.New 嵌套消息（ZtLiveUserInfo / ZtLiveUserIdentity / ImageCdnNode）
// 带来的堆分配与 GC 压力（pprof 显示该部分占 HandleActionSignal 分配量的近四成）。
// 任何无法识别的编码一律返回 ok=false，由调用方回退到标准 proto.Unmarshal，保证行为正确。

// readVarint 以 protobuf 基线变长编码读取一个 uint64，返回值与消耗的字节数；失败返回 (0, 0)。
func readVarint(b []byte) (uint64, int) {
	var x uint64
	var s uint
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c < 0x80 {
			if i >= 10 || (i == 9 && c > 1) {
				return 0, 0
			}
			return x | uint64(c)<<s, i + 1
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0
}

// skipField 按 wire type 跳过某个字段的值，返回剩余字节；无法识别返回 (nil, false)。
func skipField(b []byte, wireType uint64) ([]byte, bool) {
	switch wireType {
	case 0: // varint
		_, n := readVarint(b)
		if n <= 0 || n > len(b) {
			return nil, false
		}
		return b[n:], true
	case 1: // 64-bit
		if len(b) < 8 {
			return nil, false
		}
		return b[8:], true
	case 2: // length-delimited
		v, n := readVarint(b)
		if n <= 0 || uint64(len(b)-n) < v {
			return nil, false
		}
		return b[n+int(v):], true
	case 5: // 32-bit
		if len(b) < 4 {
			return nil, false
		}
		return b[4:], true
	default:
		return nil, false
	}
}

// parseMedal 解析守护徽章 json 字符串为 MedalInfo；空串或解析失败返回零值。
func parseMedal(badge string) MedalInfo {
	if badge == "" {
		return MedalInfo{}
	}
	p := medalParserPool.Get()
	v, err := p.Parse(badge)
	medalParserPool.Put(p)
	if err != nil {
		log.Printf("分析守护徽章的json数据出现错误：%v", err)
		return MedalInfo{}
	}
	m := MedalInfo{}
	if o := v.GetObject("medalInfo"); o != nil {
		if val := o.Get("uperId"); val != nil {
			m.UperID = val.GetInt64()
		}
		if val := o.Get("userId"); val != nil {
			m.UserID = val.GetInt64()
		}
		if val := o.Get("clubName"); val != nil {
			m.ClubName = string(val.GetStringBytes())
		}
		if val := o.Get("level"); val != nil {
			m.Level = val.GetInt()
		}
	}
	return m
}

// parseCommentFast 以零嵌套分配解析 CommonActionSignalComment 热路径所需字段。
// 返回 (*Comment, true) 表示成功；任何异常返回 (nil, false)，调用方应回退到标准解析。
func parseCommentFast(pl []byte) (*Comment, bool) {
	c := &Comment{}
	b := pl
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n <= 0 {
			return nil, false
		}
		b = b[n:]
		field := tag >> 3
		wire := tag & 7
		switch {
		case field == 1 && wire == 2: // content
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return nil, false
			}
			c.Content = string(b[n2 : n2+int(v)])
			b = b[n2+int(v):]
		case field == 2 && wire == 0: // sendTimeMs
			v, n2 := readVarint(b)
			if n2 <= 0 {
				return nil, false
			}
			c.SendTime = int64(v)
			b = b[n2:]
		case field == 3 && wire == 2: // userInfo
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return nil, false
			}
			sub := b[n2 : n2+int(v)]
			b = b[n2+int(v):]
			ui, ok := parseUserInfoFast(sub)
			if !ok {
				return nil, false
			}
			c.UserInfo = ui
		default:
			rest, ok := skipField(b, wire)
			if !ok {
				return nil, false
			}
			b = rest
		}
	}
	return c, true
}

// parseUserInfoFast 解析 ZtLiveUserInfo 的所需字段（userId / nickname / avatar[0].url /
// badge / userIdentity.managerType），不构造任何嵌套 protobuf 消息。
func parseUserInfoFast(b []byte) (UserInfo, bool) {
	var ui UserInfo
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n <= 0 {
			return ui, false
		}
		b = b[n:]
		field := tag >> 3
		wire := tag & 7
		switch {
		case field == 1 && wire == 0: // userId
			v, n2 := readVarint(b)
			if n2 <= 0 {
				return ui, false
			}
			ui.UserID = int64(v)
			b = b[n2:]
		case field == 2 && wire == 2: // nickname
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return ui, false
			}
			ui.Nickname = string(b[n2 : n2+int(v)])
			b = b[n2+int(v):]
		case field == 3 && wire == 2: // avatar（repeated ImageCdnNode），仅取首个 url
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return ui, false
			}
			node := b[n2 : n2+int(v)]
			b = b[n2+int(v):]
			if ui.Avatar == "" {
				if url, ok := firstImageURL(node); !ok {
					return ui, false
				} else {
					ui.Avatar = url
				}
			}
		case field == 4 && wire == 2: // badge（json 字符串）
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return ui, false
			}
			ui.Medal = parseMedal(string(b[n2 : n2+int(v)]))
			b = b[n2+int(v):]
		case field == 5 && wire == 2: // userIdentity
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return ui, false
			}
			sub := b[n2 : n2+int(v)]
			b = b[n2+int(v):]
			mt, ok := parseManagerType(sub)
			if !ok {
				return ui, false
			}
			ui.ManagerType = mt
		default:
			rest, ok := skipField(b, wire)
			if !ok {
				return ui, false
			}
			b = rest
		}
	}
	return ui, true
}

// firstImageURL 提取 ImageCdnNode 的 url 字段（field 2）。
func firstImageURL(b []byte) (string, bool) {
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n <= 0 {
			return "", false
		}
		b = b[n:]
		field := tag >> 3
		wire := tag & 7
		if field == 2 && wire == 2 {
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return "", false
			}
			return string(b[n2 : n2+int(v)]), true
		}
		rest, ok := skipField(b, wire)
		if !ok {
			return "", false
		}
		b = rest
	}
	return "", true
}

// parseManagerType 提取 ZtLiveUserIdentity 的 managerType 字段（field 1）。
func parseManagerType(b []byte) (ManagerType, bool) {
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n <= 0 {
			return 0, false
		}
		b = b[n:]
		field := tag >> 3
		wire := tag & 7
		if field == 1 && wire == 0 {
			v, n2 := readVarint(b)
			if n2 <= 0 {
				return 0, false
			}
			return ManagerType(v), true
		}
		rest, ok := skipField(b, wire)
		if !ok {
			return 0, false
		}
		b = rest
	}
	return 0, true
}

// parseActionSignalItems 零分配遍历 ZtLiveScActionSignal 的 item，对每个 Payload 元素调用 fn。
// 仅提取 SignalType（field 1）与各 Payload（field 2，repeated bytes）元素，不构造任何 protobuf 消息，
// 从而消除标准 proto.Unmarshal 为每条 item 反射创建 ZtLiveActionSignalItem 及其 Payload [][]byte 拷贝的
// 分配与 CPU 开销（pprof 显示该部分占 HandleActionSignal 分配量的过半）。
// 遇到任何非预期/截断的编码一律返回 false，由调用方回退到标准 proto.Unmarshal，保证行为正确。
func parseActionSignalItems(b []byte, fn func(sigType, pl []byte)) bool {
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n <= 0 {
			return false
		}
		b = b[n:]
		field := tag >> 3
		wire := tag & 7
		if field == 1 && wire == 2 { // item（repeated ZtLiveActionSignalItem，field 1）
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return false
			}
			item := b[n2 : n2+int(v)]
			b = b[n2+int(v):]
			if !parseActionItem(item, fn) {
				return false
			}
		} else {
			rest, ok := skipField(b, wire)
			if !ok {
				return false
			}
			b = rest
		}
	}
	return true
}

// parseActionItem 解析单个 ZtLiveActionSignalItem：提取 SignalType（field 1）与每个
// Payload（field 2，repeated bytes），对每个 Payload 元素调用 fn(sigType, pl)。
// 为兼容字段乱序（极少见），先用栈上缓冲暂存 Payload 元素，待 SignalType 确定后再派发，
// 既避免额外堆分配，又保证乱序编码下不丢失弹幕。
func parseActionItem(b []byte, fn func(sigType, pl []byte)) bool {
	var sigType []byte
	var payloads [4][]byte
	np := 0
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n <= 0 {
			return false
		}
		b = b[n:]
		field := tag >> 3
		wire := tag & 7
		switch {
		case field == 1 && wire == 2: // signalType
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return false
			}
			sigType = b[n2 : n2+int(v)]
			b = b[n2+int(v):]
		case field == 2 && wire == 2: // payload（repeated bytes）
			if np >= len(payloads) {
				return false // 单 item 超过 4 个 payload 视为非预期编码，回退标准解析
			}
			v, n2 := readVarint(b)
			if n2 <= 0 || uint64(len(b)-n2) < v {
				return false
			}
			payloads[np] = b[n2 : n2+int(v)]
			np++
			b = b[n2+int(v):]
		default:
			rest, ok := skipField(b, wire)
			if !ok {
				return false
			}
			b = rest
		}
	}
	for i := 0; i < np; i++ {
		fn(sigType, payloads[i])
	}
	return true
}
