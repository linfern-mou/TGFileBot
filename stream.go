package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// Task 代表一个下载分片任务
type Task struct {
	ContentStart int64       // 任务请求的数据起点（绝对位置）
	ContentEnd   int64       // 任务请求的数据终点（绝对位置）
	Version      int64       // 任务对应的文件版本号, 用于处理引用过期
	Error        error       // 下载过程中产生的错误
	Done         bool        // 是否已成功
	Content      chan []byte // 下载到的二进制内容
}

// Stream 结构体用于管理大文件的并发下载和流式传输
type Stream struct {
	Ctx          context.Context        // 上下文, 用于取消下载
	Client       *telegram.Client       // Gogram 客户端实例
	Src          *telegram.MessageMedia // Telegram 消息媒体源
	Workers      int                    // 下载并发协程数
	MID          int32                  // Telegram 消息 ID
	CID          int64                  // Telegram 频道/会话 ID
	ChunkSize    int64                  // 每个下载分片的大小（通常 512KB 或 1MB）
	ContentSize  int64                  // 文件的总大小
	MaxCacheSize int64                  // 最大缓存大小
	HeadSize     int64                  // 头部缓存大小
	TailSize     int64                  // 尾部缓存大小
	TaskStart    *int64                 // 当前已分配任务的下载起点
	TaskEnd      *int64                 // 当前已分配任务的下载终点
	FileName     string                 // 文件名
	Error        error                  // 整个流运行过程中的错误
	Count        atomic.Int64           // 当前正在运行的协程数量
	Version      atomic.Int64           // 文件版本号, 因引用过期刷新后递增
	Init         atomic.Bool            // 是否已经初始化
	Mutex        *sync.Mutex            // 用于保护并发安全
	Tasks        chan *Task             // 任务管道, 用于向工作协程分发下载任务
	Pools        []*telegram.WorkerPool // 每个工作协程的独立连接池 (Pools[numTask-1])
}

// newTask 初始化并返回一个 Task 对象
func newTask() *Task {
	return &Task{
		Error:   nil,
		Content: make(chan []byte, 1),
	}
}

// newStream 初始化并返回一个 Stream 对象, 负责管理特定文件的流式下载
func newStream(ctx context.Context, client *telegram.Client, media telegram.MessageMedia, workers int, mid int32, cid, contentSize int64, name string) *Stream {
	// 根据并发数动态调整分片大小
	chunkSize := int64(1 * 1024 * 1024)
	// 默认 32MB 缓存
	maxCacheSize := infos.Conf.MaxSize
	if maxCacheSize == 0 {
		maxCacheSize = 32 * 1024 * 1024
	}
	headSize := maxCacheSize / 2
	if headSize > 8*1024*1024 {
		headSize = 8 * 1024 * 1024
	}
	tailSize := maxCacheSize / 2
	if tailSize > 8*1024*1024 {
		tailSize = 8 * 1024 * 1024
	}
	// 计算任务管道的容量
	maxChans := int(maxCacheSize / chunkSize)
	if maxChans == 0 {
		maxChans = 1
	}
	return &Stream{
		Ctx:          ctx,
		Client:       client,
		Src:          &media,
		Workers:      workers,
		FileName:     name,
		MID:          mid,
		CID:          cid,
		ContentSize:  contentSize,
		ChunkSize:    chunkSize, // 这里设置了固定值, 可以根据需要调整
		MaxCacheSize: maxCacheSize,
		HeadSize:     headSize,
		TailSize:     tailSize,
		Tasks:        make(chan *Task, maxChans),
		Mutex:        new(sync.Mutex),
		TaskStart:    new(int64),
		TaskEnd:      new(int64),
		Pools:        make([]*telegram.WorkerPool, workers), // 每个工作协程一个独立 slot
	}
}

// start 启动工作协程开始下载任务
func (stream *Stream) start(contentStart, contentEnd int64) {
	// 计算任务总数
	maxTasks := int((contentEnd - contentStart + 1 + stream.ChunkSize - 1) / stream.ChunkSize)
	// 限制并发协程数不超过配置值
	if maxTasks > stream.Workers {
		maxTasks = stream.Workers
	}

	for numTask := 1; numTask <= maxTasks; numTask++ {
		stream.Count.Add(1)
		go func(numTask int) {
			defer stream.Count.Add(-1)
			stream.download(numTask, contentStart, contentEnd)
		}(numTask)
	}
}

