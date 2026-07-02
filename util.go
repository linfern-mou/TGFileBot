package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	imExt = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
		".webp": true, ".heic": true, ".heif": true,
	}
	videoExt = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".wmv": true, ".flv": true,
		".f4v": true, ".webm": true, ".m4v": true, ".mov": true, ".3gp": true,
		".ts": true, ".m3u8": true, ".rm": true, ".rmvb": true, ".iso": true,
	}
)

// IsVideoFile 判断文件后缀是否为视频文件
func IsVideoFile(ext string) bool {
	return videoExt[strings.ToLower(ext)]
}

// IsImFile 判断文件后缀是否为图片文件
func IsImFile(ext string) bool {
	return imExt[strings.ToLower(ext)]
}

// handleTime 将秒数格式化为人类可读的时间字符串
func handleTime(secs uint64) string {
	if secs > 86400 {
		return fmt.Sprintf("%dd %dh %dm %ds", secs/86400, (secs%86400)/3600, (secs%3600)/60, secs%60)
	} else if secs > 3600 {
		return fmt.Sprintf("%dh %dm %ds", secs/3600, (secs%3600)/60, secs%60)
	} else if secs > 60 {
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%ds", secs)
}

// formatFileSize 将字节数格式化为 B/K/M 单位的字符串
func formatFileSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}

	units := []string{"B", "K", "M"}
	var count int
	var result = float64(size)

	for result >= unit && count < len(units)-1 {
		result /= unit
		count++
	}

	// 如果是整数则不保留小数, 有小数则保留两位
	if result == float64(int64(result)) {
		return fmt.Sprintf("%.0f%s", result, units[count])
	}
	return fmt.Sprintf("%.2f%s", result, units[count])
}

// convertMaxSize 将用户输入的缓存大小字符串（如 "32M"）转换为字节数
func convertSize(str string) int64 {
	var unit int64 = 1
	src := strings.ToUpper(str)
	switch {
	case strings.HasSuffix(src, "B"), regexp.MustCompile(`\d$`).MatchString(src):
		src = strings.TrimSuffix(src, "B")
		unit = 1
	case strings.HasSuffix(src, "K"):
		src = strings.TrimSuffix(src, "K")
		unit = 1024
	case strings.HasSuffix(src, "M"):
		src = strings.TrimSuffix(src, "M")
		unit = 1024 * 1024
	case strings.HasSuffix(src, "G"):
		src = strings.TrimSuffix(src, "G")
		unit = 1024 * 1024 * 1024
	default:
		return int64(128 * 1024)
	}

	value, err := strconv.ParseInt(src, 10, 64)
	if err != nil {
		return int64(128 * 1024)
	}
	return value * unit
}

// extractContent 从字符串中提取正文与可选的行数参数
// 例如 "error 20" 返回 ("error", &20)；"error" 返回 ("error", nil)；"20" 返回 ("", &20)
func extractContent(src string) (string, *int) {
	src = strings.TrimSpace(src)

	// 1. 如果整个字符串就是一个数字
	if num, err := strconv.Atoi(src); err == nil {
		return "", &num
	}

	// 2. 寻找主体部分最后一个空格
	count := strings.LastIndexByte(src, ' ')
	if count == -1 {
		return src, nil
	}

	// 3. 判断最后一个空格后面那一截是不是数字
	content := src[count+1:]
	if num, err := strconv.Atoi(content); err == nil {
		return src[:count], &num
	}

	return src, nil
}

// readLastLines 读取日志文件中匹配 src 正则的最后 count 行
func readLastLines(filePath, src string, count int) (lines []string, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("关闭文件失败: %+v", err)
		}
	}()

	re := regexp.MustCompile(src)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if re.MatchString(scanner.Text()) {
			lines = append(lines, scanner.Text())
		}
		// 超过行数限制后, 舍弃旧行（滑动窗口效果）
		if len(lines) > count {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return lines, err
	}
	return lines, nil
}

// cleanFiles 清理指定类型的 session 或 cache 文件
func cleanFiles(realm CleanRealm) {
	switch strings.ToLower(realm.Realm) {
	case "cache":
		if files, err := os.ReadDir(infos.FilesPath); err == nil {
			src := fmt.Sprintf("%s_", strings.ToLower(realm.Cate))
			for _, file := range files {
				name := strings.TrimSpace(file.Name())
				if !file.IsDir() && strings.HasPrefix(name, src) && strings.HasSuffix(name, ".cache") {
					if realm.Filter {
						if realm.ID != "" && realm.ID != "0" {
							currentID := strings.TrimSuffix(strings.TrimPrefix(name, src), ".cache")
							if currentID != realm.ID {
								if err := os.Remove(filepath.Join(infos.FilesPath, name)); err != nil {
									log.Printf("删除缓存文件失败: %+v", err)
								}
							}
						}
					} else {
						if err := os.Remove(filepath.Join(infos.FilesPath, name)); err != nil {
							log.Printf("删除缓存文件失败: %+v", err)
						}
					}
				}
			}
		}
	case "session":
		name := fmt.Sprintf("%s.session", strings.ToLower(realm.Cate))
		if err := os.Remove(filepath.Join(infos.FilesPath, name)); err != nil {
			log.Printf("删除缓存文件失败: %+v", err)
		}
	}
}

