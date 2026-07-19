package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// streamPathRe 从 /stream/{mid}/{name} 形式的路径中提取消息 ID, handleParams 每次请求都会用到, 提升为包级变量避免重复编译
var streamPathRe = regexp.MustCompile(`/stream/(\d+)/[a-zA-Z0-9]+`)

// handleMain 是 HTTP 服务的主分发函数, 根据路径路由到不同的处理器
func handleMain(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// 标准化路径处理, 移除尾部斜杠
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	switch {
	case path == "/":
		// 返回服务器状态 JSON
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		conf := infos.Conf.Load()
		content := map[string]any{
			"版本":   version,
			"域名":   conf.Site,
			"端口":   conf.Port,
			"缓存":   formatFileSize(conf.MaxSize),
			"并发":   conf.Workers,
			"运行时间": handleTime(uint64(time.Since(startTime).Seconds())),
		}
		if err := json.NewEncoder(w).Encode(content); err != nil {
			log.Printf("发送网页失败: %+v", err)
		}
		return
	case path == "/pic":
		handlePic(w, r)
		return
	case path == "/link":
		// 处理链接直链提取并跳转
		handleLink(w, r)
		return
	case path == "/list":
		handleList(w, r)
		return
	case path == "/search":
		// 处理搜索
		handleSearch(w, r)
		return
	case path == "/sources":
		handleSources(w, r)
		return
	case path == "/comments":
		handleComments(w, r)
		return
	case strings.HasPrefix(path, "/stream"):
		// 处理文件分片流式下载（串流播放）核心接口
		handleStream(w, r)
		return
	default:
		// 404
		http.NotFound(w, r)
		return
	}
}

// handleParams 解析流式下载请求参数
func handleParams(r *http.Request) (result Params, err error) {
	params := r.URL.Query()
	if err = checkPass(params); err != nil {
		return result, err
	}

	result.Pass = params.Get("key")
	result.Hash = params.Get("hash")
	result.Cate = params.Get("cate")
	result.Link = params.Get("link")
	result.Keywords = params.Get("keywords")

	values := strings.Split(params.Get("cname"), ",")
	result.Channels = make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result.Channels = append(result.Channels, "@"+strings.TrimLeft(value, "@"))
	}

	values = strings.Split(params.Get("filter"), ",")
	result.Filters = make([]int64, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		size, err := convertSize(value)
		if err != nil {
			size = 0
		}
		result.Filters = append(result.Filters, size)
	}

	page, err := strconv.Atoi(params.Get("page"))
	if err != nil || page < 0 {
		page = 1
	}
	result.Page = page

	limit, err := strconv.Atoi(params.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 20
	}
	result.Limit = int(limit)

	// ParseInt 出错时返回值已经是 0, 无需再显式判断赋值
	offset, _ := strconv.ParseInt(params.Get("offset"), 10, 32)
	result.Offset = int32(offset)

	cid, err := strconv.ParseInt(params.Get("cid"), 10, 64)
	if err != nil {
		cid = 0
	}
	// cid 未提供或显式传 0 时都应回退到 /channel 配置的默认频道, 而不仅仅是解析成功的情形
	if channelID := infos.Conf.Load().ChannelID; cid == 0 && channelID != 0 {
		cid = channelID
	}
	result.CID = cid

	uid, err := strconv.ParseInt(params.Get("uid"), 10, 64)
	if err != nil {
		uid = 0
	}
	result.UID = uid

	mid, err := strconv.ParseInt(params.Get("mid"), 10, 32)
	if err != nil || mid == 0 {
		matches := streamPathRe.FindStringSubmatch(r.URL.Path)
		if len(matches) == 2 {
			mid, err = strconv.ParseInt(matches[1], 10, 32)
			if err != nil {
				mid = 0
			}
		} else {
			mid = 0
		}
	}
	result.MID = int32(mid)

	// ParseBool 出错时返回值已经是 false, 无需再显式判断赋值
	reverse, _ := strconv.ParseBool(params.Get("reverse"))
	result.Reverse = reverse

	return result, nil
}

