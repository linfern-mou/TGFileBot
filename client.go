package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/amarnathcjd/gogram/telegram"
)

// startBot 创建并连接 Bot 客户端, 注册消息处理器并设置命令菜单
func (infos *Infos) startBot() (err error) {
	conf := infos.Conf.Load()
	botID := strconv.FormatInt(infos.BotID, 10)
	if botID != "" && botID != "0" {
		cleanFiles(CleanRealm{ID: botID, Cate: "bot", Realm: "cache", Filter: true})
	}

	// 创建 Bot 客户端
	client, err := telegram.NewClient(botConf("bot"))
	if err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("创建 Bot 客户端失败: %+v", err)
		return err
	}

	// 连接 Bot
	if err = client.Connect(); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("Bot 连接失败: %+v", err)
		return err
	}

	// 登录 Bot
	if err = client.LoginBot(conf.BotToken); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("Bot 登录失败: %+v", err)
		return err
	}

	// 注册 Bot 命令处理函数
	client.On(telegram.OnMessage, handleBotCommand)

	go func() {
		// 先清空默认的命令列表, 确保没有权限的用户什么也看不到
		_, err := client.SetBotCommands([]*telegram.BotCommand{}, nil)
		if err != nil {
			log.Printf("清空默认命令失败: %+v", err)
		}

		userID, err := client.ResolvePeer(conf.UserID)
		if err != nil {
			log.Printf("解析用户 ID 失败: %v", err)
			return
		}
		commands := []*telegram.BotCommand{
			{
				Command:     "qr",
				Description: "获取登录二维码",
			},
			{
				Command:     "phone",
				Description: "输入手机号登录",
			},
			{
				Command:     "code",
				Description: "输入验证码登录(需混入非数字字符)",
			},
			{
				Command:     "pass",
				Description: "输入2FA密码登录",
			},
		}
		commonCommands := []*telegram.BotCommand{
			{
				Command:     "dc",
				Description: "设置客户端默认DC",
			},
			{
				Command:     "allow",
				Description: "添加白名单",
			},
			{
				Command:     "disallow",
				Description: "移除白名单",
			},
			{
				Command:     "add",
				Description: "添加搜索频道",
			},
			{
				Command:     "del",
				Description: "移除搜索频道",
			},
			{
				Command:     "addrule",
				Description: "添加关键词规则",
			},
			{
				Command:     "delrule",
				Description: "移除关键词规则",
			},
			{
				Command:     "list",
				Description: "列出搜索频道、白名单、关键词规则",
			},
			{
				Command:     "info",
				Description: "获取程序运行信息",
			},
			{
				Command:     "size",
				Description: "设置程序缓存大小",
			},
			{
				Command:     "site",
				Description: "设置反代域名",
			},
			{
				Command:     "port",
				Description: "设置HTTP服务端口",
			},
			{
				Command:     "proxy",
				Description: "设置代理",
			},
			{
				Command:     "check",
				Description: "查找HASH对应的用户信息",
			},
			{
				Command:     "workers",
				Description: "设置并发数",
			},
			{
				Command:     "channel",
				Description: "设置绑定频道",
			},
			{
				Command:     "password",
				Description: "设置接口访问密码",
			},
		}
		commands = append(commands, commonCommands...)

		_, err = client.SetBotCommands(commands, &userID)
		if err != nil {
			log.Printf("设置 Bot 超级管理员命令失败: %+v", err)
			return
		}

		for _, adminID := range conf.AdminIDs {
			if adminID == conf.UserID {
				continue
			}
			userID, err := client.ResolvePeer(adminID)
			if err != nil {
				log.Printf("解析用户 ID 失败: %+v", err)
				continue
			}
			_, err = client.SetBotCommands(commonCommands, &userID)
			if err != nil {
				log.Printf("设置 Bot 管理员命令失败: %+v", err)
				continue
			}
		}
	}()

	if conf.DeBUG {
		log.Printf("Bot 启动成功")
	}

	infos.BotClient.Store(client)
	return nil
}

// userBotClient 创建并连接 UserBot 客户端（不执行登录, 仅建立连接）
func (infos *Infos) userBotClient() (err error) {
	appConf := infos.Conf.Load()
	// 清理缓存
	userID := strconv.FormatInt(appConf.UserID, 10)
	if userID != "" && userID != "0" {
		cleanFiles(CleanRealm{ID: userID, Cate: "user", Realm: "cache", Filter: true})
	}

	clientConf := botConf("user")
	if appConf.DC != 0 {
		clientConf.DataCenter = appConf.DC
	}

	client, err := telegram.NewClient(clientConf)
	if err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})
		log.Printf("创建 UserBot 客户端失败: %+v", err)
		return
	}

	// 连接 UserBot
	if err = client.Connect(); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})
		log.Printf("UserBot 连接失败: %+v", err)
		return
	}

	infos.UserClient.Store(client)

	return err
}