// download 是工作协程的核心逻辑, 负责循环领取并下载文件分片
func (stream *Stream) download(numTask int, contentStart, contentEnd int64) {
	cacheKey := mediaCacheKey(stream.CID, stream.MID)
	for {
		maxWait := 3
		chunkSize := stream.ChunkSize
		if stream.Init.CompareAndSwap(false, true) {
			maxWait = 4
			chunkSize = 128 * 1024
		}

		stream.Mutex.Lock()
		task := newTask()
		// 计算当前任务的下载范围
		if *stream.TaskStart == 0 {
			task.ContentStart = contentStart
		} else {
			task.ContentStart = *stream.TaskStart
		}
		// 处理偏移量, 确保分片按照 ChunkSize 对齐, 提高 Telegram 服务器读取效率
		offset := task.ContentStart - (task.ContentStart/chunkSize)*chunkSize
		task.ContentStart = task.ContentStart - offset
		task.ContentEnd = task.ContentStart + chunkSize - 1

		// 如果下载起点超过了请求范围, 则结束下载
		if task.ContentStart > contentEnd {
			stream.Mutex.Unlock()
			return
		}

		// 将任务推入管道供下游消费（HTTP 响应层）
		// 同时监听 ctx.Done，防止 clean() 置 nil Tasks 后协程永久阻塞
		select {
		case <-stream.Ctx.Done():
			stream.Mutex.Unlock()
			return
		case stream.Tasks <- task:
			// 成功发送任务
		}

		// 更新流的状态, 为下一个任务做准备
		*stream.TaskStart = task.ContentEnd + 1
		*stream.TaskEnd = *stream.TaskStart + chunkSize - 1
		stream.Mutex.Unlock()

		// 尝试下载该分片
		maxCount := 3
		if task.ContentStart < int64(1048576) || (contentEnd-task.ContentEnd)/contentEnd*1000 < 2 {
			maxCount = 6
		}

		for num := 1; num <= maxCount; num++ {
			// 从缓存读取
			found := stream.handleCache(task, cacheKey, offset, contentEnd)
			if found {
				task.Done = true
				break
			}

			// 下载
			if waitUntil := infos.WaitUntil.Load(); waitUntil > 0 {
				if remaining := time.Until(time.Unix(waitUntil, 0)); remaining > 0 {
					if infos.Conf.DeBUG {
						log.Printf("协程%d: 检测到FloodWait, 等待 %.2f 秒", numTask, remaining.Seconds())
					}
					time.Sleep(remaining)
				}
			}
			version := stream.Version.Load()
			stream.Mutex.Lock()
			src := *stream.Src
			stream.Mutex.Unlock()

			// 调用 Gogram 接口从 Telegram 下载特定范围的文件块
			// 首次尝试给更长超时，容忍冷启动 TCP 连接重建 + TLS + MTProto 认证
			timeout := 8 * time.Second

			// 获取该协程专属的连接池（nil 时 DownloadChunk 自动回退为临时 pool）
			pool := stream.handlePool(numTask, src)
			content, fileName, err := stream.Client.DownloadChunk(src, int(task.ContentStart), int(task.ContentEnd)+1, int(chunkSize), false, stream.Ctx, timeout, pool)
			if err != nil {
				errStr := strings.ToLower(err.Error())
				switch {
				// 如果 context 已经关闭（手动取消或整体超时），则彻底停止任务
				case stream.Ctx.Err() != nil, errors.Is(err, context.Canceled):
					task.Error = errors.New("已取消下载任务")
					close(task.Content)
					return
				case telegram.MatchError(err, "FILE_REFERENCE_EXPIRED"):
					// 如果报错文件引用过期, 则调用 refresh 重新获取消息并更新引用
					if infos.Conf.DeBUG {
						log.Printf("文件引用已过期: cid=%d, mid=%d, version=%d, name=%s, numTask=%d", stream.CID, stream.MID, version, fileName, numTask)
					}
					if err := stream.refresh(numTask, version); err != nil {
						task.Error = err
						close(task.Content)
						return
					}
					continue
				case strings.Contains(errStr, "deadline exceeded") ||
					strings.Contains(errStr, "initialize worker: timeout") ||
					strings.Contains(errStr, "get worker: timeout"):
					// 渐进重试间隔：500ms, 1s, 1.5s, 2s...
					backoffMs := 500 * num
					if backoffMs > 3000 {
						backoffMs = 3000
					}
					backoff := time.Duration(backoffMs) * time.Millisecond
					log.Printf("协程%d: TCP连接超时 %d/%d, 错误: %v, 等待 %.2f 秒后重试", numTask, num, maxCount, err, backoff.Seconds())

					time.Sleep(backoff)
					if maxWait > 0 {
						maxWait--
						num-- // 抵消 for 循环的 num++, 不计入重试次数
					}
					continue
				default:
					if matches := infos.Rex.FindStringSubmatch(errStr); len(matches) > 0 {
						wait := 3
						if len(matches) > 1 {
							for _, match := range matches[1:] {
								if match != "" {
									if value, e := strconv.Atoi(match); e == nil {
										wait = value
										break
									}
								}
							}
						}
						if infos.Conf.DeBUG {
							log.Printf("协程%d: 访问太过频繁, 等待 %d 秒后重试", numTask, wait+1)
						}
						waitUntil := time.Now().Add(time.Duration(wait+1) * time.Second)
						if currentWait := infos.WaitUntil.Load(); waitUntil.Unix() > currentWait {
							infos.WaitUntil.Store(waitUntil.Unix())
						}
						time.Sleep(time.Duration(wait+1) * time.Second)
						if maxWait > 0 {
							num = maxCount - 1
							num-- // 抵消 for 循环的 num++, 不计入重试次数
						}
						continue
					} else {
						if num < maxCount {
							backoffMs := 500 * num
							if backoffMs > 3000 {
								backoffMs = 3000
							}
							backoff := time.Duration(backoffMs) * time.Millisecond
							log.Printf("协程%d: 网络错误重试 %d/%d, 等待 %.2f 秒后重试. 错误: %+v", numTask, num, maxCount, backoff.Seconds(), err)
							time.Sleep(backoff)
							continue
						} else {
							task.Error = err
							close(task.Content)
							return
						}
					}
				}
			}

			// 缓存
			if stream.HeadSize > 0 && stream.TailSize > 0 {
				infos.Mutex.Lock()
				switch {
				case task.ContentStart <= stream.HeadSize && task.ContentEnd <= stream.HeadSize:
					if values, ok := infos.HeadCache[cacheKey]; ok {
						values.Time = time.Now() // 指针类型，直接修改即生效
						found := false
						for _, c := range values.Contents {
							if c.Start == task.ContentStart && c.End == task.ContentEnd {
								found = true
								break
							}
						}
						if !found {
							// maxChunks = HeadSize / ChunkSize, 即头部最多能存几个分片
							maxChunks := int((stream.HeadSize+stream.ChunkSize-1)/stream.ChunkSize) + 1
							lenContents := len(values.Contents)
							if lenContents >= maxChunks {
								// 淡出策略：保留靠近文件头的分片, 删除 Start 最大的（距开头最远、最冗余）
								maxNum := 0
								for num := 1; num < lenContents; num++ {
									if values.Contents[num].Start > values.Contents[maxNum].Start {
										maxNum = num
									}
								}
								values.Contents[maxNum] = values.Contents[lenContents-1]
								values.Contents[lenContents-1] = MediaContent{}
								values.Contents = values.Contents[:lenContents-1]
							}
							values.Contents = append(values.Contents, MediaContent{
								Start:   task.ContentStart,
								End:     task.ContentEnd,
								Content: content,
							})
						}
					}
				case task.ContentStart >= stream.ContentSize-stream.TailSize:
					if values, ok := infos.TailCache[cacheKey]; ok {
						values.Time = time.Now() // 指针类型，直接修改即生效
						found := false
						for _, c := range values.Contents {
							if c.Start == task.ContentStart && c.End == task.ContentEnd {
								found = true
								break
							}
						}
						if !found {
							// maxChunks = TailSize / ChunkSize, 即尾部最多能存几个分片
							maxChunks := int((stream.TailSize+stream.ChunkSize-1)/stream.ChunkSize) + 1
							lenContents := len(values.Contents)
							if lenContents >= maxChunks {
								// 淡出策略：保留靠近文件尾的分片, 删除 Start 最小的（距结尾最远、最冗余）
								minNum := 0
								for num := 1; num < lenContents; num++ {
									if values.Contents[num].Start < values.Contents[minNum].Start {
										minNum = num
									}
								}
								values.Contents[minNum] = values.Contents[lenContents-1]
								values.Contents[lenContents-1] = MediaContent{}
								values.Contents = values.Contents[:lenContents-1]
							}
							values.Contents = append(values.Contents, MediaContent{
								Start:   task.ContentStart,
								End:     task.ContentEnd,
								Content: content,
							})
						}
					}
				}
				infos.Mutex.Unlock()
			}

			task.handleContent(content, offset, contentEnd)
			task.Done = true
			break
		}

		// 检查循环退出后是否成功
		if !task.Done && task.Error == nil {
			task.Error = fmt.Errorf("下载失败, 已达最大重试次数: %d", maxCount)
			close(task.Content)
			return
		}
	}
}

