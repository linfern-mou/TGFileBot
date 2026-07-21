package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

type Params struct {
	CID      int64
	UID      int64
	Offset   int32
	MID      int32
	Page     int
	Limit    int
	Reverse  bool
	Pass     string
	Cate     string
	Hash     string
	Link     string
	Keywords string
	Filters  []int64
	Channels []string
}

type ChannelInfo struct {
	CID      int64
	Hash     int64
	UserName string
	Peer     telegram.InputPeer
	Time     time.Time
}

// LatestGroup 记录某个频道最近一次跨页相册（媒体组）边界的去重信息，
// 避免翻页时把上一页已经展示过的相册子项重复展示一遍。
// 按频道分别保存（而非共用一份全局状态），因为不同频道的消息 ID 完全可能重合。
type LatestGroup struct {
	Count int            // 上一次为该边界相册实际展示的子项数量, 新一页只需检查前 Count 条消息即可
	MIDs  map[int32]bool // 上一次已经展示过的相册子项消息 ID 集合（精确匹配, 不使用子串匹配）
	Time  time.Time      // 最近一次更新时间, 供淘汰策略使用
}

// HackLink 结构体用于在处理提取链接时传递中间数据
type HackLink struct {
	M       *telegram.NewMessage // 原始消息对象
	Ctx     context.Context      // 上下文
	Offset  int32                // 偏移量
	UID     int64                // 发起请求的用户 ID
	Pass    string               // 可选密码
	Hash    string               // 验证哈希
	Matches [][]string           // 正则匹配到的链接信息
}

type HandleMs struct {
	CID      int64
	OffsetID int32
	Limit    int
	MIDs     []int32
	Cate     string
	Words    string
	CNames   []string
	Ctx      context.Context
	Filter   telegram.MessagesFilter
}

// CleanRealm 结构体用于定义清理缓存和会话的范围
type CleanRealm struct {
	Filter bool   // 是否启用过滤, 只删除特定 ID 以外的文件
	ID     string // 过滤 ID（如账号 ID）
	Cate   string // 类型：bot 或 user
	Realm  string // 范围：cache 或 session
}

type OffSet struct {
	Offset int32     // 偏移量
	Time   time.Time // 时间
}

type OffSets struct {
	Mutex   *sync.Mutex       // 互斥锁, 保护并发安全
	OffSets map[string]OffSet // 偏移量映射
}

type MediaContent struct {
	Start   int64
	End     int64
	Content []byte
	Time    time.Time
}

type MediaCache struct {
	Contents []MediaContent
	Time     time.Time
}

type MsCache struct {
	Mes     []telegram.NewMessage
	Cate    string
	Time    time.Time
	Version atomic.Int64
}

// load 在持有 infos.Mutex 读锁的情况下安全地取出当前消息列表。
// msCache 一旦被写入 infos.MsCache 就可能被并发的多个请求共享；refreshMs
// 以及流式下载完成后的缓存回写都会在持锁状态下重新赋值 Mes 字段，
// 因此所有读取都必须走这里，而不是直接 msCache.Mes（否则可能读到撕裂的 slice header）。
func (msCache *MsCache) load() []telegram.NewMessage {
	infos.Mutex.RLock()
	defer infos.Mutex.RUnlock()
	return msCache.Mes
}