// startUserBot 发起手机号登录流程
func (infos *Infos) startUserBot(phone string) (err error) {
	infos.Mutex.Lock()
	switch infos.Status.Load() {
	case 1, 2:
		// 正在进行验证码或密码输入状态, 不允许重复发起
		infos.Mutex.Unlock()
		err = errors.New("已有登录流程正在进行")
		log.Printf("UserBot 登录失败: %+v", err)
		return err
	case 3:
		// 已登录状态, 若客户端实例丢失则尝试重建
		infos.Mutex.Unlock()
		if infos.UserClient.Load() == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		return nil
	default:
		// 未登录状态, 开始新的登录流程
		infos.Mutex.Unlock()
		if infos.UserClient.Load() == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		sendMS(nil, fmt.Sprintf("收到手机号 %s, 正在尝试发送验证码...", phone), nil, 60)

		// 在协程中执行阻塞的登录命令
		go func() {
			status, err := infos.UserClient.Load().Login(phone, &telegram.LoginOptions{
				CodeCallback:     infos.code, // 指定验证码回调函数
				PasswordCallback: infos.pass, // 指定二步验证回调函数
				MaxRetries:       3,
			})
			if err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				sendMS(nil, fmt.Sprintf("UserBot 登录失败: %+v", err), nil, 60)
				infos.resetStatus()
				return
			}

			if status == true {
				if infos.Conf.Load().DeBUG {
					log.Printf("UserBot 登录成功")
				}
				if err := infos.checkStatus(); err != nil {
					log.Printf("UserBot 登录失败: %+v", err)
					infos.resetStatus()
					return
				}
			}
		}()
	}

	return nil
}

// startUserBotQR 发起二维码登录流程
func (infos *Infos) startUserBotQR() (err error) {
	infos.Mutex.Lock()
	switch infos.Status.Load() {
	case 1, 2:
		infos.Mutex.Unlock()
		err = errors.New("已有登录流程正在进行")
		log.Printf("UserBot 登录失败: %+v", err)
		return err
	case 3:
		infos.Mutex.Unlock()
		if infos.UserClient.Load() == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		return nil
	default:
		infos.Status.Store(1)
		infos.Mutex.Unlock()
		if infos.UserClient.Load() == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		sendMS(nil, "正在请求登录二维码...", nil, 60)

		// 启动登录流程（会阻塞, 直到登录完成或失败）
		go func() {
			qr, err := infos.UserClient.Load().QRLogin(telegram.QrOptions{
				PasswordCallback: infos.pass,
			})
			if err != nil {
				log.Printf("获取 QR 登录失败: %+v", err)
				if !telegram.MatchError(err, "SESSION_PASSWORD_NEEDED]") {
					sendMS(nil, fmt.Sprintf("获取 QR 登录失败: %+v", err), nil, 60)
					infos.resetStatus()
					return
				}
			}

			png, err := qr.ExportAsPng()
			if err != nil {
				log.Printf("导出 QR PNG 失败: %+v", err)
				return
			}

			src, err := infos.BotClient.Load().UploadFile(png, &telegram.UploadOptions{
				FileName: "qr.png",
			})
			if err != nil {
				log.Printf("上传 QR 文件失败: %+v", err)
				return
			}
			sendMS(nil, src, &telegram.SendOptions{Caption: "请使用手机 Telegram 扫描此二维码登录。二维码有效期 30 秒, 如失效请重新发送 /qr"}, 35)
			err = qr.WaitLogin()
			if err != nil {
				if !strings.Contains(err.Error(), "scanning again") {
					sendMS(nil, fmt.Sprintf("QR 登录失败: %+v", err), nil, 60)
					infos.resetStatus()
					return
				}
			}

			if err := infos.checkStatus(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return
			}
		}()
	}

	return nil
}

// checkStatus 获取当前 UserBot 登录状态并校验 ID 是否合法
func (infos *Infos) checkStatus() (err error) {
	// 登录成功
	me, err := infos.UserClient.Load().GetMe()
	if err != nil {
		log.Printf("获取用户信息失败: %+v", err)
		infos.Mutex.Lock()
		infos.Status.Store(0)
		infos.Mutex.Unlock()
		return nil
	}

	if me.ID == infos.Conf.Load().UserID {
		name := me.FirstName + me.LastName
		if me.Username != "" {
			name = "@" + me.Username
		}
		src := fmt.Sprintf("登录成功! 用户: %s", name)
		log.Print(src)
		sendMS(nil, src, nil)
		infos.Mutex.Lock()
		infos.Status.Store(3)
		infos.Mutex.Unlock()
		return nil
	} else {
		log.Printf("登录失败: 用户ID不匹配, 期望 %d, 实际 %d", infos.Conf.Load().UserID, me.ID)
		if client := infos.UserClient.Load(); client != nil {
			if err := client.Disconnect(); err != nil {
				log.Printf("UserBot 退出失败: %+v", err)
			}
		}
		infos.resetStatus()
		return infos.userBotClient()
	}
}