// handleRanHeader 解析 HTTP Range 头
func handleRanHeader(src string, size int64) (start, end int64) {
	if src == "" {
		return 0, size - 1
	}
	src = strings.TrimSpace(strings.TrimPrefix(src, "bytes="))
	parts := strings.SplitN(src, "-", 2)
	if len(parts) == 2 {
		if parts[0] == "" {
			suffixLength, err := strconv.ParseInt(parts[1], 10, 64)
			if err == nil && suffixLength > 0 {
				start = size - suffixLength
				end = size - 1
				if start < 0 {
					start = 0
				}
			} else {
				start, end = 0, size-1
			}
		} else {
			var err error
			start, err = strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				start = 0
			}
			if parts[1] != "" {
				end, err = strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					end = size - 1
				}
			} else {
				end = size - 1
			}
		}
	} else {
		start, end = 0, size-1
	}
	if end >= size {
		end = size - 1
	}
	if start > end {
		start = end
	}
	return start, end
}

func handlePic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, fmt.Sprintf("不支持的请求方法: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}
	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if params.CID == 0 && len(params.Channels) == 0 {
		http.Error(w, "频道信息无效", http.StatusBadRequest)
		return
	}
	if params.MID == 0 {
		http.Error(w, "消息ID无效", http.StatusBadRequest)
		return
	}

	param := HandleMs{
		CID:    params.CID,
		CNames: params.Channels,
		MIDs:   []int32{params.MID},
		Ctx:    r.Context(),
		Cate:   params.Cate,
		Limit:  1,
	}

	msCache, err := infos.handleMs(param)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	ms := msCache.snapshot()
	cate := msCache.Cate
	client := infos.clientByCate(cate)

	if len(ms) == 0 {
		http.Error(w, "未获取到消息", http.StatusBadRequest)
		return
	}
	if client == nil {
		http.Error(w, "对应客户端未就绪", http.StatusServiceUnavailable)
		return
	}

	src := ms[0]
	defer infos.tcpStat(cate).touch()

	// 从媒体中查找最大的 PhotoSize
	var actualThumb telegram.PhotoSize
	var maxSize int32

	updateMax := func(s telegram.PhotoSize) {
		switch sz := s.(type) {
		case *telegram.PhotoSizeObj:
			if sz.Size > maxSize {
				maxSize = sz.Size
				actualThumb = sz
			}
		case *telegram.PhotoSizeProgressive:
			var sMax int32
			for _, m := range sz.Sizes {
				if m > sMax {
					sMax = m
				}
			}
			if sMax > maxSize {
				maxSize = sMax
				actualThumb = sz
			}
		}
	}

	if !src.IsMedia() {
		http.Error(w, "消息不包含媒体", http.StatusBadRequest)
		return
	}
	switch m := src.Media().(type) {
	case *telegram.MessageMediaPhoto:
		if p, ok := m.Photo.(*telegram.PhotoObj); ok {
			for _, s := range p.Sizes {
				updateMax(s)
			}
		}
	case *telegram.MessageMediaDocument:
		if d, ok := m.Document.(*telegram.DocumentObj); ok {
			for _, s := range d.Thumbs {
				updateMax(s)
			}
		}
	}

	if actualThumb == nil {
		http.Error(w, "未找到缩略图", http.StatusNotFound)
		return
	}

	clientIP := GetClientIP(r)
	log.Printf("正在处理来自 %s 的请求, 开始下载封面, cid=%d, mid=%d, name=%s", clientIP, params.CID, params.MID, src.File.Name)

	buf := new(bytes.Buffer)
	maxCount := 2
	success := false
	for count := 1; count <= maxCount; count++ {
		version := msCache.Version.Load()
		_, err = client.DownloadMedia(src.Media(), &telegram.DownloadOptions{
			ThumbOnly: true,
			ThumbSize: actualThumb,
			Buffer:    buf,
			Ctx:       r.Context(),
		})
		if err != nil {
			if telegram.MatchError(err, "FILE_REFERENCE_EXPIRED") {
				if infos.Conf.Load().DeBUG {
					log.Printf("引用过期, 正在尝试刷新文件引用, cid=%d, mid=%d, name=%s", params.CID, params.MID, src.File.Name)
				}
				src, err = infos.refreshMs(client, version, param, msCache)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				buf.Reset()
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			success = true
			break
		}
	}
	if !success {
		http.Error(w, "下载封面失败: 文件引用持续过期", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	if n, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
	}
}

// handleList 处理来自 HTTP 的文件列表请求
func handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, fmt.Sprintf("不支持的请求方法: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}
	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var items struct {
		HasMore bool    `json:"more"`
		Items   []Items `json:"items"`
	}
	items.Items = make([]Items, 0, len(params.Channels))
	lenFilters := len(params.Filters)
	for num, channel := range params.Channels {
		filter := int64(0)
		if num < lenFilters {
			filter = params.Filters[num]
		}
		item, err := infos.list(channel, params.Page, params.Limit, params.Offset, filter, params.Reverse, r.Context())
		if err != nil {
			log.Printf("获取频道 %s 的文件列表失败: %+v", channel, err)
			continue
		}
		if !items.HasMore {
			items.HasMore = item.HasMore
		}
		items.Items = append(items.Items, item)
	}
	content, err := json.Marshal(items)
	if err != nil {
		log.Printf("JSON序列化失败: %+v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	n, err := w.Write(content)
	if err != nil {
		log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
		return
	}
}

// handleLink 处理链接提取请求, 将 Telegram 消息链接转换为直链下载地址并执行重定向
func handleLink(w http.ResponseWriter, r *http.Request) {
	res := HackLink{
		Ctx: r.Context(),
	}
	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	src := params.Link
	if src == "" || !strings.HasPrefix(src, "http") {
		http.Error(w, "无效的链接", http.StatusBadRequest)
		return
	}

	clientIP := GetClientIP(r)
	log.Printf("正在处理来自 %s 的请求, 开始提取直链, link=%s", clientIP, src)

	// 3. 正则匹配并解析链接
	res.Matches = telegramLinkRe.FindAllStringSubmatch(src, -1)
	res.UID = params.UID
	res.Pass = params.Pass
	res.Hash = params.Hash
	res.Offset = params.Offset

	items, err := hackLinks(res)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(items) == 0 {
		http.Error(w, "未找到可下载的媒体", http.StatusNotFound)
		return
	}

	sortItems(items, params.Reverse)

	result, err := json.Marshal(items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if n, err := w.Write(result); err != nil {
		log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
	}
}

// handleStream 处理来自 HTTP 的文件流式读取请求
// 该函数实现了 Range 分段下载支持, 允许像播放普通 mp4 文件一样拖动进度条
func handleStream(w http.ResponseWriter, r *http.Request) {
	// 0. 检验 HTTP 请求类型, 过滤非法请求
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, fmt.Sprintf("不支持的请求方法: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}

	// 1-2. 获取 URL 参数、完成身份校验、解析频道 ID 和消息 ID
	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if params.CID == 0 && len(params.Channels) == 0 {
		http.Error(w, "频道信息无效", http.StatusBadRequest)
		return
	}
	if params.MID == 0 {
		http.Error(w, "消息ID无效", http.StatusBadRequest)
		return
	}

	param := HandleMs{
		CID:    params.CID,
		CNames: params.Channels,
		MIDs:   []int32{params.MID},
		Ctx:    r.Context(),
		Cate:   params.Cate,
		Limit:  1,
	}

	msCache, err := infos.handleMs(param)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	ms := msCache.snapshot()
	cate := msCache.Cate
	client := infos.clientByCate(cate)

	if len(ms) == 0 {
		http.Error(w, "未获取到消息", http.StatusBadRequest)
		return
	}
	if client == nil {
		http.Error(w, "对应客户端未就绪", http.StatusServiceUnavailable)
		return
	}

	src := ms[0]
	if src.File == nil {
		http.Error(w, "消息不是有效的媒体文件", http.StatusBadRequest)
		return
	}
	size := src.File.Size
	fileName := src.File.Name
	chunkSize := 1 * 1024 * 1024
	// 整个下载分支使用同一份 Workers 快照, 避免判断分支与实际下载并发数在热重载后不一致
	workers := infos.Conf.Load().Workers
	if size < int64(chunkSize*workers) {
		// 与下方大文件分支保持一致的响应头, 否则浏览器/播放器只能靠内容嗅探猜测 Content-Type,
		// 对 mkv/ts 等容器并不可靠, 下载模式也拿不到正确文件名
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", handleMediaCate(fileName))
		disposition := "inline"
		if r.URL.Query().Get("download") == "true" {
			disposition = "attachment"
		}
		w.Header().Set("Content-Disposition", contentDisposition(disposition, fileName))

		// 与大文件分支一致地解析 Range 头, 避免小文件分支明明声明了 Accept-Ranges 却始终整体返回 200,
		// 导致播放器/下载工具的 seek、断点续传在小文件上失效
		ranHeader := r.Header.Get("Range")
		start, end := handleRanHeader(ranHeader, size)

		// HEAD 请求只需要头部信息, 文件大小已从消息元数据中获知, 无需真的向 Telegram 发起下载
		if r.Method == http.MethodHead {
			if ranHeader == "" {
				w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			} else {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
				w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			}
			return
		}

		clientIP := GetClientIP(r)
		log.Printf("正在处理来自 %s 的请求, 开始下载, cid=%d, mid=%d, name=%s", clientIP, params.CID, params.MID, fileName)
		buf := new(bytes.Buffer)
		maxCount := 2
		success := false
		for count := 1; count <= maxCount; count++ {
			version := msCache.Version.Load()
			_, err = client.DownloadMedia(src.Media(), &telegram.DownloadOptions{
				Buffer:    buf,
				ChunkSize: int32(chunkSize),
				Threads:   workers,
				Ctx:       r.Context(),
			})
			if err != nil {
				if telegram.MatchError(err, "FILE_REFERENCE_EXPIRED") {
					if infos.Conf.Load().DeBUG {
						log.Printf("引用过期, 正在尝试刷新文件引用, cid=%d, mid=%d, name=%s", params.CID, params.MID, src.File.Name)
					}
					src, err = infos.refreshMs(client, version, param, msCache)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					buf.Reset()
				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			} else {
				success = true
				break
			}
		}
		if !success {
			http.Error(w, "下载失败: 文件引用持续过期", http.StatusInternalServerError)
			return
		}

		content := buf.Bytes()
		if ranHeader == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		} else {
			// 下载到的内容以实际大小为准做边界保护, 防止 Telegram 返回的字节数与消息元数据里的 size 有出入导致越界
			rangeStart, rangeEnd := start, end
			if rangeEnd >= int64(len(content)) {
				rangeEnd = int64(len(content)) - 1
			}
			if rangeStart > rangeEnd {
				rangeStart = rangeEnd
			}
			content = content[rangeStart : rangeEnd+1]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeStart, rangeEnd, size))
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusPartialContent)
		}
		if n, err := w.Write(content); err != nil {
			log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
		}
	} else {
		// 创建新的 Stream 流管理对象
		stream := newStream(r.Context(), client, src.Media(), workers, params.MID, params.CID, src.File.Size, fileName)
		stream.Ms = ms

		// 如果是转发的消息, 重定向源频道以确保分片下载稳定性
		if src.Message.FwdFrom != nil {
			if ch, ok := src.Message.FwdFrom.FromID.(*telegram.PeerChannel); ok {
				stream.CID = ch.ChannelID
				stream.MID = src.Message.FwdFrom.ChannelPost
			}
		}

		// 6. 设置 HTTP 响应头
		w.Header().Set("Accept-Ranges", "bytes") // 启用 Range 支持
		w.Header().Set("Content-Type", handleMediaCate(fileName))

		disposition := "inline"
		if r.URL.Query().Get("download") == "true" {
			disposition = "attachment" // 附件模式下载
		}
		w.Header().Set("Content-Disposition", contentDisposition(disposition, fileName))

		// 7. 处理 HTTP Range 请求（分段读取的核心逻辑）
		ranHeader := r.Header.Get("Range")
		start, end := handleRanHeader(ranHeader, size)

		if ranHeader == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusOK)
		} else {
			contentLength := end - start + 1
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
			w.WriteHeader(http.StatusPartialContent)
		}

		// 提前发送 Header，重置客户端(ExoPlayer)连接超时倒计时
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		// 如果是 HEAD 请求, 只返回首部信息后提早结束避免开启流媒体下载协程
		if r.Method == http.MethodHead {
			return
		}

		clientIP := GetClientIP(r)
		log.Printf("正在处理来自 %s 的请求, 开始下载, cid=%d, mid=%d, name=%s, start=%d, end=%d", clientIP, params.CID, params.MID, fileName, start, end)

		// 缓存逻辑：检查头部/尾部缓存是否命中, 并决定实际下载起点
		stream.HeadSize, stream.TailSize = mediaCacheSizes(size, stream.MaxCacheSize)

		// 启动并发下载协程
		go stream.start(start, end)
		defer func() {
			if stream.Version.Load() > 0 {
				// stream.Ms 由 stream.refresh() 在 stream.Mutex 保护下并发写入
				// （下载协程可能在客户端已断开、本函数已经在准备 return 时仍在后台刷新引用），
				// 这里必须持同一把锁读取，否则可能读到撕裂的 slice header 并写入共享缓存 msCache.Mes
				stream.Mutex.Lock()
				ms := stream.Ms
				stream.Mutex.Unlock()

				infos.Mutex.Lock()
				msCache.Mes = ms
				msCache.Time = time.Now()
				msCache.Version.Add(1)
				infos.Mutex.Unlock()
				if infos.Conf.Load().DeBUG {
					log.Printf("缓存数据更新, cid=%d, mid=%d, name=%s, version=%d", params.CID, params.MID, fileName, msCache.Version.Load())
				}
			}

			// 异步清理：不阻塞当前请求 goroutine 返回，使新请求能立即被处理
			go stream.clean()

			// TCP 正常 → 记录唤醒时间，下次 30 分钟内跳过 wakeTCP
			// TCP 断开 → 清零，下次请求强制触发 wakeTCP 探活重连
			if !stream.TCPDead.Load() {
				infos.tcpStat(cate).touch()
			} else {
				infos.tcpStat(cate).reset()
			}
		}()

		// 10. 循环从下载管道读取分片并写入 HTTP 响应体
		if r.Method == http.MethodGet {
			// 首个分片给更长超时，容忍冷启动 Telegram 连接重建延迟
			timer := time.NewTimer(60 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-r.Context().Done():
					// 客户端断开连接（如浏览器关闭或拖动进度条导致旧请求作废）
					if infos.Conf.Load().DeBUG {
						log.Printf("流式传输文件已取消: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
					}
					return
				case task := <-stream.Tasks:
					// 读取一个下载好的分片任务
					if task == nil {
						log.Printf("流式传输文件出错: cid=%d, mid=%d, name=%s, error=任务为空", params.CID, params.MID, fileName)
						continue
					}

					if task.Error != nil {
						log.Printf("切片下载出错: cid=%d, mid=%d, start=%d, end=%d, name=%s, error=%+v", params.CID, params.MID, task.ContentStart, task.ContentEnd, fileName, task.Error)
						return
					}
					// 等待任务完成或者客户端断开
					select {
					case <-r.Context().Done():
						if infos.Conf.Load().DeBUG {
							log.Printf("流式传输文件已取消: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
						}
						return
					case <-timer.C:
						log.Printf("流式传输文件超时: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
						return
					case content, ok := <-task.Content:
						if !ok {
							if infos.Conf.Load().DeBUG {
								log.Printf("流式传输文件已完成: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
							}
							return
						}

						// 写入响应
						if len(content) > 0 {
							if _, err := w.Write(content); err != nil {
								log.Printf("写入文件流时出错: cid=%d, mid=%d, name=%s, err=%v", params.CID, params.MID, fileName, err)
								return
							}
						}
						// 检查是否已经写完当前请求的所有范围
						if task.ContentEnd >= end {
							log.Printf("流式传输文件已完成: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
							return
						}
						task = nil
						content = nil
						timer.Reset(30 * time.Second)
					}
				case <-timer.C:
					log.Printf("流式传输文件超时: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
					return
				}
			}
		}
	}
}

// handleSources 获取相册中的所有文件
func handleSources(w http.ResponseWriter, r *http.Request) {
	// 0. 检验 HTTP 请求类型, 过滤非法请求
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, fmt.Sprintf("不支持的请求方法: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}

	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if params.CID == 0 && len(params.Channels) == 0 {
		http.Error(w, "频道信息无效", http.StatusBadRequest)
		return
	}

	if params.MID == 0 {
		http.Error(w, "消息ID无效", http.StatusBadRequest)
		return
	}

	param := HandleMs{
		CID:    params.CID,
		CNames: params.Channels,
		MIDs:   []int32{params.MID},
		Ctx:    r.Context(),
		Cate:   params.Cate,
		Limit:  params.Limit,
	}

	msCache, err := infos.handleMs(param)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	resources := msCache.snapshot()
	if len(resources) == 0 {
		http.Error(w, "未获取到消息", http.StatusBadRequest)
		return
	}

	src := resources[0]
	ms, err := src.GetMediaGroup()
	if err != nil {
		log.Printf("提取媒体组错误: %+v", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if len(ms) == 0 {
		http.Error(w, "未获取到媒体组", http.StatusNotFound)
		return
	}

	items := Items{
		Channel: strings.TrimSpace(src.Channel.Title),
		ID:      src.Channel.Username,
		HasMore: false,
		Item:    make([]Item, 0, len(ms)),
	}
	filter := int64(0)
	if len(params.Filters) > 0 {
		filter = params.Filters[0]
	}
	for _, m := range ms {
		if IsVideoFile(m.File.Ext) && m.File.Size < filter {
			continue
		}
		item := handleItem(m)
		items.Item = append(items.Item, item)
	}
	sortItems(items.Item, params.Reverse)

	content, err := json.Marshal(items)
	if err != nil {
		log.Printf("序列化相册媒体组失败: %+v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	n, err := w.Write(content)
	if err != nil {
		log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
		return
	}
}

// handleSearch 处理搜索请求, 并发搜索多个频道
func handleSearch(w http.ResponseWriter, r *http.Request) {
	if infos.UserClient.Load() == nil {
		http.Error(w, "userBot 未登录, 无法使用搜索功能", http.StatusUnauthorized)
		return
	}
	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	src := params.Keywords
	if src == "" {
		http.Error(w, "缺少关键词", http.StatusBadRequest)
		return
	}
	words := strings.Split(src, ",")
	page := params.Page
	offset := params.Offset
	limit := params.Limit

	clientIP := GetClientIP(r)
	log.Printf("正在处理来自 %s 的请求, 开始搜索, page=%d, offset=%d, limit=%d, keywords=%s", clientIP, page, offset, limit, src)

	// 整个搜索请求使用同一份配置快照, Conf 已是原子指针, 无需再靠 Mutex 保护读取
	conf := infos.Conf.Load()
	channels := make([]string, 0, len(conf.Channels))
	if len(params.Channels) == 0 {
		channels = append(channels, conf.Channels...)
	} else {
		channels = append(channels, params.Channels...)
	}

	results := make(chan Items, len(channels))
	var workerPool sync.WaitGroup

	maxCount := int64(2 * conf.Workers)
	if maxCount == 0 {
		maxCount = 3
	}

	lenWords := len(words)
	lenFilters := len(params.Filters)
	for num, channel := range channels {
		infos.Cond.L.Lock()
		for searchCount.Load() >= maxCount {
			infos.Cond.Wait()
		}
		// 必须在同一把锁内完成"检查未超限"与"计数加一", 否则并发的多个请求可能都通过检查后
		// 才各自加一, 导致 searchCount 短暂超过 maxCount, 限流失效
		searchCount.Add(1)
		infos.Cond.L.Unlock()

		workerPool.Add(1)
		channel = strings.TrimLeft(channel, "@")
		channel = fmt.Sprintf("@%s", channel)
		go func(channel string) {
			defer func() {
				workerPool.Done()
				searchCount.Add(-1)
				infos.Cond.L.Lock()
				infos.Cond.Broadcast()
				infos.Cond.L.Unlock()
			}()

			filter := int64(0)
			if num < lenFilters {
				filter = params.Filters[num]
			}
			keywords := words[0]
			if num < lenWords {
				keywords = words[num]
			}

			keywords = strings.TrimSpace(keywords)
			if keywords == "" || keywords == "#" {
				return
			}
			result, err := infos.search(channel, keywords, page, limit, int32(offset), filter, params.Reverse, r.Context())
			if err != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case results <- result:
			}
		}(channel)
	}

	// 启动一个协程，在所有任务完成后关闭通道
	go func() {
		workerPool.Wait()
		close(results)
	}()

	var items struct {
		HasMore bool    `json:"more"`
		Items   []Items `json:"items"`
	}

	items.Items = make([]Items, 0, len(channels))
	defer func() {
		content, err := json.Marshal(items)
		if err != nil {
			log.Printf("JSON序列化失败: %+v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		n, err := w.Write(content)
		if err != nil {
			log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
			return
		}
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case result, ok := <-results:
			if !ok {
				return
			}
			if len(result.Item) > 0 {
				items.Items = append(items.Items, result)
			}
			if !items.HasMore && result.HasMore {
				items.HasMore = result.HasMore
			}
		}
	}
}

// handleComments 处理评论消息，返回评论消息列表
func handleComments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, fmt.Sprintf("不支持的请求方法: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}
	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if params.MID == 0 {
		http.Error(w, "消息ID无效", http.StatusBadRequest)
		return
	}
	if params.CID == 0 && len(params.Channels) == 0 {
		http.Error(w, "频道信息无效", http.StatusBadRequest)
		return
	}

	param := HandleMs{
		CID:    params.CID,
		CNames: params.Channels,
		MIDs:   []int32{params.MID},
		Ctx:    r.Context(),
		Cate:   "user",
		Limit:  params.Limit,
	}

	msCache, err := infos.handleMs(param)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	ms := msCache.snapshot()
	if len(ms) == 0 {
		http.Error(w, "未获取到消息", http.StatusNotFound)
		return
	}
	hasMore, err := infos.handleComments(params.MID, params.Offset, params.Page, params.Limit, &ms)
	if err != nil {
		http.Error(w, "获取评论失败", http.StatusInternalServerError)
		return
	}

	var result struct {
		HasMore bool    `json:"more"`
		Items   []Items `json:"items"`
	}
	result.Items = make([]Items, 0, 1)
	items := Items{
		HasMore: hasMore,
	}
	result.HasMore = items.HasMore
	filter := int64(0)
	if len(params.Filters) > 0 {
		filter = params.Filters[0]
	}
	for _, m := range ms {
		// ms[0] 是 handleMs 直接取来的锚点消息，不像评论区回复那样经过 IsMedia 过滤，
		// 锚点帖子本身可能是纯文本/无媒体消息或频道解析失败，m.File/m.Channel 此时为 nil，
		// 必须先判空再访问 —— handleItem 内部会直接读 m.Channel.ID，同样需要先排除掉
		if m.File == nil || m.Channel == nil {
			continue
		}
		if IsVideoFile(m.File.Ext) && m.File.Size < filter {
			continue
		}

		if items.Channel == "" {
			items.Channel = m.Channel.Title
			items.ID = m.Channel.Username
		}
		item := handleItem(m)
		items.Item = append(items.Item, item)
	}
	sortItems(items.Item, params.Reverse)
	result.Items = append(result.Items, items)

	content, err := json.Marshal(result)
	if err != nil {
		log.Printf("JSON序列化失败: %+v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	n, err := w.Write(content)
	if err != nil {
		log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
		return
	}

}

// handleMediaCate 根据文件扩展名返回对应的 MIME 类型
func handleMediaCate(fileName string) string {
	lowerFileName := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lowerFileName, ".webm"):
		return "video/webm"
	case strings.HasSuffix(lowerFileName, ".avi"):
		return "video/x-msvideo"
	case strings.HasSuffix(lowerFileName, ".wmv"):
		return "video/x-ms-wmv"
	case strings.HasSuffix(lowerFileName, ".flv"):
		return "video/x-flv"
	case strings.HasSuffix(lowerFileName, ".mov"):
		return "video/quicktime"
	case strings.HasSuffix(lowerFileName, ".mkv"):
		return "video/x-matroska"
	case strings.HasSuffix(lowerFileName, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(lowerFileName, ".mpeg"), strings.HasSuffix(lowerFileName, ".mpg"):
		return "video/mpeg"
	case strings.HasSuffix(lowerFileName, ".3gpp"), strings.HasSuffix(lowerFileName, ".3gp"):
		return "video/3gpp"
	case strings.HasSuffix(lowerFileName, ".mp4"), strings.HasSuffix(lowerFileName, ".m4s"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

// hackLinks 是链接解析的核心逻辑，负责将 t.me 链接映射到具体的媒体消息并生成本程序的流地址
func hackLinks(res HackLink) (items []Item, errs error) {
	for _, match := range res.Matches {
		var username string
		var cid int64 // 用于 ResolvePeer 的标识项
		var mid int32 // 消息 ID

		// 1. 解析 Chat ID 或 Username
		if match[2] != "" {
			// 如果是 c/(\d+)，代表私有频道链接，需要给 ID 补充前缀 -100
			value, err := strconv.ParseInt("-100"+match[2], 10, 64)
			if err != nil {
				log.Printf("解析频道ID失败: %+v", err)
				if res.M != nil {
					if _, err := res.M.Reply("解析频道ID失败"); err != nil {
						log.Printf("发送消息失败: %+v", err)
					}
				}
				continue
			}
			cid = value
		} else {
			// 否则匹配的是公开频道的 username
			channelInfo, err := infos.handleChannel(match[3])
			if err != nil {
				log.Printf("获取频道 %s 信息失败: %+v", match[3], err)
				continue
			}
			cid = channelInfo.CID
			username = channelInfo.UserName
		}

		// 2. 解析消息偏移 ID
		value, err := strconv.ParseInt(match[4], 10, 32)
		if err != nil {
			errs = errors.Join(errs, err)
			log.Printf("解析消息ID失败: %+v", err)
			continue
		}

		mid = int32(value)

		// 3. 使用 UserBot 客户端尝试获取目标消息
		param := HandleMs{
			CID:    cid,
			CNames: []string{username},
			MIDs:   []int32{mid},
			Ctx:    res.Ctx,
			Cate:   "user",
			Limit:  1,
		}

		msCache, err := infos.handleMs(param)
		if err != nil {
			log.Printf("获取消息失败: cid=%v, mid=%d, err=%+v", cid, mid, err)
			errs = errors.Join(errs, err)
			continue
		}
		ms := msCache.snapshot()

		if len(ms) == 0 {
			log.Printf("未获取到消息: cid=%v, mid=%d", cid, mid)
			err = errors.New("未获取到消息")
			errs = errors.Join(errs, err)
			continue
		}

		// 4. 处理链接中的评论 (comment) 逻辑
		if match[5] != "" {
			if _, err := infos.handleComments(mid, res.Offset, 1, 0, &ms); err != nil {
				log.Printf("获取评论失败: cid=%v, mid=%d, err=%+v", cid, mid, err)
				errs = errors.Join(errs, err)
				continue
			}
		}

		items = make([]Item, 0, len(ms))
		for _, src := range ms {
			if src.Message.GroupedID != 0 {
				medias, err := src.GetMediaGroup()
				if err != nil {
					log.Printf("提取媒体组错误: %+v", err)
				}
				for _, media := range medias {
					items = append(items, handleItem(media))
				}
			} else {
				if !src.IsMedia() {
					log.Printf("消息不包含媒体: cid=%v, mid=%d", cid, mid)
					continue
				}
				items = append(items, handleItem(src))
			}
		}
	}

	if len(items) == 0 {
		errs = errors.Join(errs, errors.New("未获取到有效链接"))
	}

	if errs != nil && res.M != nil {
		if _, err := res.M.Reply(errs.Error()); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil, errs
	}
	return items, nil
}