type Item struct {
	Ext      string `json:"ext"`
	Src      string `json:"src"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Date     int32  `json:"date"`
	MID      int32  `json:"mid"`
	CID      int64  `json:"cid"`
	GID      int64  `json:"gid"`
	Size     int64  `json:"size"`
}

type Items struct {
	HasMore bool   `json:"more"`
	ID      string `json:"id"`
	Word    string `json:"word"`
	Channel string `json:"channel"`
	Item    []Item `json:"item"`
}

type ID struct {
	IsAdmin bool
	IsWhite bool
}

// TCPStatu 记录一路 TCP 连接的探活状态, 字段用原子类型是因为会被多个并发 HTTP 请求 goroutine 同时读写
type TCPStatu struct {
	Latenc   atomic.Int64 // 延迟, 单位毫秒
	WakeTime atomic.Int64 // 最近一次探活/唤醒时间, UnixNano; 零值表示"从未探活", 会强制触发下一次 wakeTCP
	Fails    atomic.Int64 // 连续下载失败计数; > 0 时 handleMs 跳过 30 分钟阈值, 强制触发 wakeTCP
}

// wake 记录一次成功的探活/唤醒, 更新延迟和唤醒时间, 同时清零连续失败计数
func (s *TCPStatu) wake(latenc int64) {
	s.Latenc.Store(latenc)
	s.WakeTime.Store(time.Now().UnixNano())
	s.Fails.Store(0)
}

// reset 清空唤醒时间, 强制下一次请求重新触发 wakeTCP
func (s *TCPStatu) reset() {
	s.WakeTime.Store(0)
}

// touch 仅刷新唤醒时间, 不改变已记录的延迟; 同时清零连续失败计数
func (s *TCPStatu) touch() {
	s.WakeTime.Store(time.Now().UnixNano())
	s.Fails.Store(0)
}

// fail 递增连续失败计数并清空唤醒时间, 使下一个请求强制走 wakeTCP 重连探活
func (s *TCPStatu) fail() {
	s.Fails.Add(1)
	s.WakeTime.Store(0)
}

// since 返回距离上次探活/唤醒过去的时长
func (s *TCPStatu) since() time.Duration {
	return time.Since(time.Unix(0, s.WakeTime.Load()))
}

// Infos 结构体保存了程序运行时的全局状态和资源句柄
type Infos struct {
	BotClient    atomic.Pointer[telegram.Client] // 独立的 Bot 客户端（用于与用户交互）, 原子指针支持无锁并发读写
	UserClient   atomic.Pointer[telegram.Client] // 全局 UserBot 客户端实例（用于读取私有内容和流式传输）, 原子指针支持无锁并发读写
	Mutex        *sync.RWMutex                   // 全局互斥锁, 保护并发安全
	Cond         *sync.Cond                      // 条件变量, 用于搜索并发限流等待（独立锁, 不与 Mutex 共用）
	Conf         atomic.Pointer[Conf]            // 全局配置快照, 原子指针支持无锁并发读；更新走 updateConf（写时拷贝）
	ConfMu       *sync.Mutex                     // 序列化配置更新, 避免并发管理员命令互相覆盖对方的修改
	File         *os.File                        // 日志文件句柄
	Rex          *regexp.Regexp                  // 用于解析 Telegram FloodWait 错误的正则
	RexRules     []*regexp.Regexp                // 预编译的群管正则规则缓存
	FilesPath    string                          // 配置文件存放目录
	FilePath     string                          // 日志文件路径
	MaxMs        int                             // 最大消息数
	MaxChannel   int                             // 最大频道数
	MaxMedia     int                             // 最大媒体数
	BotID        int64                           // Bot 自身的 ID
	Status       atomic.Int32                    // UserBot 登录状态: 0 未登录, 1 等待验证码, 2 等待二步验证, 3 已登录
	WaitUntil    atomic.Int64                    // 等待结束时间
	Code         chan string                     // 用于接收异步提交的验证码
	Pass         chan string                     // 用于接收异步提交的二步验证密码
	IDs          map[int64]ID                    // 用户 ID -> 权限标记
	HashIndex    map[string]int64                // hash -> uid 反查表, 由 rebuildHashIndexLocked 统一维护, 供 checkHash O(1) 查找
	LatestGroups map[string]*LatestGroup         // 频道 -> 最近一次相册边界去重信息, 见 LatestGroup 注释
	ChannelID    map[string]*ChannelInfo         // 缓存频道名到频道 ID 的映射, 减少重复查询
	HeadCache    map[string]*MediaCache          // 缓存文件头部数据
	TailCache    map[string]*MediaCache          // 缓存文件尾部数据
	MsCache      map[string]*MsCache             // 缓存消息，避免频繁调用 GetMessages
	TCPStatus    struct {
		Bot  TCPStatu
		User TCPStatu
	} // 记录TCP连接状态
}

// tcpStat 按类别返回对应的 TCP 探活状态, 用于收敛调用方重复的 switch cate 判断
func (infos *Infos) tcpStat(cate string) *TCPStatu {
	if cate == "user" {
		return &infos.TCPStatus.User
	}
	return &infos.TCPStatus.Bot
}

var infos *Infos
var offSets *OffSets
var startTime time.Time
var searchCount atomic.Int64
var version = "v1.1.3"

// main 是程序的入口函数
func main() {
	startTime = time.Now()
	// 解析命令行参数
	files := flag.String("files", "files", "配置文件所属目录路径（包含 config.json, session 等）")
	file := flag.String("log", "", "日志文件的存放路径")
	var ver bool
	flag.BoolVar(&ver, "version", false, "显示程序版本号并退出")
	flag.BoolVar(&ver, "v", false, "显示程序版本号并退出")
	flag.Parse()

	// 版本检查逻辑
	if ver {
		fmt.Println(version)
		return
	}

	// 1. 初始化全局 Infos 对象并加载配置
	value, err := newInfos(*file, *files)
	if err != nil {
		log.Printf("初始化失败: %+v", err)
		return
	}
	infos = value
	offSets = newOffSets()

	// 2. 退出时的资源清理（延迟执行）
	defer func() {
		if infos.File != nil {
			if err := infos.File.Close(); err != nil {
				log.Printf("关闭日志文件错误: %+v", err)
			}
		}
		if client := infos.BotClient.Load(); client != nil {
			if err := client.Disconnect(); err != nil {
				log.Printf("Bot 退出失败: %+v", err)
			}
		}
		if client := infos.UserClient.Load(); client != nil {
			if err := client.Disconnect(); err != nil {
				log.Printf("UserBot 退出失败: %+v", err)
			}
		}
	}()

	// 3. 校验关键配置参数
	conf := infos.Conf.Load()
	if conf.AppID == 0 || conf.AppHash == "" || conf.BotToken == "" {
		log.Panicf("配置文件缺少必要的参数: AppID、AppHash、BotToken")
		return
	}

	if conf.Port == 0 {
		// 仅填充内存中的默认值, 不落盘持久化（用户未显式设置时不应改写 config.json）
		filled := *conf
		filled.Port = 8080 // 默认端口 8080
		conf = &filled
		infos.Conf.Store(conf)
	}

	// 4. 启动 Bot 客户端
	if err = infos.startBot(); err != nil {
		return
	}

	// 5. 初始化 UserBot 客户端（此时只是连接, 尚未完成登录流程）
	if err = infos.userBotClient(); err != nil {
		log.Printf("UserBot 启动失败: %+v", err)
		return
	}

	// 忽略 SIGPIPE 信号, 防止由于网络异常断开导致进程崩溃
	signal.Ignore(syscall.SIGPIPE)

	// 设置系统中断信号监听, 用于优雅退出
	statusChan := make(chan os.Signal, 1)
	signal.Notify(statusChan, os.Interrupt, syscall.SIGTERM)

	// 创建 HTTP 服务器
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", conf.Port),
		Handler:           http.HandlerFunc(handleMain),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       600 * time.Second,
		MaxHeaderBytes:    1 << 20, // 最大头部字节数 (1MB)
	}

	// 6. 在独立协程中启动 HTTP 服务
	go func() {
		log.Printf("HTTP 服务运行在 %d 端口", conf.Port)

		if err := server.ListenAndServe(); err != nil {
			log.Printf("HTTP 服务启动失败: %+v", err)
			statusChan <- os.Interrupt
		}
	}()

	// 7. 发送程序启动通知
	sendMS(nil, "程序已启动", nil, 60)

	// 8. 检查 UserBot 登录状态, 尝试自动登录（若已存在 session）
	if err := infos.checkStatus(); err != nil {
		log.Printf("UserBot 登录失败: %+v", err)
		infos.resetStatus()
	}

	// 阻塞等待直到接收到退出信号
	status := <-statusChan
	log.Printf("收到信号: %v, 正在退出...", status)

	// 设置关闭的超时时间，例如 60 秒
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("HTTP 服务关闭异常: %+v", err)
	} else {
		if infos.Conf.Load().DeBUG {
			log.Printf("HTTP 服务已优雅关闭")
		}
	}
	sendMS(nil, "程序已退出", nil, 60)
}

// newInfos 初始化全局 Infos 对象, 加载日志和配置
func newInfos(filePath, filesPath string) (*Infos, error) {
	if filePath != "" {
		filePath = filepath.Clean(filePath)
	}
	filesPath = filepath.Clean(filesPath)

	maxChannel := 16
	maxMedia := 4
	mutex := new(sync.RWMutex)
	infos := &Infos{
		MaxMs:      maxChannel * 16,
		MaxChannel: maxChannel,
		MaxMedia:   maxMedia,
		FilePath:   filePath,
		FilesPath:  filesPath,
		Mutex:      mutex,
		ConfMu:     new(sync.Mutex),
		// Cond 使用独立的 Mutex, 避免搜索限流的 Wait/Broadcast 与 infos.Mutex 上其他无关操作互相阻塞
		Cond:         sync.NewCond(new(sync.Mutex)),
		Code:         make(chan string, 1),
		Pass:         make(chan string, 1),
		HeadCache:    make(map[string]*MediaCache, maxMedia),
		TailCache:    make(map[string]*MediaCache, maxMedia),
		MsCache:      make(map[string]*MsCache, maxChannel*16),
		ChannelID:    make(map[string]*ChannelInfo, maxChannel),
		LatestGroups: make(map[string]*LatestGroup, maxChannel),
		IDs:          make(map[int64]ID),
		HashIndex:    make(map[string]int64),
		Rex:          regexp.MustCompile(`(?i)(?:FLOOD(?:_PREMIUM)?_WAIT_(\d+)|WAIT(?:\s+OF)?\s*(\d+))`),
	}

	// 创建日志文件
	if filePath != "" {
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Printf("无法打开日志文件: %+v", err)
		}
		infos.File = file
		// 设置日志输出
		multiWriter := io.MultiWriter(os.Stdout, file)
		log.SetOutput(multiWriter)
	}

	// 加载配置文件
	conf, err := loadConf(filesPath)
	if err != nil {
		log.Fatalf("载入配置文件失败: %+v", err)
	}
	if conf.Workers == 0 {
		conf.Workers = 1
	}
	if conf.MaxSize == 0 {
		conf.MaxSize = 32 * 1024 * 1024
	}
	infos.Conf.Store(conf)
	infos.IDs = make(map[int64]ID, len(conf.AdminIDs)+len(conf.WhiteIDs)+1)
	infos.buildIDs()
	infos.buildRexRules()

	// 获取 BotID
	if conf.BotToken != "" {
		parts := strings.Split(conf.BotToken, ":")
		if len(parts) < 1 {
			return nil, fmt.Errorf("BotToken 格式错误: %s", conf.BotToken)
		}
		result := strings.TrimSpace(parts[0])
		infos.BotID, err = strconv.ParseInt(result, 10, 64)
		if err != nil {
			log.Printf("解析 BotID 失败: %+v", err)
		}
	}

	return infos, nil
}

// newOffSets 初始化全局翻页偏移量缓存
func newOffSets() *OffSets {
	return &OffSets{
		Mutex:   new(sync.Mutex),
		OffSets: make(map[string]OffSet),
	}
}

// buildRegex 预编译正则规则并缓存到 infos.RexRules
func (infos *Infos) buildRexRules() {
	conf := infos.Conf.Load()
	rexRules := make([]*regexp.Regexp, 0, len(conf.Rules))
	for _, rule := range conf.Rules {
		if rule == "" {
			continue
		}
		r, err := regexp.Compile(rule)
		if err != nil {
			log.Printf("正则规则编译失败 [%s]: %+v", rule, err)
			continue
		}
		rexRules = append(rexRules, r)
	}

	infos.Mutex.Lock()
	infos.RexRules = rexRules
	infos.Mutex.Unlock()

	if conf.DeBUG {
		log.Printf("成功预编译 %d 条正则规则", len(rexRules))
	}
}
