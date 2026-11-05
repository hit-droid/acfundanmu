package acfundanmu

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"slices"
	"sync"

	"github.com/orzogc/acfundanmu/acproto"
	"github.com/valyala/bytebufferpool"

	"google.golang.org/protobuf/proto"
)

// gzipReaderPool 复用 gzip.Reader，避免每条压缩弹幕都重新分配，降低 GC 压力
var gzipReaderPool = sync.Pool{
	New: func() any {
		return new(gzip.Reader)
	},
}

// gzipBytesReaderPool 复用 bytes.Reader，避免每次解压都重新分配读取器
var gzipBytesReaderPool = sync.Pool{
	New: func() any {
		return new(bytes.Reader)
	},
}

// getProto 从 sync.Pool 取一个复用的 protobuf 消息对象。
// proto.Unmarshal 在反序列化前会先 Reset 该消息，因此复用外层消息是安全的；
// 反序列化产生的字符串均为 proto 内部拷贝（不引用输入缓冲），提取到弹幕对象后即可归还。
func getProto[T proto.Message](p *sync.Pool) T {
	return p.Get().(T)
}

// putProto 将复用的 protobuf 消息对象 Reset 后归还到池中，及时释放其持有的字符串引用以利于 GC。
func putProto(p *sync.Pool, m proto.Message) {
	proto.Reset(m)
	p.Put(m)
}

// 热路径上复用的 protobuf 消息对象池。每条弹幕都会新建对应的 signal 消息，
// 池化可消除反复分配这些结构体带来的堆分配与 GC 压力。
var (
	// handleCommand 入站消息
	scMessagePool = &sync.Pool{New: func() any { return new(acproto.ZtLiveScMessage) }}

	// handleActionSignal
	actionSignalPool = &sync.Pool{New: func() any { return new(acproto.ZtLiveScActionSignal) }}
	commentMsgPool   = &sync.Pool{New: func() any { return new(acproto.CommonActionSignalComment) }}
	likeMsgPool      = &sync.Pool{New: func() any { return new(acproto.CommonActionSignalLike) }}
	enterRoomMsgPool = &sync.Pool{New: func() any { return new(acproto.CommonActionSignalUserEnterRoom) }}
	followMsgPool    = &sync.Pool{New: func() any { return new(acproto.CommonActionSignalUserFollowAuthor) }}
	bananaMsgPool    = &sync.Pool{New: func() any { return new(acproto.AcfunActionSignalThrowBanana) }}
	giftMsgPool      = &sync.Pool{New: func() any { return new(acproto.CommonActionSignalGift) }}
	richTextMsgPool  = &sync.Pool{New: func() any { return new(acproto.CommonActionSignalRichText) }}
	joinClubMsgPool  = &sync.Pool{New: func() any { return new(acproto.AcfunActionSignalJoinClub) }}
	shareLiveMsgPool = &sync.Pool{New: func() any { return new(acproto.CommonActionSignalUserShareLive) }}

	// handleStateSignal
	stateSignalPool           = &sync.Pool{New: func() any { return new(acproto.ZtLiveScStateSignal) }}
	stateDisplayInfoPool      = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalDisplayInfo) }}
	stateTopUsersPool         = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalTopUsers) }}
	stateRecentCommentPool    = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalRecentComment) }}
	stateRedpackListPool      = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalCurrentRedpackList) }}
	stateChatCallPool         = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalChatCall) }}
	stateChatAcceptPool       = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalChatAccept) }}
	stateChatReadyPool        = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalChatReady) }}
	stateChatEndPool          = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalChatEnd) }}
	stateAuthorChatCallPool   = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalAuthorChatCall) }}
	stateAuthorChatAcceptPool = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalAuthorChatAccept) }}
	stateAuthorChatReadyPool  = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalAuthorChatReady) }}
	stateAuthorChatEndPool    = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalAuthorChatEnd) }}
	stateAuthorChatSoundPool  = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalAuthorChatChangeSoundConfig) }}
	stateLiveStatePool        = &sync.Pool{New: func() any { return new(acproto.CommonStateSignalLiveState) }}

	// handleNotifySignal
	notifySignalPool       = &sync.Pool{New: func() any { return new(acproto.ZtLiveScNotifySignal) }}
	notifyKickedOutPool    = &sync.Pool{New: func() any { return new(acproto.CommonNotifySignalKickedOut) }}
	notifyViolationPool    = &sync.Pool{New: func() any { return new(acproto.CommonNotifySignalViolationAlert) }}
	notifyManagerStatePool = &sync.Pool{New: func() any { return new(acproto.CommonNotifySignalLiveManagerState) }}
)