// resetStatus 断开 UserBot 连接并清理 session/cache, 将状态重置为未登录
func (infos *Infos) resetStatus() {
	// 排空可能残留的旧验证码/密码
	select {
	case <-infos.Code:
	default:
	}
	select {
	case <-infos.Pass:
	default:
	}

	// 1. 断开连接并清理句柄
	if client := infos.UserClient.Load(); client != nil {
		if err := client.Disconnect(); err != nil {
			log.Printf("UserBot 断开连接失败: %+v", err)
		}
	}
	// 2. 清理磁盘上的 Session 和 Cache 文件（防止因文件损坏导致的下次循环失败）
	cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
	cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})

	// 3. 重置内存状态
	infos.UserClient.Store(nil)
	infos.Status.Store(0)
}

// code 是登录回调, 暂停协程等待用户通过 Bot 发送验证码
func (infos *Infos) code() (code string, err error) {
	// 使用CompareAndSwap原子操作确保只有一个goroutine能进入
	if !infos.Status.CompareAndSwap(0, 1) {
		err = errors.New("当前状态不是等待验证码")
		sendMS(nil, err.Error(), nil, 60)
		return "", err
	}
	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()

	sendMS(nil, "等待用户输入 /code 验证码...", nil, 120)
	select {
	case code := <-infos.Code:
		return code, nil
	case <-timeout.C:
		infos.Status.Store(0)
		err = errors.New("等待验证码超时")
		sendMS(nil, err.Error(), nil, 60)
		return "", err
	}
}

// submitCode 接收用户通过 Bot 发送的验证码并写入通道
func (infos *Infos) submitCode(str string) (err error) {
	infos.Mutex.Lock()

	if infos.Status.Load() != 1 {
		infos.Mutex.Unlock()
		err = errors.New("当前状态不是等待验证码")
		return err
	}

	// 过滤非数字字符
	var sb strings.Builder
	for _, r := range str {
		if isNumber(r) {
			sb.WriteRune(r)
		}
	}

	code := sb.String()
	infos.Mutex.Unlock() // 发送前解锁，允许阻塞但不会死锁全局

	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()

	select {
	case infos.Code <- code:
		return nil
	case <-timeout.C:
		err = errors.New("等待验证码超时")
		infos.Status.Store(0) // 流程失败，重置为未登录状态
		return err
	}
}

// pass 是登录回调, 暂停协程等待用户通过 Bot 发送 2FA 密码
func (infos *Infos) pass() (pass string, err error) {
	// 使用CompareAndSwap原子操作确保只有一个goroutine能进入
	if !infos.Status.CompareAndSwap(1, 2) {
		err = errors.New("当前状态不是等待2FA密码")
		sendMS(nil, err.Error(), nil, 60)
		return "", err
	}
	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()

	sendMS(nil, "等待用户输入 /pass 2FA密码...", nil, 120)
	select {
	case pass := <-infos.Pass:
		return pass, nil
	case <-timeout.C:
		err = errors.New("等待2FA密码超时")
		sendMS(nil, err.Error(), nil, 60)
		infos.Status.Store(0) // 流程失败，重置为未登录状态
		return "", err
	}
}

// submitPass 接收用户通过 Bot 发送的 2FA 密码并写入通道
func (infos *Infos) submitPass(pass string) (err error) {
	infos.Mutex.Lock()

	if infos.Status.Load() != 2 {
		infos.Mutex.Unlock()
		err = errors.New("当前状态不是等待2FA密码")
		return err
	}
	infos.Mutex.Unlock() // 发送前解锁，允许阻塞但不会死锁全局

	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()

	select {
	case infos.Pass <- pass:
		return nil
	case <-timeout.C:
		err = errors.New("等待2FA密码超时")
		infos.Status.Store(0) // 流程失败，重置为未登录状态
		return err
	}
}

// cateClient 根据消息缓存记录的 cate ("user"/"bot") 解析出对应的客户端实例。
// 供 HTTP 处理器在拿到 handleMs 的结果后使用，取代此前直接读取共享字段 infos.Client 的做法。
func (infos *Infos) cateClient(cate string) *telegram.Client {
	if cate == "user" {
		return infos.UserClient.Load()
	}
	return infos.BotClient.Load()
}

