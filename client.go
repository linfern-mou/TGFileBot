package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// startBot 创建并连接 Bot 客户端, 注册消息处理器并设置命令菜单
func (infos *Infos) startBot() (err error) {
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
	if err = client.LoginBot(infos.Conf.BotToken); err != nil {
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

		userID, err := client.ResolvePeer(infos.Conf.UserID)
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

		for _, adminID := range infos.Conf.AdminIDs {
			if adminID == infos.Conf.UserID {
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

	if infos.Conf.DeBUG {
		log.Printf("Bot 启动成功")
	}

	infos.Mutex.Lock()
	infos.BotClient = client
	infos.Mutex.Unlock()
	return nil
}

// userBotClient 创建并连接 UserBot 客户端（不执行登录, 仅建立连接）
func (infos *Infos) userBotClient() (err error) {
	// 清理缓存
	userID := strconv.FormatInt(infos.Conf.UserID, 10)
	if userID != "" && userID != "0" {
		cleanFiles(CleanRealm{ID: userID, Cate: "user", Realm: "cache", Filter: true})
	}

	conf := botConf("user")
	if infos.Conf.DC != 0 {
		conf.DataCenter = infos.Conf.DC
	}

	client, err := telegram.NewClient(conf)
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

	infos.Mutex.Lock()
	infos.UserClient = client
	infos.Mutex.Unlock()

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
		if infos.UserClient == nil {
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
		if infos.UserClient == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		sendMS(nil, fmt.Sprintf("收到手机号 %s, 正在尝试发送验证码...", phone), nil, 60)

		// 在协程中执行阻塞的登录命令
		go func() {
			status, err := infos.UserClient.Login(phone, &telegram.LoginOptions{
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
				if infos.Conf.DeBUG {
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
		if infos.UserClient == nil {
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
		if infos.UserClient == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		sendMS(nil, "正在请求登录二维码...", nil, 60)

		// 启动登录流程（会阻塞, 直到登录完成或失败）
		go func() {
			qr, err := infos.UserClient.QRLogin(telegram.QrOptions{
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

			src, err := infos.BotClient.UploadFile(png, &telegram.UploadOptions{
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
	me, err := infos.UserClient.GetMe()
	if err != nil {
		log.Printf("获取用户信息失败: %+v", err)
		infos.Mutex.Lock()
		infos.Status.Store(0)
		infos.Mutex.Unlock()
		return nil
	}

	if me.ID == infos.Conf.UserID {
		name := me.FirstName + me.LastName
		if me.Username != "" {
			name = "@" + me.Username
		}
		sendMS(nil, fmt.Sprintf("登录成功! 用户: %s", name), nil)
		infos.Mutex.Lock()
		infos.Status.Store(3)
		infos.Mutex.Unlock()
		return nil
	} else {
		log.Printf("登录失败: 用户ID不匹配, 期望 %d, 实际 %d", infos.Conf.UserID, me.ID)
		if infos.UserClient != nil {
			if err := infos.UserClient.Disconnect(); err != nil {
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
	if err := infos.UserClient.Disconnect(); err != nil {
		log.Printf("UserBot 断开连接失败: %+v", err)
	}
	// 2. 清理磁盘上的 Session 和 Cache 文件（防止因文件损坏导致的下次循环失败）
	cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
	cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})

	// 3. 重置内存状态
	infos.Mutex.Lock()
	infos.UserClient = nil
	infos.Status.Store(0)
	infos.Mutex.Unlock()
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

// wakeTCP 预热连接，防止冷启动卡死
func (infos *Infos) wakeTCP(cate string) error {
	if infos.Client == nil {
		return errors.New("infos.Client 不能为 nil")
	}

	// 设置较短超时
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 最轻量探活 RPC
	latenc, err := infos.Client.Ping(ctx)
	if err != nil {
		if infos.Conf.DeBUG {
			log.Printf("TCP 链路异常, 正在重连: %+v", err)
		}
		// 强制断开
		if err := infos.Client.Disconnect(); err != nil {
			log.Printf("强制断开 TCP 连接失败: %+v", err)
		}
		// 重连
		if err := infos.Client.Connect(); err != nil {
			log.Printf("重连 TCP 失败: %+v", err)
			return err
		}
		// 重连后再次验证，必须使用全新的 context，防止使用已过期的旧 context
		newCtx, newCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer newCancel()
		if value, err := infos.Client.Ping(newCtx); err != nil {
			log.Printf("重连 TCP 后验证失败: %+v", err)
			return err
		} else {
			if infos.Conf.DeBUG {
				log.Printf("TCP 链路已恢复, 延迟: %dms", value.Milliseconds())
			}
			switch cate {
			case "bot":
				infos.TCPStatus.Bot.Latenc = value.Milliseconds()
				infos.TCPStatus.Bot.WakeTime = time.Now()
			case "user":
				infos.TCPStatus.User.Latenc = value.Milliseconds()
				infos.TCPStatus.User.WakeTime = time.Now()
			}
			return nil
		}
	}

	if infos.Conf.DeBUG {
		log.Printf("TCP 链路正常, 延迟: %dms", latenc.Milliseconds())
	}
	switch cate {
	case "bot":
		infos.TCPStatus.Bot.Latenc = latenc.Milliseconds()
		infos.TCPStatus.Bot.WakeTime = time.Now()
	case "user":
		infos.TCPStatus.User.Latenc = latenc.Milliseconds()
		infos.TCPStatus.User.WakeTime = time.Now()
	}
	return nil
}

// botConf 构造 Telegram 客户端所需的通用配置
func botConf(cate string) (conf telegram.ClientConfig) {
	conf = telegram.ClientConfig{
		AppID:        infos.Conf.AppID,
		AppHash:      infos.Conf.AppHash,
		LogLevel:     telegram.LogError,
		Session:      filepath.Join(infos.FilesPath, fmt.Sprintf("%s.session", cate)),
		Cache:        telegram.NewCache(filepath.Join(infos.FilesPath, fmt.Sprintf("%s.cache", cate))),
		CacheSenders: true,
		DeviceConfig: telegram.DeviceConfig{
			DeviceModel:   "Android",
			SystemVersion: "Android 14",
			AppVersion:    "10.14.3",
		},
		FloodHandler: func(err error) bool {
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
			waitUntil := time.Now().Add(time.Duration(wait+1) * time.Second)
			infos.WaitUntil.Store(waitUntil.Unix())
			time.Sleep(time.Duration(wait+1) * time.Second)
			return true
		},
	}
	if infos.Conf.Proxy != "" {
		proxy, err := telegram.ProxyFromURL(infos.Conf.Proxy)
		if err == nil {
			conf.Proxy = proxy
		} else {
			log.Printf("代理地址解析失败: %v", err)
		}
	}
	return conf
}

// list
func (infos *Infos) list(channel string, page, limit int, filter int64, reverse bool, ctx context.Context) (items Items, err error) {
	ch, err := infos.handleChannel(channel)
	if err != nil {
		return items, err
	}
	if page == 1 {
		handleOffset("del", channel, 0)
	}

	offset := handleOffset("get", fmt.Sprintf("%s|%d", channel, page), 0)
	if page > 1 && offset == 0 {
		return items, errors.New("未找到匹配消息")
	}

	ms, err := infos.UserClient.GetMessages(ch, &telegram.SearchOption{
		Limit:   int32(limit),
		Offset:  offset,
		Context: ctx,
		Filter:  &telegram.InputMessagesFilterPhotoVideo{},
	})

	if err != nil {
		return items, err
	}

	lenMs := len(ms)
	switch {
	case lenMs == 0:
		return items, errors.New("未找到匹配消息")
	case lenMs == limit:
		handleOffset("set", fmt.Sprintf("%s|%d", channel, page+1), ms[lenMs-1].ID)
		items.HasMore = true
	}

	if reverse {
		slices.Reverse(ms)
	}

	infos.Mutex.RLock()
	latestCount := infos.LatestCount
	latestID := infos.LatestID
	infos.Mutex.RUnlock()

	mids := make(map[int32]bool, 0)
	maxNum := len(ms) - 1
	for num, m := range ms {
		if m.File == nil {
			continue
		}
		if num <= latestCount && strings.Contains(latestID, strconv.FormatInt(int64(m.ID), 10)) {
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
			if !reverse {
				slices.Reverse(medias)
			}

			src := channel
			count := 0
			for _, media := range medias {
				if IsVideoFile(media.File.Ext) && media.File.Size < filter {
					continue
				}

				sid := strconv.FormatInt(int64(media.ID), 10)
				if strings.Contains(latestID, sid) {
					break
				}

				count++
				src += ":" + sid
				mids[media.ID] = true
				item := handleItem(media)
				items.Item = append(items.Item, item)
			}
			if num == maxNum {
				infos.Mutex.Lock()
				infos.LatestCount = count
				infos.LatestID = src
				infos.Mutex.Unlock()
			}
		} else {
			item := handleItem(m)
			items.Item = append(items.Item, item)
		}
	}
	
	items.ID = channel
	return items, nil
}

// search 在指定频道中搜索关键词并返回匹配的媒体文件列表
func (infos *Infos) search(channel, keywords string, page, limit int, offset int32, filter int64, reverse bool, ctx context.Context) (items Items, err error) {
	ch, err := infos.handleChannel(channel)
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

	ms, err := infos.UserClient.GetMessages(ch, &telegram.SearchOption{
		Query:   keywords,
		Limit:   int32(limit),
		Offset:  offset,
		Context: ctx,
		Filter:  &telegram.InputMessagesFilterPhotoVideo{},
	})

	if err != nil {
		return items, err
	}

	lenMs := len(ms)
	switch {
	case lenMs == 0:
		return items, errors.New("未找到匹配消息")
	case lenMs == limit:
		key := fmt.Sprintf("%s|%s|%d", channel, keywords, page+1)
		handleOffset("set", key, ms[lenMs-1].ID)
		items.HasMore = true
	}

	if reverse {
		slices.Reverse(ms)
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
	return items, nil
}

// selectClient 根据当前网络延迟选择最佳客户端
func (infos *Infos) handleMs(cid int64, mid int32, cate string) (result string, src telegram.NewMessage, err error) {
	var wakeTime time.Time

	// 1. 选择下载客户端，并提取对应的唤醒时间
	if cate == "user" && infos.Status.Load() == 3 {
		infos.Client = infos.UserClient
		wakeTime = infos.TCPStatus.User.WakeTime
	} else {
		cate = "bot"
		infos.Client = infos.BotClient
		wakeTime = infos.TCPStatus.Bot.WakeTime
	}
	result = cate

	// 2. 统一处理 TCP 链路检查与唤醒逻辑（彻底去除了重复代码）
	if time.Since(wakeTime).Minutes() > 30 {
		if err = infos.wakeTCP(cate); err != nil {
			log.Printf("唤醒 TCP 连接失败: %+v", err)
			return "", telegram.NewMessage{}, err
		}
	} else if infos.Conf.DeBUG {
		diff := time.Since(wakeTime)
		minutes := int(diff.Minutes())
		seconds := int(diff.Seconds()) % 60
		if minutes != 0 {
			timeStr := fmt.Sprintf("%02d分%02d秒", minutes, seconds)
			timeStr = strings.TrimPrefix(timeStr, "0")
			log.Printf("TCP 链路正常, %s前唤醒", timeStr)
		} else {
			log.Printf("TCP 链路正常, %d秒前唤醒", seconds)
		}
	}

	// 3. 获取消息
	ms, err := infos.Client.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
	if err != nil || len(ms) == 0 {
		err = fmt.Errorf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms))
		log.Print(err.Error())
		return result, telegram.NewMessage{}, err
	}

	src = ms[0]
	if !src.IsMedia() {
		err = fmt.Errorf("消息不包含媒体: cid=%d, mid=%d", cid, mid)
		log.Print(err.Error())
		return result, telegram.NewMessage{}, err
	}

	return result, src, nil
}

// handleChannel 处理频道ID, 返回 InputPeer
func (infos *Infos) handleChannel(channel string) (result telegram.InputPeer, err error) {
	infos.Mutex.RLock()
	result, ok := infos.ChannelID[channel]
	infos.Mutex.RUnlock()
	if !ok {
		result, err = infos.UserClient.ResolvePeer(channel)
		if err != nil {
			log.Printf("频道解析失败: %+v", err)
			return result, err
		}
		infos.Mutex.Lock()
		infos.ChannelID[channel] = result
		infos.Mutex.Unlock()
	}
	return result, nil
}

// handleComments 处理评论消息，返回评论消息列表
func (infos *Infos) handleComments(mid int32, ms *[]telegram.NewMessage, reverse bool) error {
	if len(*ms) == 0 {
		return errors.New("未找到消息")
	}
	src := (*ms)[0]
	if src.Message.Replies != nil && src.Message.Replies.ChannelID != 0 {
		discussionID := src.Message.Replies.ChannelID
		ch, err := infos.handleChannel(src.Channel.Username)
		if err != nil {
			log.Printf("获取频道失败: %+v", err)
			return err
		}
		results, err := infos.UserClient.MessagesGetReplies(&telegram.MessagesGetRepliesParams{
			Peer:  ch,
			Limit: 100,
			MsgID: mid,
		})

		if err != nil {
			log.Printf("获取评论消息失败: cid=%d, mid=%d, err=%v", src.Channel.ID, mid, err)
			return err
		}

		// 从 MessagesGetReplies 的结果中提取原始消息列表
		var newMs []telegram.Message
		switch v := results.(type) {
		case *telegram.MessagesMessagesSlice:
			newMs = v.Messages
		case *telegram.MessagesChannelMessages:
			newMs = v.Messages
		case *telegram.MessagesMessagesObj:
			newMs = v.Messages
		default:
			log.Printf("收到未知的底层具体类型: %T, %v", v, v)
		}

		// PackMessages 将 []telegram.Message 转为 []*telegram.NewMessage，
		// 然后按 commentIDSet 过滤，设置 Chat.ID 后追加到 ms
		if reverse {
			slices.Reverse(newMs)
		}
		for _, nm := range telegram.PackMessages(infos.UserClient, newMs) {
			if !nm.IsMedia() {
				continue
			}
			nm.Chat.ID = discussionID
			*ms = append(*ms, *nm)
		}
	}
	return nil
}

// handleLinks 处理消息媒体, 返回直链
func handleLinks(res HackLink, src telegram.NewMessage) (link string) {
	link = fmt.Sprintf("%s/stream?cid=%v&mid=%d&cate=user", strings.TrimSuffix(infos.Conf.Site, "/"), src.ChatID(), src.ID)

	if infos.Conf.Password != "" {
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
	src = strings.ReplaceAll(src, "_", "")
	src = strings.Join(strings.Fields(src), " ")
	name := strings.TrimSpace(m.File.Name)
	name = strings.ReplaceAll(name, "_", "")
	name = strings.Join(strings.Fields(name), " ")

	item.Ext = m.File.Ext
	item.Src = src
	item.Name = name
	item.Size = m.File.Size
	item.CID = m.Channel.ID
	item.MID = m.ID
	if m.Message != nil {
		item.Date = m.Message.Date
		item.GID = m.Message.GroupedID
	}
	return item
}