// clean 清理未完成或已读取的任务管道, 防止内存泄漏
func (stream *Stream) clean() {
	// 创建计时器, 避免死循环
	waiter := time.NewTimer(5 * time.Second)
	defer func() {
		waiter.Stop()
		// 等待所有 download 协程退出后再操作 Pools，消除与 handlePool 写入的数据竞争
		// Count 由各协程自己递减（defer stream.Count.Add(-1)），此处轮询直到归零
		deadline := time.Now().Add(30 * time.Second)
		for stream.Count.Load() > 0 {
			if time.Now().After(deadline) {
				log.Printf("等待协程超时, 强制退出")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		// 关闭所有工作协程的连接池（仅 drain channel，不终止底层 TCP）
		for num, pool := range stream.Pools {
			if pool != nil {
				pool.Close()
				stream.Pools[num] = nil
			}
		}
		if infos.Conf.DeBUG {
			log.Print("清理完成")
		}
	}()

	for {
		select {
		case task, ok := <-stream.Tasks:
			if !ok {
				task = nil
				return
			}
			if task != nil {
				timer := time.NewTimer(5 * time.Second)
				select {
				case _, ok := <-task.Content:
					if !ok {
						task.Content = nil
					}
					timer.Stop()
				case <-timer.C:
					if infos.Conf.DeBUG {
						log.Printf("清理任务时遇到阻塞过长, 强制丢弃: start=%d end=%d", task.ContentStart, task.ContentEnd)
					}
				}
			}
			// 重置计时器
			waiter.Reset(5 * time.Second)
		case <-waiter.C:
			stream.Tasks = nil
			return
		}
	}
}

// refresh 重新从 Telegram 获取消息以更新文件引用 (file_reference)
// 分布式锁/互斥锁确保并发情况下只刷新一次
func (stream *Stream) refresh(numTask int, version int64) (err error) {
	stream.Mutex.Lock()
	defer stream.Mutex.Unlock()

	// 如果版本号已经变了, 说明其他协程已经完成了刷新
	if version != stream.Version.Load() {
		if infos.Conf.DeBUG {
			log.Printf("文件引用已刷新, 直接使用新版本: cid=%d, mid=%d, numTask=%d, version=%d, newVersion=%d", stream.CID, stream.MID, numTask, version, stream.Version.Load())
		}
		return
	}

	// 重新获取消息
	ms, err := stream.Client.GetMessages(stream.CID, &telegram.SearchOption{IDs: []int32{stream.MID}})
	if err != nil {
		stream.Error = err
		return err
	}
	if len(ms) == 0 {
		err = fmt.Errorf("获取消息失败: cid=%d, mid=%d, err=未获取到消息", stream.CID, stream.MID)
		stream.Error = err
		return err
	}
	src := ms[0]

	// 确保消息依然包含媒体内容
	if !src.IsMedia() {
		err = fmt.Errorf("消息不包含媒体: cid=%d, mid=%d", stream.CID, stream.MID)
		stream.Error = err
		return err
	}
	// 更新流中的媒体引用
	*stream.Src = src.Media()
	stream.Version.Add(1)
	if infos.Conf.DeBUG {
		log.Printf("文件引用已刷新: cid=%d, mid=%d, numTask=%d, version=%d, newVersion=%d", stream.CID, stream.MID, numTask, version, stream.Version.Load())
	}
	return nil
}

// handlePool 返回该协程专属的长连接池，首次调用时懒初始化。
// 每个工作协程使用独立的 slot（Pools[numTask-1]），无需加锁。
// 若初始化失败则返回 nil，DownloadChunk 自动回退至临时 pool 模式。
func (stream *Stream) handlePool(numTask int, src telegram.MessageMedia) *telegram.WorkerPool {
	idx := numTask - 1
	// 快速路径：无锁检查，每个协程只读写自己的 slot
	if stream.Pools[idx] != nil {
		return stream.Pools[idx]
	}
	_, dc, _, _, err := telegram.GetFileLocation(src)
	if err != nil {
		log.Printf("无法解析文件 DC: %+v, 回退临时连接", err)
		return nil
	}
	if dc == 0 {
		dc = int32(stream.Client.GetDC())
	}
	pool, err := stream.Client.NewDownloadPool(dc)
	if err != nil {
		log.Printf("协程%d 初始化 DC%d 连接池失败: %+v, 回退临时连接", numTask, dc, err)
		return nil
	}
	stream.Pools[idx] = pool
	if infos.Conf.DeBUG {
		log.Printf("协程%d 初始化连接池成功: DC%d, cid=%d, mid=%d", numTask, dc, stream.CID, stream.MID)
	}
	return stream.Pools[idx]
}

func (task *Task) handleContent(content []byte, offset, contentEnd int64) {
	// 根据初始偏移量截取内容
	if int64(len(content)) > offset {
		content = content[offset:]
	} else {
		content = nil
	}
	actualStart := task.ContentStart + offset

	// 裁剪末尾：最后一个分片可能超出实际请求范围（contentEnd）,
	// 防止写入 HTTP 响应时超过声明的 Content-Length
	if task.ContentEnd > contentEnd {
		wantedLen := contentEnd - actualStart + 1
		if wantedLen > 0 && int64(len(content)) > wantedLen {
			content = content[:wantedLen]
		}
		task.ContentEnd = contentEnd
	}
	task.Content <- content
	close(task.Content) // 唤醒等待此分片的协程
}

func (stream *Stream) handleCache(task *Task, cacheKey string, offset, contentEnd int64) (found bool) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()
	// 从缓存读取
	switch {
	case task.ContentStart <= stream.HeadSize && task.ContentEnd <= stream.HeadSize:
		if values, ok := infos.HeadCache[cacheKey]; ok {
			for _, value := range values.Contents {
				if value.Start == task.ContentStart && value.End == task.ContentEnd {
					if infos.Conf.DeBUG {
						log.Printf("命中头部缓存: cid=%d, mid=%d, name=%s, start=%d, end=%d", stream.CID, stream.MID, stream.FileName, task.ContentStart, task.ContentEnd)
					}
					task.handleContent(value.Content, offset, contentEnd)
					return true
				}
			}
		} else {
			evictOldestMediaCache(infos.HeadCache, infos.MaxMedia)
			contents := make([]MediaContent, 0, int(stream.HeadSize/stream.ChunkSize))
			infos.HeadCache[cacheKey] = &MediaCache{Contents: contents, Time: time.Now()}
			if infos.Conf.DeBUG {
				log.Printf("头部缓存已初始化: cid=%d, mid=%d", stream.CID, stream.MID)
			}
			return false
		}
	case task.ContentStart >= stream.ContentSize-stream.TailSize:
		if values, ok := infos.TailCache[cacheKey]; ok {
			for _, value := range values.Contents {
				if value.Start == task.ContentStart && value.End == task.ContentEnd {
					if infos.Conf.DeBUG {
						log.Printf("命中尾部缓存: cid=%d, mid=%d, name=%s, start=%d, end=%d", stream.CID, stream.MID, stream.FileName, task.ContentStart, task.ContentEnd)
					}
					task.handleContent(value.Content, offset, contentEnd)
					return true
				}
			}
		} else {
			evictOldestMediaCache(infos.TailCache, infos.MaxMedia)
			contents := make([]MediaContent, 0, int(stream.TailSize/stream.ChunkSize))
			infos.TailCache[cacheKey] = &MediaCache{Contents: contents, Time: time.Now()}
			if infos.Conf.DeBUG {
				log.Printf("尾部缓存已初始化: cid=%d, mid=%d", stream.CID, stream.MID)
			}
			return false
		}
	}
	return false
}
