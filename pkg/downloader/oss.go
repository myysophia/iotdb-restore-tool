package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
)

// Downloader 下载器接口
type Downloader interface {
	Download(ctx context.Context, url, destPath string) error
	Exists(ctx context.Context, url string) (bool, int64, error)
	DownloadWithProgress(ctx context.Context, url, destPath string) error
}

// OSSDownloader OSS 下载器
type OSSDownloader struct {
	httpClient *http.Client
	maxRetries int
	retryDelay time.Duration
}

// NewOSSDownloader 创建 OSS 下载器
func NewOSSDownloader() *OSSDownloader {
	return &OSSDownloader{
		httpClient: &http.Client{
			Timeout: 30 * time.Minute,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
				// 启用 HTTP/2（如果服务器支持）
				// 注意：oss-accelerate.aliyuncs.com 可能需要 HTTP/1.1
				ForceAttemptHTTP2: false,
			},
		},
		maxRetries: 3,
		retryDelay: 5 * time.Second,
	}
}

// Download 下载文件到指定路径
func (d *OSSDownloader) Download(ctx context.Context, url, destPath string) error {
	return d.DownloadWithProgress(ctx, url, destPath)
}

// DownloadWithProgress 下载文件并显示进度
func (d *OSSDownloader) DownloadWithProgress(ctx context.Context, url, destPath string) error {
	// 确保目标目录存在
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	// 检查文件是否已存在
	if info, err := os.Stat(destPath); err == nil {
		logger.Info("文件已存在，跳过下载",
			zap.String("file", destPath),
			zap.Int64("size", info.Size()),
		)
		return nil
	}

	var lastErr error
	for attempt := 0; attempt < d.maxRetries; attempt++ {
		if attempt > 0 {
			logger.Warn("下载重试",
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", d.maxRetries),
				zap.Error(lastErr),
			)
			time.Sleep(d.retryDelay)
		}

		// 创建请求
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			lastErr = fmt.Errorf("创建请求失败: %w", err)
			continue
		}

		// 发送请求
		resp, err := d.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求失败: %w", err)
			continue
		}

		// 检查状态码
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("服务器返回错误状态码: %d", resp.StatusCode)
			continue
		}

		// 获取文件大小
		fileSize := resp.ContentLength
		logger.Info("开始下载",
			zap.String("url", url),
			zap.String("dest", destPath),
			zap.Int64("size", fileSize),
		)

		// 创建目标文件
		destFile, err := os.Create(destPath)
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("创建目标文件失败: %w", err)
		}

		// 使用 TeeWriter 来显示进度
		progressWriter := &progressWriter{
			total:     fileSize,
			writer:    destFile,
			url:       url,
			startTime: time.Now(),
		}

		// 复制数据
		_, err = io.Copy(progressWriter, resp.Body)
		resp.Body.Close()
		destFile.Close()

		if err != nil {
			os.Remove(destPath) // 删除不完整的文件
			lastErr = fmt.Errorf("下载文件失败: %w", err)
			continue
		}

		// 下载成功
		progressWriter.Finish()
		logger.Info("下载完成",
			zap.String("file", destPath),
			zap.Int64("size", fileSize),
			zap.Duration("duration", time.Since(progressWriter.startTime)),
		)
		return nil
	}

	return fmt.Errorf("下载失败，重试 %d 次后放弃: %w", d.maxRetries, lastErr)
}

// Exists 检查远程文件是否存在，并返回文件大小
func (d *OSSDownloader) Exists(ctx context.Context, url string) (bool, int64, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false, 0, fmt.Errorf("创建请求失败: %w", err)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return false, 0, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// 获取文件大小
		size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		return true, size, nil
	}

	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}

	return false, 0, fmt.Errorf("服务器返回错误状态码: %d", resp.StatusCode)
}

// progressWriter 进度写入器
type progressWriter struct {
	total     int64
	written   int64
	writer    io.Writer
	url       string
	startTime time.Time
	lastLog   time.Time
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	if err != nil {
		return n, err
	}

	pw.written += int64(n)

	// 每 5 秒或每 10% 打印一次进度
	now := time.Now()
	if now.Sub(pw.lastLog) > 5*time.Second || pw.total > 0 && pw.written% (pw.total/10) < int64(len(p)) {
		percent := float64(pw.written) / float64(pw.total) * 100
		speed := float64(pw.written) / time.Since(pw.startTime).Seconds() / 1024 / 1024 // MB/s
		logger.Info("下载进度",
			zap.String("url", pw.url),
			zap.Float64("percent", percent),
			zap.String("size", formatBytes(pw.written)),
			zap.String("total", formatBytes(pw.total)),
			zap.Float64("speed", speed),
		)
		pw.lastLog = now
	}

	return n, nil
}

func (pw *progressWriter) Finish() {
	percent := float64(pw.written) / float64(pw.total) * 100
	logger.Info("下载完成",
		zap.String("url", pw.url),
		zap.Float64("percent", percent),
		zap.String("size", formatBytes(pw.written)),
		zap.Duration("duration", time.Since(pw.startTime)),
	)
}

// formatBytes 格式化字节数
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ExtractFilename 从 URL 中提取文件名
func ExtractFilename(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "downloaded_file"
}