// danmuSlicePool 复用弹幕结果切片（[]DanmuMessage）的底层数组，避免每个批次都重新分配。
// 切片元素均为指针，归还后仍由队列/事件持有，不会与切片底层数组产生别名冲突。
// 仅回收容量不超过上限的切片，避免偶发超大批次长期占用内存。
var danmuSlicePool = sync.Pool{
	New: func() any {
		return make([]DanmuMessage, 0, 64)
	},
}

const maxPooledDanmuCap = 4096

// decompressGzip 解压 GZIP 数据，复用 gzip.Reader、bytes.Reader 与输出缓冲（bytebufferpool），
// 几乎消除解压路径上的内存分配，显著降低高弹幕量下的 GC 压力。
// 返回的 *bytebufferpool.ByteBuffer 由调用方在消费完其中的字节（如 proto.Unmarshal 之后）后
// 通过 bytebufferpool.Put 归还，不得长期持有。
func decompressGzip(data []byte) (*bytebufferpool.ByteBuffer, error) {
	gr := gzipReaderPool.Get().(*gzip.Reader)
	defer gzipReaderPool.Put(gr)

	reader := gzipBytesReaderPool.Get().(*bytes.Reader)
	reader.Reset(data)
	defer gzipBytesReaderPool.Put(reader)

	if err := gr.Reset(reader); err != nil {
		return nil, err
	}
	bb := bytebufferpool.Get()
	if _, err := bb.ReadFrom(gr); err != nil {
		bytebufferpool.Put(bb)
		return nil, err
	}
	return bb, nil
}