// wakeTCP 预热连接，防止冷启动卡死
// client 由调用方显式传入（而非读取共享的 infos.Client），避免并发请求下客户端选择互相覆盖
func (infos *Infos) wakeTCP(client *telegram.Client, cate string) error {
	if client == nil {
		return errors.New("client 不能为 nil")
	}
	debug := infos.Conf.Load().DeBUG

	// 设置较短超时
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 最轻量探活 RPC
	latenc, err := client.Ping(ctx)
	if err != nil {
		if debug {
			log.Printf("TCP 链路异常, 正在重连: %+v", err)
		}
		// 强制断开
		if err := client.Disconnect(); err != nil {
			log.Printf("强制断开 TCP 连接失败: %+v", err)
		}
		// 重连
		if err := client.Connect(); err != nil {
			log.Printf("重连 TCP 失败: %+v", err)
			return err
		}
		// 重连后再次验证，必须使用全新的 context，防止使用已过期的旧 context
		newCtx, newCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer newCancel()
		if value, err := client.Ping(newCtx); err != nil {
			log.Printf("重连 TCP 后验证失败: %+v", err)
			return err
		} else {
			if debug {
				log.Printf("TCP 链路已恢复, 延迟: %dms", value.Milliseconds())
			}
			infos.tcpStat(cate).wake(value.Milliseconds())
			return nil
		}
	}

	if debug {
		log.Printf("TCP 链路正常, 延迟: %dms", latenc.Milliseconds())
	}
	infos.tcpStat(cate).wake(latenc.Milliseconds())
	return nil
}

// botConf 构造 Telegram 客户端所需的通用配置
func botConf(cate string) (conf telegram.ClientConfig) {
	appConf := infos.Conf.Load()
	conf = telegram.ClientConfig{
		AppID:        appConf.AppID,
		AppHash:      appConf.AppHash,
		LogLevel:     telegram.LogError,
		Session:      filepath.Join(infos.FilesPath, fmt.Sprintf("%s.session", cate)),
		Cache:        telegram.NewCache(filepath.Join(infos.FilesPath, fmt.Sprintf("%s.cache", cate))),
		CacheSenders: true,
		DeviceConfig: telegram.DeviceConfig{
			DeviceModel:   "Android",
			SystemVersion: "Android 14",
			AppVersion:    "10.14.3",
		},
		FloodHandler: func(ctx context.Context, err error) bool {
			wait := 3
			matches := infos.Rex.FindStringSubmatch(err.Error())
			if len(matches) > 1 {
				for _, match := range matches {
					if value, err := strconv.Atoi(match); err == nil {
						wait = value
						break
					}
				}
			}
			log.Printf("访问太过频繁, 等待 %d 秒后重试", wait+1)
			waitSec := time.Duration(wait+1) * time.Second
			waitUntil := time.Now().Add(waitSec)
			infos.WaitUntil.Store(waitUntil.Unix())

			timer := time.NewTimer(waitSec)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}

			return true
		},
	}
	if appConf.Proxy != "" {
		proxy, err := telegram.ProxyFromURL(appConf.Proxy)
		if err == nil {
			conf.Proxy = proxy
		} else {
			log.Printf("代理地址解析失败: %v", err)
		}
	}
	return conf
}

// list
func (infos *Infos) list(channel string, page, limit int, offset int32, filter int64, reverse bool, ctx context.Context) (items Items, err error) {
	channelInfo, err := infos.handleChannel(channel)
	if err != nil {
		return items, err
	}
	if page == 1 {
		handleOffset("del", channel, 0)
	} else {
		offset = handleOffset("get", fmt.Sprintf("%s|%d", channel, page), 0)
	}

	if page > 1 && offset == 0 {
		return items, errors.New("未找到匹配消息")
	}

	params := HandleMs{
		CID:      channelInfo.CID,
		OffsetID: offset,
		Limit:    limit,
		Filter:   &telegram.InputMessagesFilterPhotoVideo{},
		Ctx:      ctx,
		Cate:     "user",
	}

	msCache, err := infos.handleMs(params)
	if err != nil {
		return items, err
	}

	ms := msCache.load()
	lenMs := len(ms)
	switch {
	case lenMs == 0:
		return items, errors.New("未找到匹配消息")
	case lenMs == limit:
		handleOffset("set", fmt.Sprintf("%s|%d", channel, page+1), ms[lenMs-1].ID)
		items.HasMore = true
	}

	// 按频道读取上一页遗留的相册边界去重信息, latestMIDs 精确匹配消息 ID,
	// 不再用字符串子串匹配（会把 ID=12 误判为 ID=123 的子串导致误删), 且按频道隔离, 避免不同频道间 mid 相同时互相污染
	infos.Mutex.RLock()
	latestGroup := infos.LatestGroups[channel]
	infos.Mutex.RUnlock()
	latestCount := 0
	var latestMIDs map[int32]bool
	if latestGroup != nil {
		latestCount = latestGroup.Count
		latestMIDs = latestGroup.MIDs
	}

	mids := make(map[int32]bool)
	maxNum := len(ms) - 1
	for num, m := range ms {
		if m.File == nil {
			continue
		}
		if num <= latestCount && latestMIDs[m.ID] {
			continue
		}

		if value, ok := mids[m.ID]; ok && value {
			continue
		}

		if IsVideoFile(m.File.Ext) && m.File.Size < filter {
			continue
		}

		if items.Channel == "" {
			items.Channel = strings.TrimSpace(m.Channel.Title)
		}

		if (num == 0 || num == maxNum) && m.Message.GroupedID != 0 {
			medias, err := m.GetMediaGroup()
			if err != nil {
				log.Printf("提取媒体组错误: %+v", err)
			}

			count := 0
			newMIDs := make(map[int32]bool, len(medias))
			for _, media := range medias {
				if IsVideoFile(media.File.Ext) && media.File.Size < filter {
					continue
				}
				if value, ok := mids[media.ID]; ok && value {
					continue
				}

				if latestMIDs[media.ID] {
					break
				}

				count++
				newMIDs[media.ID] = true
				mids[media.ID] = true
				item := handleItem(media)
				items.Item = append(items.Item, item)
			}
			if num == maxNum {
				infos.Mutex.Lock()
				evictOldestLatestGroup(infos.LatestGroups, infos.MaxChannel)
				infos.LatestGroups[channel] = &LatestGroup{Count: count, MIDs: newMIDs, Time: time.Now()}
				infos.Mutex.Unlock()
			}
		} else {
			mids[m.ID] = true
			item := handleItem(m)
			items.Item = append(items.Item, item)
		}
	}

	sortItems(items.Item, reverse)
	items.ID = channel
	return items, nil
}

