package main

import (
	"bytes"
	"context"
	"encoding/json"
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
		content := map[string]any{
			"版本":   version,
			"域名":   infos.Conf.Site,
			"端口":   infos.Conf.Port,
			"缓存":   formatFileSize(infos.Conf.MaxSize),
			"并发":   infos.Conf.Workers,
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

	result.Cate = params.Get("cate")
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

	offset, err := strconv.ParseInt(params.Get("offset"), 10, 32)
	if err != nil || offset == 0 {
		offset = 0
	}
	result.Offset = int32(offset)

	cid, err := strconv.ParseInt(params.Get("cid"), 10, 64)
	if err == nil {
		if cid == 0 && infos.Conf.ChannelID != 0 {
			cid = infos.Conf.ChannelID
		}
	} else {
		cid = 0
	}
	result.CID = cid

	mid, err := strconv.ParseInt(params.Get("mid"), 10, 32)
	if err != nil || mid == 0 {
		re := regexp.MustCompile(`/stream/(\d+)/[a-zA-Z0-9]+`)
		matches := re.FindStringSubmatch(r.URL.Path)
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

	result.Filter = convertSize(params.Get("filter"))
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
	if params.CID == 0 {
		http.Error(w, "频道ID无效", http.StatusBadRequest)
		return
	}
	if params.MID == 0 {
		http.Error(w, "消息ID无效", http.StatusBadRequest)
		return
	}

	cate, src, err := infos.handleMs(params.CID, params.MID, params.Cate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	defer func() {
		switch cate {
		case "user":
			infos.TCPStatus.User.WakeTime = time.Now()
		case "bot":
			infos.TCPStatus.Bot.WakeTime = time.Now()
		}
	}()

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
	_, err = infos.Client.DownloadMedia(src.Media(), &telegram.DownloadOptions{
		ThumbOnly: true,
		ThumbSize: actualThumb,
		Buffer:    buf,
		Ctx:       r.Context(),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Write(buf.Bytes())
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

	for _, channel := range params.Channels {
		item, err := infos.list(channel, params.Page, params.Limit, params.Filter)
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

	if params.CID == 0 {
		http.Error(w, "频道ID无效", http.StatusBadRequest)
		return
	}
	if params.MID == 0 {
		http.Error(w, "消息ID无效", http.StatusBadRequest)
		return
	}

	cate, src, err := infos.handleMs(params.CID, params.MID, params.Cate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	size := src.File.Size
	fileName := src.File.Name
	if size < infos.Conf.MaxSize*int64(infos.Conf.Workers) {
		clientIP := GetClientIP(r)
		log.Printf("正在处理来自 %s 的请求, 开始下载, cid=%d, mid=%d, name=%s", clientIP, params.CID, params.MID, fileName)
		buf := new(bytes.Buffer)
		_, err = infos.Client.DownloadMedia(src.Media(), &telegram.DownloadOptions{
			Buffer:    buf,
			Ctx:       r.Context(),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.Write(buf.Bytes())
	} else {
		// 创建新的 Stream 流管理对象
		stream := newStream(r.Context(), infos.Client, src.Media(), infos.Conf.Workers, params.MID, params.CID, src.File.Size, fileName)

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
		w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, fileName))

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
		stream.HeadSize, stream.TailSize = mediaCacheSizes(size)

		// 启动并发下载协程
		go stream.start(start, end)
		defer func() {
			stream.clean()
			switch cate {
			case "user":
				infos.TCPStatus.User.WakeTime = time.Now()
			case "bot":
				infos.TCPStatus.Bot.WakeTime = time.Now()
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
					if infos.Conf.DeBUG {
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
						if infos.Conf.DeBUG {
							log.Printf("流式传输文件已取消: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
						}
						return
					case <-timer.C:
						log.Printf("流式传输文件超时: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
						return
					case content, ok := <-task.Content:
						if !ok {
							if infos.Conf.DeBUG {
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
							if infos.Conf.DeBUG {
								log.Printf("流式传输文件已完成: cid=%d, mid=%d, name=%s", params.CID, params.MID, fileName)
							}
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

// handleSearch 处理搜索请求, 并发搜索多个频道
func handleSearch(w http.ResponseWriter, r *http.Request) {
	if infos.UserClient == nil {
		http.Error(w, "userBot 未登录, 无法使用搜索功能", http.StatusUnauthorized)
		return
	}
	params, err := handleParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	keywords := params.Keywords
	if keywords == "" {
		http.Error(w, "缺少关键词", http.StatusBadRequest)
		return
	}
	page := params.Page
	offset := params.Offset
	limit := params.Limit

	clientIP := GetClientIP(r)
	log.Printf("正在处理来自 %s 的请求, 开始搜索, page=%d, offset=%d, limit=%d, keywords=%s", clientIP, page, offset, limit, keywords)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	channels := make([]string, 0, len(infos.Conf.Channels))
	infos.Mutex.RLock() // 加锁保护读取过程
	if len(params.Channels) == 0 {
		channels = append(channels, infos.Conf.Channels...)
	} else {
		channels = append(channels, params.Channels...)
	}
	infos.Mutex.RUnlock() // 读取完立即解锁

	results := make(chan Items, len(channels))
	var workerPool sync.WaitGroup

	maxCount := int64(2 * infos.Conf.Workers)
	if maxCount == 0 {
		maxCount = 3
	}

	for _, channel := range channels {
		infos.Cond.L.Lock()
		for searchCount.Load() >= maxCount {
			infos.Cond.Wait()
		}
		infos.Cond.L.Unlock()

		searchCount.Add(1)
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

			result, err := infos.search(channel, keywords, page, limit, int32(offset), params.Filter)
			if err != nil {
				return
			}
			select {
			case <-ctx.Done():
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
		case <-ctx.Done():
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

// handleLink 处理链接提取请求, 将 Telegram 消息链接转换为直链下载地址并执行重定向
func handleLink(w http.ResponseWriter, r *http.Request) {
	res := HackLink{}
	params := r.URL.Query()

	// 1. 验证访问权限 (密码或哈希)
	if err := checkPass(params); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// 2. 获取目标 Telegram 链接
	src := params.Get("link")
	if src == "" || !strings.HasPrefix(src, "http") {
		http.Error(w, "无效的链接", http.StatusBadRequest)
		return
	}

	clientIP := GetClientIP(r)
	log.Printf("正在处理来自 %s 的请求, 开始提取直链, link=%s", clientIP, src)

	// 3. 正则匹配并解析链接
	re := regexp.MustCompile(`t\.me\/(c\/(\d+)|([a-zA-Z0-9_]+))\/(\d+)(?:.*comment=(\d+))?(?:.*o=([-+]?\d+))?`)
	res.Matches = re.FindAllStringSubmatch(src, -1)
	value := params.Get("uid")

	uid, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		log.Printf("转换UID错误: %+v", err)
	}
	res.UID = uid
	res.Pass = params.Get("key")
	res.Hash = params.Get("hash")

	links := hackLink(res)
	if len(links) == 0 {
		http.Error(w, "未找到可下载的媒体", http.StatusNotFound)
		return
	}

	result, err := json.Marshal(links)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
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