// 处理接受到的数据里的命令
func (ac *AcFunLive) handleCommand(ctx context.Context, stream *acproto.DownstreamPayload, event bool) (e error) {
	defer func() {
		if err := recover(); err != nil {
			e = fmt.Errorf("handleCommand() error: %v", err)
		}
	}()

	if stream == nil {
		panic(fmt.Errorf("stream 为 nil"))
	}

	switch stream.Command {
	case "Global.ZtLiveInteractive.CsCmd":
		cmd := &acproto.ZtLiveCsCmdAck{}
		err := proto.Unmarshal(stream.PayloadData, cmd)
		checkErr(err)
		switch cmd.CmdAckType {
		case "ZtLiveCsEnterRoomAck":
			enterRoom := &acproto.ZtLiveCsEnterRoomAck{}
			err = proto.Unmarshal(cmd.Payload, enterRoom)
			checkErr(err)
			if enterRoom.HeartbeatIntervalMs > 0 {
				go ac.clientHeartbeat(ctx, enterRoom.HeartbeatIntervalMs)
			} else {
				go ac.clientHeartbeat(ctx, 10000)
			}
		case "ZtLiveCsHeartbeatAck":
			//heartbeat := &acproto.ZtLiveCsHeartbeatAck{}
			//err = proto.Unmarshal(cmd.Payload, heartbeat)
			//checkErr(err)
		case "ZtLiveCsUserExitAck":
			//userExit := &acproto.ZtLiveCsUserExitAck{}
			//err = proto.Unmarshal(cmd.Payload, userExit)
			//checkErr(err)
		default:
			log.Printf("未知的cmd.CmdAckType：%s\npayload string:\n%s\npayload base64:\n%s\n",
				cmd.CmdAckType,
				string(cmd.Payload),
				base64.StdEncoding.EncodeToString(cmd.Payload))
		}
	case "Basic.Handshake":
		handshake := &acproto.HansshakeResponse{}
		err := proto.Unmarshal(stream.PayloadData, handshake)
		checkErr(err)

		_, err = ac.danmuClient.Write(ac.t.register())
		checkErr(err)
	case "Basic.Register":
		register := &acproto.RegisterResponse{}
		err := proto.Unmarshal(stream.PayloadData, register)
		checkErr(err)
		ac.t.instanceID = register.InstanceId
		ac.t.sessionKey = register.SessKey

		_, err = ac.danmuClient.Write(ac.t.keepAlive())
		checkErr(err)
		_, err = ac.danmuClient.Write(ac.t.enterRoom())
		checkErr(err)
	case "Basic.KeepAlive":
		//keepalive := &acproto.KeepAliveResponse{}
		//err := proto.Unmarshal(stream.PayloadData, keepalive)
		//checkErr(err)
	case "Basic.Ping":
		//ping := &acproto.PingResponse{}
		//err := proto.Unmarshal(stream.PayloadData, ping)
		//checkErr(err)
	case "Basic.Unregister":
		unregister := &acproto.UnregisterResponse{}
		err := proto.Unmarshal(stream.PayloadData, unregister)
		checkErr(err)
		ac.t.err.Store(fmt.Errorf("接收到 Unregister 信号"))
		_ = ac.danmuClient.Close("Unregister")
	case "Push.ZtLiveInteractive.Message":
		_, err := ac.danmuClient.Write(ac.t.pushMessage())
		checkErr(err)
		message := getProto[*acproto.ZtLiveScMessage](scMessagePool)
		defer putProto(scMessagePool, message)
		err = proto.Unmarshal(stream.PayloadData, message)
		checkErr(err)
		payload := message.Payload
		var gzBuf *bytebufferpool.ByteBuffer
		if message.CompressionType == acproto.ZtLiveScMessage_GZIP {
			bb, gerr := decompressGzip(message.Payload)
			checkErr(gerr)
			gzBuf = bb
			payload = bb.Bytes()
		}
		switch message.MessageType {
		case "ZtLiveScActionSignal":
			ac.handleActionSignal(&payload, event)
		case "ZtLiveScStateSignal":
			ac.handleStateSignal(&payload, event)
		case "ZtLiveScNotifySignal":
			ac.handleNotifySignal(&payload, event)
		case "ZtLiveScStatusChanged":
			statusChanged := &acproto.ZtLiveScStatusChanged{}
			err = proto.Unmarshal(payload, statusChanged)
			checkErr(err)
			if statusChanged.Type == acproto.ZtLiveScStatusChanged_LIVE_CLOSED || statusChanged.Type == acproto.ZtLiveScStatusChanged_LIVE_BANNED {
				ac.clientStop("Live closed")
			}
		case "ZtLiveScTicketInvalid":
			ticketInvalid := &acproto.ZtLiveScTicketInvalid{}
			err = proto.Unmarshal(payload, ticketInvalid)
			checkErr(err)
			index := ac.t.ticketIndex.Load()
			_ = ac.t.ticketIndex.CompareAndSwap(index, (index+1)%uint32(len(ac.t.tickets)))
			_, err = ac.danmuClient.Write(ac.t.enterRoom())
			checkErr(err)
		default:
			log.Printf("未知的message.MessageType：%s\npayload string:\n%s\npayload base64:\n%s\n",
				message.MessageType,
				string(payload),
				base64.StdEncoding.EncodeToString(payload))
		}
		// 归还 gzip 解压复用的缓冲（handleActionSignal 等已消费其中的字节，不会长期持有）
		if gzBuf != nil {
			bytebufferpool.Put(gzBuf)
		}
	// AcFun 帐号收到的私信信息
	case "Push.Message":
	case "Push.DataUpdate":
	case "Push.SyncSession":
	case "Push.acfun":
	default:
		if stream.ErrorCode > 0 {
			log.Println("DownstreamPayload error:", stream.ErrorCode, stream.ErrorMsg)
			if stream.ErrorCode == 10018 {
				ac.t.err.Store(fmt.Errorf(string(stream.ErrorData)))
				ac.clientStop("Log out")
			} else {
				log.Printf("接收弹幕出现错误：%s", string(stream.ErrorData))
			}
		} else {
			log.Printf("未知的stream.Command：%s\npayload string:\n%s\npayload base64:\n%s\n",
				stream.Command,
				string(stream.PayloadData),
				base64.StdEncoding.EncodeToString(stream.PayloadData))
		}
	}

	return nil
}