// search 在指定频道中搜索关键词并返回匹配的媒体文件列表
func (infos *Infos) search(channel, keywords string, page, limit int, offset int32, filter int64, reverse bool, ctx context.Context) (items Items, err error) {
	channelInfo, err := infos.handleChannel(channel)
	if err != nil {
		return items, err
	}

	if offset == 0 {
		key := fmt.Sprintf("%s|%s|%d", channel, keywords, page)
		offset = handleOffset("get", key, offset)
		if page > 1 && offset == 0 {
			return items, errors.New("未找到匹配消息")
		}
	}

	params := HandleMs{
		CID:      channelInfo.CID,
		OffsetID: offset,
		Limit:    limit,
		Filter:   &telegram.InputMessagesFilterPhotoVideo{},
		Ctx:      ctx,
		Words:    keywords,
		Cate:     "user",
	}

	msCache, err := infos.handleMs(params)
	if err != nil {
		return items, err
	}

	ms := msCache.load()
	lenMs := len(ms)
	switch {
	case lenMs == 0:
		return items, errors.New("未找到匹配消息")
	case lenMs == limit:
		key := fmt.Sprintf("%s|%s|%d", channel, keywords, page+1)
		handleOffset("set", key, ms[lenMs-1].ID)
		items.HasMore = true
	}

	for _, m := range ms {
		if m.File == nil {
			continue
		}

		if IsVideoFile(m.File.Ext) && m.File.Size < filter {
			continue
		}

		if items.Channel == "" {
			items.Channel = strings.TrimSpace(m.Channel.Title)
		}
		items.Item = append(items.Item, handleItem(m))
	}
	items.ID = channel
	items.Word = keywords
	sortItems(items.Item, reverse)
	return items, nil
}

