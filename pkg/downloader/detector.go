package downloader

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
)

// Detector 时间戳检测器
type Detector struct {
	downloader *OSSDownloader
}

// NewDetector 创建时间戳检测器
func NewDetector() *Detector {
	return &Detector{
		downloader: NewOSSDownloader(),
	}
}

// DetectTimestamp 自动检测备份文件的时间戳
// 规则：使用当前小时的35分，自动尝试秒数 01-10
func (d *Detector) DetectTimestamp(ctx context.Context, baseURL, podName string) (string, error) {
	logger.Info("自动检测备份文件时间戳",
		zap.String("base_url", baseURL),
		zap.String("pod_name", podName),
	)

	// 获取当前小时
	hour := time.Now().Format("2006010215") // YYYYMMDDHH
	baseTimestamp := hour + "35" // 固定35分

	// 尝试秒数 01-10
	for second := 1; second <= 10; second++ {
		secondStr := fmt.Sprintf("%02d", second)
		timestamp := baseTimestamp + secondStr
		filename := fmt.Sprintf("emsau_%s_%s.tar.gz", podName, timestamp)
		url := fmt.Sprintf("%s/%s", baseURL, filename)

		exists, size, err := d.downloader.Exists(ctx, url)
		if err != nil {
			logger.Warn("检查文件失败",
				zap.String("timestamp", timestamp),
				zap.Error(err),
			)
			continue
		}

		if exists {
			logger.Info("找到备份文件",
				zap.String("timestamp", timestamp),
				zap.String("filename", filename),
				zap.Int64("size", size),
			)
			return timestamp, nil
		}
	}

	return "", fmt.Errorf("未找到匹配的备份文件（尝试了 %s3501-%s3510）", hour, hour)
}

// DetectTimestampCustom 自定义时间戳检测
// 支持自定义模式，如前缀、后缀等
func (d *Detector) DetectTimestampCustom(ctx context.Context, baseURL, podName, pattern string) (string, error) {
	logger.Info("使用自定义模式检测时间戳",
		zap.String("pattern", pattern),
	)

	// 解析模式中的占位符
	// 支持：{hour} - 当前小时
	hour := time.Now().Format("2006010215")

	// 替换占位符
	searchPattern := strings.ReplaceAll(pattern, "{hour}", hour)

	// 如果模式包含通配符，需要枚举
	if strings.Contains(searchPattern, "*") {
		// 简单实现：枚举秒数 01-10
		for second := 1; second <= 10; second++ {
			secondStr := fmt.Sprintf("%02d", second)
			timestamp := strings.ReplaceAll(searchPattern, "*", secondStr)
			filename := fmt.Sprintf("emsau_%s_%s.tar.gz", podName, timestamp)
			url := fmt.Sprintf("%s/%s", baseURL, filename)

			exists, _, err := d.downloader.Exists(ctx, url)
			if err != nil {
				continue
			}

			if exists {
				logger.Info("找到备份文件",
					zap.String("timestamp", timestamp),
					zap.String("filename", filename),
				)
				return timestamp, nil
			}
		}
	} else {
		// 直接检查
		filename := fmt.Sprintf("emsau_%s_%s.tar.gz", podName, searchPattern)
		url := fmt.Sprintf("%s/%s", baseURL, filename)

		exists, _, err := d.downloader.Exists(ctx, url)
		if err != nil {
			return "", fmt.Errorf("检查文件失败: %w", err)
		}

		if exists {
			return searchPattern, nil
		}
	}

	return "", fmt.Errorf("未找到匹配的备份文件")
}

// ParseTimestamp 从文件名解析时间戳
func ParseTimestamp(filename string) (string, error) {
	// 匹配模式：emsau_podname_YYYYMMDDHHMMSS.tar.gz
	re := regexp.MustCompile(`emsau_\S+_(\d{14})\.tar\.gz`)
	matches := re.FindStringSubmatch(filename)

	if len(matches) < 2 {
		return "", fmt.Errorf("无法从文件名解析时间戳: %s", filename)
	}

	return matches[1], nil
}

// ValidateTimestamp 验证时间戳格式
func ValidateTimestamp(timestamp string) error {
	// 时间戳应该是 14 位数字
	if len(timestamp) != 14 {
		return fmt.Errorf("时间戳长度错误，应为 14 位，实际为 %d 位", len(timestamp))
	}

	// 检查是否都是数字
	if _, err := strconv.ParseInt(timestamp, 10, 64); err != nil {
		return fmt.Errorf("时间戳格式错误，应该为数字: %w", err)
	}

	// 尝试解析为时间
	_, err := time.Parse("20060102150405", timestamp)
	if err != nil {
		return fmt.Errorf("时间戳不是有效的时间格式: %w", err)
	}

	return nil
}

// FormatTimestamp 格式化时间戳为可读字符串
func FormatTimestamp(timestamp string) (string, error) {
	t, err := time.Parse("20060102150405", timestamp)
	if err != nil {
		return "", err
	}

	return t.Format("2006-01-02 15:04:05"), nil
}

// BuildBackupURL 构建备份文件的完整 URL
func BuildBackupURL(baseURL, podName, timestamp string) string {
	filename := fmt.Sprintf("emsau_%s_%s.tar.gz", podName, timestamp)
	return fmt.Sprintf("%s/%s", baseURL, filename)
}

// BuildBackupFilename 构建备份文件名
func BuildBackupFilename(podName, timestamp string) string {
	return fmt.Sprintf("emsau_%s_%s.tar.gz", podName, timestamp)
}