// 处理 action signal 数据
func (ac *AcFunLive) handleActionSignal(payload *[]byte, event bool) {
	actionSignal := getProto[*acproto.ZtLiveScActionSignal](actionSignalPool)
	defer putProto(actionSignalPool, actionSignal)
	err := proto.Unmarshal(*payload, actionSignal)
	checkErr(err)

	// 复用弹幕结果切片，避免每个批次都重新分配底层数组
	danmu := danmuSlicePool.Get().([]DanmuMessage)
	danmu = danmu[:0]
	defer func() {
		if cap(danmu) <= maxPooledDanmuCap {
			danmuSlicePool.Put(danmu)
		}
	}()
	for _, item := range actionSignal.Item {
		for _, pl := range item.Payload {
			switch item.SignalType {
			case "CommonActionSignalComment":
				// 热路径优先用零嵌套分配的轻量解析（避免每条弹幕 reflect.New 嵌套消息），
				// 遇到非预期编码时回退到标准 proto.Unmarshal，保证行为正确。
				comment, ok := parseCommentFast(pl)
				if !ok {
					fc := getProto[*acproto.CommonActionSignalComment](commentMsgPool)
					err = proto.Unmarshal(pl, fc)
					if err != nil {
						log.Printf("解析弹幕评论出现错误：%v", err)
						continue
					}
					comment = &Comment{
						DanmuCommon: DanmuCommon{
							SendTime: fc.SendTimeMs,
							UserInfo: newUserInfo(fc.UserInfo),
						},
						Content: fc.Content,
					}
					putProto(commentMsgPool, fc)
				}
				danmu = append(danmu, comment)
			case "CommonActionSignalLike":
				like := getProto[*acproto.CommonActionSignalLike](likeMsgPool)
				err = proto.Unmarshal(pl, like)
				checkErr(err)
				d := &Like{
					SendTime: like.SendTimeMs,
					UserInfo: newUserInfo(like.UserInfo),
				}
				putProto(likeMsgPool, like)
				danmu = append(danmu, d)
			case "CommonActionSignalUserEnterRoom":
				enter := getProto[*acproto.CommonActionSignalUserEnterRoom](enterRoomMsgPool)
				err = proto.Unmarshal(pl, enter)
				checkErr(err)
				d := &EnterRoom{
					SendTime: enter.SendTimeMs,
					UserInfo: newUserInfo(enter.UserInfo),
				}
				putProto(enterRoomMsgPool, enter)
				danmu = append(danmu, d)
			case "CommonActionSignalUserFollowAuthor":
				follow := getProto[*acproto.CommonActionSignalUserFollowAuthor](followMsgPool)
				err = proto.Unmarshal(pl, follow)
				checkErr(err)
				d := &FollowAuthor{
					SendTime: follow.SendTimeMs,
					UserInfo: newUserInfo(follow.UserInfo),
				}
				putProto(followMsgPool, follow)
				danmu = append(danmu, d)
			case "AcfunActionSignalThrowBanana":
				banana := getProto[*acproto.AcfunActionSignalThrowBanana](bananaMsgPool)
				err = proto.Unmarshal(pl, banana)
				checkErr(err)
				d := &ThrowBanana{
					DanmuCommon: DanmuCommon{
						SendTime: banana.SendTimeMs,
						UserInfo: UserInfo{
							UserID:   banana.Visitor.UserId,
							Nickname: banana.Visitor.Name,
						},
					},
					BananaCount: int(banana.Count),
				}
				putProto(bananaMsgPool, banana)
				danmu = append(danmu, d)
			case "CommonActionSignalGift":
				gift := getProto[*acproto.CommonActionSignalGift](giftMsgPool)
				err = proto.Unmarshal(pl, gift)
				checkErr(err)
				ac.t.giftsMutex.RLock()
				g, ok := ac.t.gifts[gift.GiftId]
				ac.t.giftsMutex.RUnlock()
				// 存在未知礼物时
				if !ok {
					list, err := ac.t.getGiftList(ac.t.liveID)
					if err != nil {
						log.Printf("获取礼物列表出现错误：%v", err)
						g = GiftDetail{
							GiftID:   gift.GiftId,
							GiftName: "未知礼物",
						}
					} else {
						ac.t.giftsMutex.Lock()
						ac.t.gifts = list
						g, ok = ac.t.gifts[gift.GiftId]
						if !ok {
							g = GiftDetail{
								GiftID:   gift.GiftId,
								GiftName: "未知礼物",
							}
						}
						ac.t.giftsMutex.Unlock()
					}
				}
				d := &Gift{
					DanmuCommon: DanmuCommon{
						SendTime: gift.SendTimeMs,
						UserInfo: newUserInfo(gift.UserInfo),
					},
					GiftDetail:          g,
					Count:               gift.BatchSize,
					Combo:               gift.ComboCount,
					Value:               gift.Rank,
					ComboID:             gift.ComboKey,
					SlotDisplayDuration: gift.SlotDisplayDurationMs,
					ExpireDuration:      gift.ExpireDurationMs,
				}
				if gift.DrawGiftInfo != nil {
					d.DrawGiftInfo = DrawGiftInfo{
						ScreenWidth:  gift.DrawGiftInfo.ScreenWidth,
						ScreenHeight: gift.DrawGiftInfo.ScreenHeight,
					}
					d.DrawGiftInfo.DrawPoint = make([]DrawPoint, len(gift.DrawGiftInfo.DrawPoint))
					for i, drawPoint := range gift.DrawGiftInfo.DrawPoint {
						d.DrawGiftInfo.DrawPoint[i] = DrawPoint{
							MarginLeft:  drawPoint.MarginLeft,
							MarginTop:   drawPoint.MarginTop,
							ScaleRatio:  drawPoint.ScaleRatio,
							Handup:      drawPoint.Handup,
							PointWidth:  drawPoint.PointWidth,
							PointHeight: drawPoint.PointHeight,
						}
					}
				}
				putProto(giftMsgPool, gift)
				danmu = append(danmu, d)
			case "CommonActionSignalRichText":
				richText := getProto[*acproto.CommonActionSignalRichText](richTextMsgPool)
				err = proto.Unmarshal(pl, richText)
				checkErr(err)
				d := &RichText{
					SendTime: richText.SendTimeMs,
				}
				d.Segments = make([]RichTextSegment, len(richText.Segments))
				for i, segment := range richText.Segments {
					switch segment := segment.Segment.(type) {
					case *acproto.CommonActionSignalRichText_RichTextSegment_UserInfo:
						userInfo := &RichTextUserInfo{
							UserInfo: newUserInfo(segment.UserInfo.User),
							Color:    segment.UserInfo.Color,
						}
						d.Segments[i] = userInfo
					case *acproto.CommonActionSignalRichText_RichTextSegment_Plain:
						plain := &RichTextPlain{
							Text:  segment.Plain.Text,
							Color: segment.Plain.Color,
						}
						d.Segments[i] = plain
					case *acproto.CommonActionSignalRichText_RichTextSegment_Image:
						image := &RichTextImage{
							AlternativeText:  segment.Image.AlternativeText,
							AlternativeColor: segment.Image.AlternativeColor,
						}
						image.Pictures = make([]string, len(segment.Image.Pictures))
						for j, picture := range segment.Image.Pictures {
							image.Pictures[j] = picture.Url
						}
						d.Segments[i] = image
					default:
						log.Println("出现未处理的 RichText Segment")
					}
				}
				putProto(richTextMsgPool, richText)
				danmu = append(danmu, d)
			case "AcfunActionSignalJoinClub":
				join := getProto[*acproto.AcfunActionSignalJoinClub](joinClubMsgPool)
				err = proto.Unmarshal(pl, join)
				checkErr(err)
				d := &JoinClub{
					JoinTime: join.JoinTimeMs,
					FansInfo: UserInfo{
						UserID:   join.FansInfo.UserId,
						Nickname: join.FansInfo.Name,
					},
					UperInfo: UserInfo{
						UserID:   join.UperInfo.UserId,
						Nickname: join.UperInfo.Name,
					},
				}
				putProto(joinClubMsgPool, join)
				danmu = append(danmu, d)
			case "CommonActionSignalUserShareLive":
				share := getProto[*acproto.CommonActionSignalUserShareLive](shareLiveMsgPool)
				err = proto.Unmarshal(pl, share)
				checkErr(err)
				d := &ShareLive{
					DanmuCommon: DanmuCommon{
						SendTime: share.SendTimeMs,
						UserInfo: newUserInfo(share.UserInfo),
					},
					SharePlatform:     SharePlatformType(share.SharePlatformId),
					SharePlatformIcon: share.SharePlatformIcon,
				}
				putProto(shareLiveMsgPool, share)
				danmu = append(danmu, d)
			default:
				log.Printf("未知的Action Signal item.SignalType：%s\npayload string:\n%s\npayload base64:\n%s\n",
					item.SignalType,
					string(pl),
					base64.StdEncoding.EncodeToString(pl))
			}
		}
	}

	// 按 SendTime 大小排序（上游通常已按时间有序，先检测可跳过排序以提速）
	if !slices.IsSortedFunc(danmu, danmuLess) {
		slices.SortFunc(danmu, danmuLess)
	}

	// 热路径上只加载一次 handler 快照（写时复制保证该批次内分发一致），
	// 避免逐条弹幕都 atomic 读取快照与重复 map 查找，降低事件分发开销。
	snap := ac.handlerMap.snap.Load()
	for _, d := range danmu {
		if event {
			switch d := d.(type) {
			case *Comment:
				ac.dispatchEvent(snap, commentDanmu, d)
			case *Like:
				ac.dispatchEvent(snap, likeDanmu, d)
			case *EnterRoom:
				ac.dispatchEvent(snap, enterRoomDanmu, d)
			case *FollowAuthor:
				ac.dispatchEvent(snap, followAuthorDanmu, d)
			case *ThrowBanana:
				ac.dispatchEvent(snap, throwBananaDanmu, d)
			case *Gift:
				ac.dispatchEvent(snap, giftDanmu, d)
			case *RichText:
				ac.dispatchEvent(snap, richTextDanmu, d)
			case *JoinClub:
				ac.dispatchEvent(snap, joinClubDanmu, d)
			case *ShareLive:
				ac.dispatchEvent(snap, shareLiveDanmu, d)
			default:
				log.Println("出现未处理的 DanmuMessage")
			}
		} else {
			err = ac.q.Put(d)
			checkErr(err)
		}
	}
}