// handleMs 根据当前网络延迟选择最佳客户端
func (infos *Infos) handleMs(params HandleMs) (result *MsCache, err error) {
	debug := infos.Conf.Load().DeBUG

	// 1. 选择下载客户端
	// 注意：客户端选择结果保存在局部变量 client 中，不写回共享字段，
	// 避免并发请求下 A 请求选中的客户端被 B 请求的选择覆盖（数据竞争）
	var client *telegram.Client
	if params.Cate == "user" && infos.Status.Load() == 3 {
		client = infos.UserClient.Load()
	} else {
		params.Cate = "bot"
		client = infos.BotClient.Load()
	}

	stat := infos.tcpStat(params.Cate)
	latenc := stat.Latenc.Load()
	duration := stat.since()

	// 2. 统一处理 TCP 链路检查与唤醒逻辑（彻底去除了重复代码）
	// 当连续失败计数 > 0 时, 无视 30 分钟阈值强制触发探活重连
	switch {
	case duration.Minutes() > 30 || stat.Fails.Load() > 0:
		if err = infos.wakeTCP(client, params.Cate); err != nil {
			log.Printf("唤醒 TCP 连接失败: %+v", err)
			return result, err
		}
	case debug:
		minutes := int(duration.Minutes())
		seconds := int(duration.Seconds()) % 60
		if minutes != 0 {
			timeStr := fmt.Sprintf("%02d分%02d秒", minutes, seconds)
			timeStr = strings.TrimPrefix(timeStr, "0")
			log.Printf("TCP 链路正常, %s前唤醒, 延迟: %d毫秒", timeStr, latenc)
		} else {
			log.Printf("TCP 链路正常, %d秒前唤醒, 延迟: %d毫秒", seconds, latenc)
		}
	}

	// 3. 获取消息
	if params.Limit == 0 {
		params.Limit = 100
	}

	src := ""
	kname := params.Cate

	if len(params.CNames) > 0 {
		channel, err := infos.handleChannel(params.CNames[0])
		if err != nil {
			return result, err
		}
		params.CID = channel.CID
		src = "name=" + channel.UserName
		kname += ":" + channel.UserName
	}

	cidStr := strconv.FormatInt(params.CID, 10)
	src = "cid=" + cidStr
	kname += ":" + cidStr

	if len(params.MIDs) > 0 {
		src += ", mids=["
		for _, mid := range params.MIDs {
			midStr := strconv.FormatInt(int64(mid), 10)
			src += midStr + ", "
			kname += ":" + midStr
		}
		src = strings.TrimRight(src, ", ")
		src += "]"
	}

	if params.OffsetID > 0 {
		offsetIDStr := strconv.FormatInt(int64(params.OffsetID-1), 10)
		src += ", offset=" + offsetIDStr
		kname += ":" + offsetIDStr
	}

	if params.Words != "" {
		src += ", keywords=" + params.Words
		kname += ":" + params.Words
	}

	lenMIDs := len(params.MIDs)
	if lenMIDs > 0 && params.Limit > lenMIDs {
		params.Limit = lenMIDs
	}

	// 不同 Limit 的请求不能共用同一份缓存, 否则会返回条数与请求不符的结果（见 kname 说明）
	kname += ":limit=" + strconv.Itoa(params.Limit)

	infos.Mutex.RLock()
	result, ok := infos.MsCache[kname]
	infos.Mutex.RUnlock()

	// hit 的判断与 result.Time 的更新必须在同一把锁内完成：result.Mes/Time 可能被
	// refreshMs 或流式下载完成后的缓存回写并发修改（详见 MsCache.load 的注释）
	hit := false
	if ok {
		infos.Mutex.Lock()
		if result.Mes != nil && len(result.Mes) >= params.Limit {
			hit = true
			result.Time = time.Now()
		}
		infos.Mutex.Unlock()
	}

	if hit {
		if debug {
			log.Printf("命中消息缓存: %s", kname)
		}
	} else {
		param := &telegram.SearchOption{
			IDs:     params.MIDs,
			Query:   params.Words,
			Limit:   int32(params.Limit),
			Offset:  params.OffsetID,
			Context: params.Ctx,
			Filter:  params.Filter,
		}
		ms, err := client.GetMessages(params.CID, param)
		if err != nil {
			// RPC 调用失败可能是 TCP 断连引起, 标记失败以便下次请求强制触发 wakeTCP
			stat.fail()
			return result, err
		}

		if len(ms) == 0 {
			err = errors.New("未获取到消息")
			if debug {
				log.Printf("获取消息失败: %s, count=%d, err=%+v", src, len(ms), err)
			}
			return result, err
		}
		result = &MsCache{Mes: ms, Time: time.Now(), Cate: params.Cate}
		if len(ms) == params.Limit && (lenMIDs > 0 || params.OffsetID > 0) {
			infos.Mutex.Lock()
			evictOldestMsCache(infos.MsCache, infos.MaxMs)
			infos.MsCache[kname] = result
			infos.Mutex.Unlock()
		}
	}

	return result, nil
}

// 刷新消息, 用于异步下载完成后的缓存更新
// client 由调用方显式传入（而非读取共享的 infos.Client），避免并发请求下客户端选择互相覆盖
func (infos *Infos) refreshMs(client *telegram.Client, version int64, params HandleMs, msCache *MsCache) (src telegram.NewMessage, err error) {
	debug := infos.Conf.Load().DeBUG
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()

	if version != msCache.Version.Load() {
		if len(msCache.Mes) > 0 {
			src = msCache.Mes[0]
			if debug {
				log.Printf("文件引用已刷新, 直接使用新版本, cid=%d, mids=%v, name=%s, version=%d, newVersion=%d", params.CID, params.MIDs, src.File.Name, version, msCache.Version.Load())
			}
			return src, nil
		} else {
			log.Printf("文件引用已刷新, 但未获取到消息, cid=%d, mids=%v, version=%d, newVersion=%d", params.CID, params.MIDs, version, msCache.Version.Load())
			return src, errors.New("未获取到消息")
		}
	}

	// 重新获取消息
	ms, err := client.GetMessages(params.CID, &telegram.SearchOption{
		IDs:     params.MIDs,
		Context: params.Ctx,
	})
	if err != nil {
		log.Printf("刷新文件引用失败: %+v", err)
		return src, err
	}
	if len(ms) == 0 {
		err = errors.New("未获取到消息")
		log.Printf("未获取到消息: cid=%v, mids=%v", params.CID, params.MIDs)
		return src, err
	}
	src = ms[0]
	if !src.IsMedia() {
		err = errors.New("消息不包含媒体")
		log.Printf("消息不包含媒体: cid=%v, mids=%v", params.CID, params.MIDs)
		return src, err
	}
	msCache.Mes = ms
	msCache.Time = time.Now()
	msCache.Version.Add(1)
	if debug {
		log.Printf("缓存数据更新, cid=%d, mids=%v, name=%s, version=%d", params.CID, params.MIDs, src.File.Name, msCache.Version.Load())
	}
	return src, nil
}