// isNumber 判断 rune 是否为数字字符（供 submitCode 过滤验证码使用）
func isNumber(r rune) bool {
	return r >= '0' && r <= '9'
}

// isAllNumber 判断字符串是否全为数字
func isAllNumber(s string) bool {
	for _, r := range s {
		if !isNumber(r) && r != '-' && r != '+' {
			return false
		}
	}
	return true
}

// GetClientIP 从http.Request中提取客户端真实IP，支持代理场景和IPv6
func GetClientIP(r *http.Request) string {
	// 1. 优先处理X-Forwarded-For（代理场景）
	xForwardedFor := r.Header.Get("X-Forwarded-For")
	if xForwardedFor != "" {
		// 格式："clientIP, proxy1IP, proxy2IP"，取第一个非空IP
		parts := strings.Split(xForwardedFor, ",")
		for _, part := range parts {
			ip := strings.TrimSpace(part)
			if ip != "" {
				return ip
			}
		}
	}

	// 2. 其次处理X-Real-IP（代理常用）
	xRealIP := r.Header.Get("X-Real-IP")
	if xRealIP != "" {
		return xRealIP
	}

	// 3. 最后从RemoteAddr获取（直接连接场景）
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}

	// 4. 所有方式失败时返回默认值
	return "未知IP"
}

// evictOldestMediaCache 当 cache map 超过 maxCount 时删除最旧的一条
func evictOldestMediaCache(cache map[string]*MediaCache, maxCount int) {
	if len(cache) <= maxCount {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	for k, v := range cache {
		if oldestKey == "" || v.Time.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.Time
		}
	}
	if oldestKey != "" {
		delete(cache, oldestKey)
		log.Printf("媒体缓存已淘汰最旧条目: key=%s", oldestKey)
	}
}

// evictOldestMsCache 当 cache map 超过 maxCount 时删除最旧的一条 (针对 MsCache)
func evictOldestMsCache(cache map[string]*MsCache, maxCount int) {
	if len(cache) <= maxCount {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	for k, v := range cache {
		if oldestKey == "" || v.Time.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.Time
		}
	}
	if oldestKey != "" {
		delete(cache, oldestKey)
		log.Printf("消息缓存已淘汰最旧条目: key=%s", oldestKey)
	}
}

// evictOldestChannelCache 当 cache map 超过 maxCount 时删除最旧的一条 (针对 ChannelInfo)
func evictOldestChannelCache(cache map[string]*ChannelInfo, maxCount int) {
	if len(cache) <= maxCount {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	for k, v := range cache {
		if oldestKey == "" || v.Time.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.Time
		}
	}
	if oldestKey != "" {
		delete(cache, oldestKey)
		log.Printf("消息缓存已淘汰最旧条目: key=%s", oldestKey)
	}
}

// mediaCacheKey 生成缓存 key
func mediaCacheKey(cid int64, mid int32) string {
	return fmt.Sprintf("%d:%d", cid, mid)
}

// mediaCacheSizes 根据文件大小计算头部缓存和尾部缓存的大小
func mediaCacheSizes(size int64) (headSize int64, tailSize int64) {
	switch {
	case size < 2*1024*1024:
		return
	case size < 16*1024*1024:
		count := size / 1024
		headSize = count / 2 * 1024
		tailSize = count / 2 * 1024
	default:
		headSize = 8 * 1024 * 1024
		tailSize = 8 * 1024 * 1024
	}
	return
}

// handleOffset 处理消息偏移量
func handleOffset(act, kname string, value int32) (offset int32) {
	offSets.Mutex.Lock()
	defer offSets.Mutex.Unlock()
	switch strings.ToLower(act) {
	case "get":
		if values, ok := offSets.OffSets[kname]; ok {
			if time.Since(values.Time) < time.Hour {
				offset = values.Offset
			} else {
				delete(offSets.OffSets, kname)
			}
		}
	case "set":
		if len(offSets.OffSets) >= 32 {
			var oldestKname string
			var oldestTime time.Time
			for k, v := range offSets.OffSets {
				if oldestTime.IsZero() || v.Time.Before(oldestTime) {
					oldestTime = v.Time
					oldestKname = k
				}
			}
			if !oldestTime.IsZero() {
				delete(offSets.OffSets, oldestKname)
			}
		}
		offSets.OffSets[kname] = OffSet{
			Offset: value,
			Time:   time.Now(),
		}
	}
	return
}