// 处理 state signal 数据
func (ac *AcFunLive) handleStateSignal(payload *[]byte, event bool) {
	stateSignal := getProto[*acproto.ZtLiveScStateSignal](stateSignalPool)
	defer putProto(stateSignalPool, stateSignal)
	err := proto.Unmarshal(*payload, stateSignal)
	checkErr(err)

	for _, item := range stateSignal.Item {
		switch item.SignalType {
		case "AcfunStateSignalDisplayInfo":
			bananaInfo := getProto[*acproto.AcfunStateSignalDisplayInfo](stateDisplayInfoPool)
			err = proto.Unmarshal(item.Payload, bananaInfo)
			checkErr(err)
			if event {
				ac.callEvent(bananaCountEvent, bananaInfo.BananaCount)
			} else {
				ac.info.Lock()
				ac.info.AllBananaCount = bananaInfo.BananaCount
				ac.info.Unlock()
			}
			// 用完即还：循环内的每条 item 复用同一对象池，避免 defer 将对象持有到整个
			// 信号处理结束，从而恢复池化复用并消除循环内 defer 的开销。
			putProto(stateDisplayInfoPool, bananaInfo)
		case "CommonStateSignalDisplayInfo":
			stateInfo := getProto[*acproto.CommonStateSignalDisplayInfo](stateDisplayInfoPool)
			err = proto.Unmarshal(item.Payload, stateInfo)
			checkErr(err)
			info := DisplayInfo{
				WatchingCount: stateInfo.WatchingCount,
				LikeCount:     stateInfo.LikeCount,
				LikeDelta:     int(stateInfo.LikeDelta),
			}
			if event {
				ac.callEvent(displayEvent, &info)
			} else {
				ac.info.Lock()
				ac.info.DisplayInfo = info
				ac.info.Unlock()
			}
			putProto(stateDisplayInfoPool, stateInfo)
		case "CommonStateSignalTopUsers":
			topUsers := getProto[*acproto.CommonStateSignalTopUsers](stateTopUsersPool)
			err = proto.Unmarshal(item.Payload, topUsers)
			checkErr(err)
			users := make([]TopUser, len(topUsers.User))
			for i, user := range topUsers.User {
				u := TopUser{
					UserInfo:          newUserInfo(user.UserInfo),
					AnonymousUser:     user.AnonymousUser,
					DisplaySendAmount: user.DisplaySendAmount,
					CustomData:        user.CustomWatchingListData,
				}
				users[i] = u
			}
			if event {
				ac.callEvent(topUsersEvent, users)
			} else {
				ac.info.Lock()
				ac.info.TopUsers = users
				ac.info.Unlock()
			}
			putProto(stateTopUsersPool, topUsers)
		case "CommonStateSignalRecentComment":
			comments := getProto[*acproto.CommonStateSignalRecentComment](stateRecentCommentPool)
			err = proto.Unmarshal(item.Payload, comments)
			checkErr(err)
			danmu := make([]Comment, len(comments.Comment))
			for i, comment := range comments.Comment {
				d := Comment{
					DanmuCommon: DanmuCommon{
						SendTime: comment.SendTimeMs,
						UserInfo: newUserInfo(comment.UserInfo),
					},
					Content: comment.Content,
				}
				danmu[i] = d
			}
			if event {
				ac.callEvent(recentCommentEvent, danmu)
			} else {
				ac.info.Lock()
				ac.info.RecentComment = danmu
				ac.info.Unlock()
			}
			putProto(stateRecentCommentPool, comments)
		case "CommonStateSignalCurrentRedpackList":
			redpackList := getProto[*acproto.CommonStateSignalCurrentRedpackList](stateRedpackListPool)
			err = proto.Unmarshal(item.Payload, redpackList)
			checkErr(err)
			redpacks := make([]Redpack, len(redpackList.Redpacks))
			for i, redpack := range redpackList.Redpacks {
				r := Redpack{
					UserInfo:           newUserInfo(redpack.Sender),
					DisplayStatus:      RedpackDisplayStatus(redpack.DisplayStatus),
					GrabBeginTime:      redpack.GrabBeginTimeMs,
					GetTokenLatestTime: redpack.GetTokenLatestTimeMs,
					RedpackID:          redpack.RedPackId,
					RedpackBizUnit:     redpack.RedpackBizUnit,
					RedpackAmount:      redpack.RedpackAmount,
					SettleBeginTime:    redpack.SettleBeginTime,
				}
				redpacks[i] = r
			}
			if event {
				ac.callEvent(redpackListEvent, redpacks)
			} else {
				ac.info.Lock()
				ac.info.RedpackList = redpacks
				ac.info.Unlock()
			}
			putProto(stateRedpackListPool, redpackList)
		case "CommonStateSignalChatCall":
			chatCall := getProto[*acproto.CommonStateSignalChatCall](stateChatCallPool)
			err = proto.Unmarshal(item.Payload, chatCall)
			checkErr(err)
			if event {
				ac.callEvent(chatCallEvent, &ChatCall{
					ChatID:   chatCall.ChatId,
					LiveID:   chatCall.LiveId,
					CallTime: chatCall.CallTimestampMs,
				})
			}
			putProto(stateChatCallPool, chatCall)
		case "CommonStateSignalChatAccept":
			chatAccept := getProto[*acproto.CommonStateSignalChatAccept](stateChatAcceptPool)
			err = proto.Unmarshal(item.Payload, chatAccept)
			checkErr(err)
			if event {
				ac.callEvent(chatAcceptEvent, &ChatAccept{
					ChatID:     chatAccept.ChatId,
					MediaType:  ChatMediaType(chatAccept.MediaType),
					SignalInfo: chatAccept.AryaSignalInfo,
				})
			}
			putProto(stateChatAcceptPool, chatAccept)
		case "CommonStateSignalChatReady":
			chatReady := getProto[*acproto.CommonStateSignalChatReady](stateChatReadyPool)
			err = proto.Unmarshal(item.Payload, chatReady)
			checkErr(err)
			if event {
				ac.callEvent(chatReadyEvent, &ChatReady{
					ChatID:    chatReady.ChatId,
					Guest:     newUserInfo(chatReady.GuestUserInfo),
					MediaType: ChatMediaType(chatReady.MediaType),
				})
			}
			putProto(stateChatReadyPool, chatReady)
		case "CommonStateSignalChatEnd":
			chatEnd := getProto[*acproto.CommonStateSignalChatEnd](stateChatEndPool)
			err = proto.Unmarshal(item.Payload, chatEnd)
			checkErr(err)
			if event {
				ac.callEvent(chatEndEvent, &ChatEnd{
					ChatID:  chatEnd.ChatId,
					EndType: ChatEndType(chatEnd.EndType),
				})
			}
			putProto(stateChatEndPool, chatEnd)
		//case "AuthorChatPlayerInfo":
		case "CommonStateSignalAuthorChatCall":
			chatCall := getProto[*acproto.CommonStateSignalAuthorChatCall](stateAuthorChatCallPool)
			err = proto.Unmarshal(item.Payload, chatCall)
			checkErr(err)
			if event {
				ac.callEvent(authorChatCallEvent, &AuthorChatCall{
					Inviter: AuthorChatPlayerInfo{
						UserInfo:               newUserInfo(chatCall.InviterUserInfo.Player),
						LiveID:                 chatCall.InviterUserInfo.LiveId,
						EnableJumpPeerLiveRoom: chatCall.InviterUserInfo.EnableJumpPeerLiveRoom,
					},
					ChatID:   chatCall.AuthorChatId,
					CallTime: chatCall.CallTimestampMs,
				})
			}
			putProto(stateAuthorChatCallPool, chatCall)
		case "CommonStateSignalAuthorChatAccept":
			chatAccept := getProto[*acproto.CommonStateSignalAuthorChatAccept](stateAuthorChatAcceptPool)
			err = proto.Unmarshal(item.Payload, chatAccept)
			checkErr(err)
			if event {
				ac.callEvent(authorChatAcceptEvent, &AuthorChatAccept{
					ChatID:     chatAccept.AuthorChatId,
					SignalInfo: chatAccept.AryaSignalInfo,
				})
			}
			putProto(stateAuthorChatAcceptPool, chatAccept)
		case "CommonStateSignalAuthorChatReady":
			chatReady := getProto[*acproto.CommonStateSignalAuthorChatReady](stateAuthorChatReadyPool)
			err = proto.Unmarshal(item.Payload, chatReady)
			checkErr(err)
			if event {
				ac.callEvent(authorChatReadyEvent, &AuthorChatReady{
					Inviter: AuthorChatPlayerInfo{
						UserInfo:               newUserInfo(chatReady.InviterUserInfo.Player),
						LiveID:                 chatReady.InviterUserInfo.LiveId,
						EnableJumpPeerLiveRoom: chatReady.InviterUserInfo.EnableJumpPeerLiveRoom,
					},
					Invitee: AuthorChatPlayerInfo{
						UserInfo:               newUserInfo(chatReady.InviteeUserInfo.Player),
						LiveID:                 chatReady.InviteeUserInfo.LiveId,
						EnableJumpPeerLiveRoom: chatReady.InviteeUserInfo.EnableJumpPeerLiveRoom,
					},
					ChatID: chatReady.AuthorChatId,
				})
			}
			putProto(stateAuthorChatReadyPool, chatReady)
		case "CommonStateSignalAuthorChatEnd":
			chatEnd := getProto[*acproto.CommonStateSignalAuthorChatEnd](stateAuthorChatEndPool)
			err = proto.Unmarshal(item.Payload, chatEnd)
			checkErr(err)
			if event {
				ac.callEvent(authorChatEndEvent, &AuthorChatEnd{
					ChatID:    chatEnd.AuthorChatId,
					EndType:   ChatEndType(chatEnd.EndType),
					EndLiveID: chatEnd.EndLiveId,
				})
			}
			putProto(stateAuthorChatEndPool, chatEnd)
		case "CommonStateSignalAuthorChatChangeSoundConfig":
			soundConfig := getProto[*acproto.CommonStateSignalAuthorChatChangeSoundConfig](stateAuthorChatSoundPool)
			err = proto.Unmarshal(item.Payload, soundConfig)
			checkErr(err)
			if event {
				ac.callEvent(authorChatChangeSoundConfigEvent, &AuthorChatChangeSoundConfig{
					ChatID:                soundConfig.AuthorChatId,
					SoundConfigChangeType: SoundConfigChangeType(soundConfig.SoundConfigChangeType),
				})
			}
			putProto(stateAuthorChatSoundPool, soundConfig)
		case "CommonStateSignalLiveState":
			liveState := getProto[*acproto.CommonStateSignalLiveState](stateLiveStatePool)
			err = proto.Unmarshal(item.Payload, liveState)
			checkErr(err)
			if event {
				ac.callEvent(liveStateEvent, &LiveState{
					A: liveState.A,
					B: liveState.B,
					C: liveState.C,
				})
			}
			putProto(stateLiveStatePool, liveState)
		case "CommonStateSignalArLiveTreasureBoxState":
		default:
			log.Printf("未知的State Signal item.SignalType：%s\npayload string:\n%s\npayload base64:\n%s\n",
				item.SignalType,
				string(item.Payload),
				base64.StdEncoding.EncodeToString(item.Payload))
		}
	}
}