// handleChannel 处理频道ID, 返回 InputPeer
func (infos *Infos) handleChannel(channel string, hash ...int64) (result ChannelInfo, err error) {
	infos.Mutex.RLock()
	cache, ok := infos.ChannelID[channel]
	infos.Mutex.RUnlock()
	if !ok {
		src := strings.TrimPrefix(channel, "@")
		if isAllNumber(src) {
			if !strings.HasPrefix(src, "-100") {
				src = "-100" + src
			}
			cid, err := strconv.ParseInt(src, 10, 64)
			if err != nil {
				log.Printf("频道 %s 解析失败: %+v", channel, err)
				return result, err
			}
			result.CID = cid
			if len(hash) > 0 && hash[0] != 0 {
				result.Hash = hash[0]
			} else {
				result.Hash = 0
			}
			result.Peer = &telegram.InputPeerUser{
				UserID:     cid,
				AccessHash: result.Hash,
			}
		} else {
			values, err := infos.UserClient.Load().ResolvePeer(channel)
			if err != nil {
				log.Printf("频道解析失败: %+v", err)
				return result, err
			}
			result.UserName = channel
			result.Peer = values
			switch value := values.(type) {
			case *telegram.InputPeerChannel:
				// 匹配到频道
				result.CID = value.ChannelID
				result.Hash = value.AccessHash
				result.Peer = value
			case *telegram.InputPeerUser:
				// 匹配到用户（假设有 UserID）
				result.CID = value.UserID
				result.Hash = value.AccessHash
				result.Peer = value
			case *telegram.InputPeerChat:
				// 匹配到普通群
				result.CID = value.ChatID
				if len(hash) > 0 && hash[0] != 0 {
					result.Hash = hash[0]
				} else {
					result.Hash = 0
				}
				result.Peer = value
			default:
				return result, errors.New("未知或不支持的 Peer 类型")
			}
			result.Time = time.Now()
			infos.Mutex.Lock()
			evictOldestChannelCache(infos.ChannelID, infos.MaxChannel)
			infos.ChannelID[channel] = &result
			infos.Mutex.Unlock()
		}
	} else {
		infos.Mutex.Lock()
		cache.Time = time.Now()
		infos.Mutex.Unlock()
		result = *cache
		if infos.Conf.Load().DeBUG {
			log.Printf("命中频道缓存: %s", channel)
		}
	}
	return result, nil
}