// danmuLess 比较两个 DanmuMessage 的发送时间，供 slices 排序使用（无堆分配）
func danmuLess(a, b DanmuMessage) int {
	at, bt := a.GetSendTime(), b.GetSendTime()
	if at < bt {
		return -1
	}
	if at > bt {
		return 1
	}
	return 0
}

// 处理 notify signal 数据
func (ac *AcFunLive) handleNotifySignal(payload *[]byte, event bool) {
	notifySignal := getProto[*acproto.ZtLiveScNotifySignal](notifySignalPool)
	defer putProto(notifySignalPool, notifySignal)
	err := proto.Unmarshal(*payload, notifySignal)
	checkErr(err)
	for _, item := range notifySignal.Item {
		switch item.SignalType {
		case "CommonNotifySignalKickedOut":
			kickedOut := getProto[*acproto.CommonNotifySignalKickedOut](notifyKickedOutPool)
			err = proto.Unmarshal(item.Payload, kickedOut)
			checkErr(err)
			if event {
				ac.callEvent(kickedOutEvent, kickedOut.Reason)
			} else {
				ac.info.Lock()
				ac.info.KickedOut = kickedOut.Reason
				ac.info.Unlock()
			}
			// 用完即还：循环内每条 item 复用同一对象池，恢复池化复用并消除循环内 defer 开销。
			putProto(notifyKickedOutPool, kickedOut)
		case "CommonNotifySignalViolationAlert":
			violationAlert := getProto[*acproto.CommonNotifySignalViolationAlert](notifyViolationPool)
			err = proto.Unmarshal(item.Payload, violationAlert)
			checkErr(err)
			if event {
				ac.callEvent(violationAlertEvent, violationAlert.ViolationContent)
			} else {
				ac.info.Lock()
				ac.info.ViolationAlert = violationAlert.ViolationContent
				ac.info.Unlock()
			}
			putProto(notifyViolationPool, violationAlert)
		case "CommonNotifySignalLiveManagerState":
			liveManagerState := getProto[*acproto.CommonNotifySignalLiveManagerState](notifyManagerStatePool)
			err = proto.Unmarshal(item.Payload, liveManagerState)
			checkErr(err)
			if event {
				ac.callEvent(managerStateEvent, ManagerState(liveManagerState.State))
			} else {
				ac.info.Lock()
				ac.info.LiveManagerState = ManagerState(liveManagerState.State)
				ac.info.Unlock()
			}
			putProto(notifyManagerStatePool, liveManagerState)
		default:
			log.Printf("未知的Notify Signal signalType：%s\npayload string:\n%s\npayload base64:\n%s\n",
				item.SignalType,
				string(item.Payload),
				base64.StdEncoding.EncodeToString(item.Payload))
		}
	}
}