// handleComments 处理评论消息，返回评论消息列表
// limit 为本次希望拉取的评论条数（对应 HTTP 请求的分页大小), hasMore 表示 Telegram 一侧是否还有更多评论未拉取,
// 由"实际拉取到的原始评论条数是否达到 limit"判断——不能用追加到 ms 后的条数判断, 因为其中非媒体消息会被过滤掉。
// page 的续传方式对齐 search()：offset==0 时按 "comments|mid|page" 查缓存拿到真实游标，
// 不在缓存里且 page>1 说明跳页请求，直接报错（不能像 page=1 那样从头拉取）
func (infos *Infos) handleComments(mid, offset int32, page, limit int, ms *[]telegram.NewMessage) (hasMore bool, err error) {
	if len(*ms) == 0 {
		return false, errors.New("未找到消息")
	}
	if limit <= 0 {
		limit = 100
	}

	if offset == 0 {
		key := fmt.Sprintf("comments|%d|%d", mid, page)
		offset = handleOffset("get", key, offset)
		if page > 1 && offset == 0 {
			return false, errors.New("未找到匹配消息")
		}
	}

	src := (*ms)[0]
	if src.Message.Replies != nil && src.Message.Replies.ChannelID != 0 {
		discussionID := src.Message.Replies.ChannelID
		username := src.Channel.Username
		if username == "" {
			username = strconv.FormatInt(src.Chat.ID, 10)
		}
		channelInfo, err := infos.handleChannel(username)
		if err != nil {
			log.Printf("获取频道失败: %+v", err)
			return false, err
		}
		if channelInfo.Hash == 0 && src.Channel.AccessHash != 0 {
			channelInfo.Hash = src.Channel.AccessHash
			channelInfo.Peer = &telegram.InputPeerChannel{
				ChannelID:  src.Channel.ID,
				AccessHash: channelInfo.Hash,
			}
		}
		results, err := infos.UserClient.Load().MessagesGetReplies(&telegram.MessagesGetRepliesParams{
			Peer:     channelInfo.Peer,
			Limit:    int32(limit),
			OffsetID: offset,
			MsgID:    mid,
		})

		if err != nil {
			log.Printf("获取评论消息失败: cid=%d, mid=%d, err=%v", src.Channel.ID, mid, err)
			return false, err
		}

		// 从 MessagesGetReplies 的结果中提取原始消息列表和随附的 Chats。
		// Chats 里带有讨论组的完整 AccessHash——必须在 PackMessages 之前注册进客户端缓存，
		// 否则 packMessage 内部按 PeerID 反查频道时缓存未命中，只能用 access_hash=0 现猜，
		// 导致除第一条(种子消息本身、频道信息天然已缓存)外的评论消息 Channel 解析失败/错误，
		// 使得 item.CID 缺失或不正确，播放时后端按 cid+mid 找不到对应媒体。
		var newMs []telegram.Message
		var chats []telegram.Chat
		switch v := results.(type) {
		case *telegram.MessagesMessagesSlice:
			newMs, chats = v.Messages, v.Chats
		case *telegram.MessagesChannelMessages:
			newMs, chats = v.Messages, v.Chats
		case *telegram.MessagesMessagesObj:
			newMs, chats = v.Messages, v.Chats
		default:
			log.Printf("收到未知的底层具体类型: %T, %v", v, v)
		}

		var discussionChannel *telegram.Channel
		for _, ch := range chats {
			if channel, ok := ch.(*telegram.Channel); ok {
				infos.UserClient.Load().Cache.UpdateChannel(channel)
				if channel.ID == discussionID {
					discussionChannel = channel
				}
			}
		}

		// 拉到的原始评论数达到 limit, 说明 Telegram 一侧大概率还有更多未拉取的评论
		hasMore = len(newMs) >= limit

		// PackMessages 将 []telegram.Message 转为 []*telegram.NewMessage；
		// 上面已注册频道缓存，这里再兜底纠正一次 Channel(此前误写为 Chat.ID，对 item.CID 无效果)
		startLen := len(*ms)
		for _, nm := range telegram.PackMessages(infos.UserClient.Load(), newMs) {
			if !nm.IsMedia() {
				continue
			}
			if nm.Channel == nil || nm.Channel.ID != discussionID {
				switch {
				case discussionChannel != nil:
					nm.Channel = discussionChannel
				case nm.Channel != nil:
					nm.Channel.ID = discussionID
					nm.Channel.Title = src.Channel.Title
					nm.Channel.Username = src.Channel.Username
					nm.Channel.AccessHash = src.Channel.AccessHash
				}
			}
			*ms = append(*ms, *nm)
		}

		// 记录下一页的续传游标, 供下次 page+1 请求时通过 handleOffset("get", ...) 取回，
		// 跟 search() 里 "page -> offset" 的续传方式保持一致
		if hasMore && len(*ms) > startLen {
			key := fmt.Sprintf("comments|%d|%d", mid, page+1)
			handleOffset("set", key, (*ms)[len(*ms)-1].ID)
		}
	}
	return hasMore, nil
}

// handleLinks 处理消息媒体, 返回直链
func handleLinks(res HackLink, item Item) (link string) {
	conf := infos.Conf.Load()
	link = fmt.Sprintf("%s/stream?cid=%v&mid=%d&cate=user", strings.TrimSuffix(conf.Site, "/"), item.CID, item.MID)
	if item.Username != "" {
		link += fmt.Sprintf("&cname=%s", item.Username)
	}

	if conf.Password != "" {
		if res.M != nil {
			link += fmt.Sprintf("&hash=%s&uid=%d", infos.calculateHash(res.M.SenderID()), res.M.SenderID())
		} else {
			switch {
			case res.Hash != "" && res.UID != 0:
				link += fmt.Sprintf("&hash=%s&uid=%d", res.Hash, res.UID)
			case res.Pass != "":
				link += fmt.Sprintf("&key=%s", res.Pass)
			default:
				log.Print("未提供密码或哈希")
			}
		}
	}
	return link
}

// handleItem 处理消息媒体, 返回 Item
func handleItem(m telegram.NewMessage) (item Item) {
	src := strings.TrimSpace(m.Text())
	src = strings.ReplaceAll(src, "_", "-")
	src = strings.TrimSpace(src)

	var last rune
	var srcBuilder strings.Builder
	srcBuilder.Grow(len(src))
	for _, char := range src {
		if unicode.IsSpace(char) && char == last {
			continue
		}
		srcBuilder.WriteRune(char)
		last = char
	}
	src = srcBuilder.String()

	name := strings.TrimSpace(m.File.Name)
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.Join(strings.Fields(name), " ")

	item.Ext = m.File.Ext
	item.Src = src
	item.Name = name
	item.Size = m.File.Size
	item.CID = m.Channel.ID
	item.Username = m.Channel.Username
	item.MID = m.ID
	if m.Message != nil {
		item.Date = m.Message.Date
		item.GID = m.Message.GroupedID
	}
	return item
}
