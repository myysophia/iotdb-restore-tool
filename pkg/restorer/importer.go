package restorer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/k8s"
	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
)

const importRetryBaseDelay = 10 * time.Second

// ImportResult 导入结果
type ImportResult struct {
	TotalFiles   int
	SuccessCount int
	FailedCount  int
	Duration     time.Duration
}

// RegionReadyFunc 在导入重试前确认 Region 已就绪。
type RegionReadyFunc func(context.Context) error

// Importer tsfile 导入器
type Importer struct {
	executor    *k8s.Executor
	config      *config.Config
	regionReady RegionReadyFunc
}

// NewImporter 创建导入器
func NewImporter(executor *k8s.Executor, cfg *config.Config, regionReady RegionReadyFunc) *Importer {
	return &Importer{
		executor:    executor,
		config:      cfg,
		regionReady: regionReady,
	}
}

// Import 导入文件列表
func (im *Importer) Import(ctx context.Context, files []string) (*ImportResult, error) {
	startTime := time.Now()
	totalFiles := len(files)

	logger.Info("开始导入 tsfile 文件",
		zap.Int("total_files", totalFiles),
		zap.Int("concurrency", im.config.Import.Concurrency),
		zap.Int("batch_size", im.config.Import.BatchSize),
		zap.Int("retry_count", im.config.Import.RetryCount),
	)

	sem := make(chan struct{}, im.config.Import.Concurrency)
	var successCount int64
	var failedCount int64
	var wg sync.WaitGroup

	batchSize := im.config.Import.BatchSize
	for i := 0; i < totalFiles; i += batchSize {
		end := i + batchSize
		if end > totalFiles {
			end = totalFiles
		}

		batch := files[i:end]
		batchNum := i/batchSize + 1
		totalBatches := (totalFiles + batchSize - 1) / batchSize

		logger.Info("处理批次",
			zap.Int("batch", batchNum),
			zap.Int("total_batches", totalBatches),
			zap.Int("batch_size", len(batch)),
			zap.Int("progress", i),
		)

		im.logMemoryUsage(ctx)

		for _, file := range batch {
			wg.Add(1)
			go func(f string) {
				defer wg.Done()

				sem <- struct{}{}
				defer func() { <-sem }()

				if err := im.importSingleFile(ctx, f); err != nil {
					atomic.AddInt64(&failedCount, 1)
					logger.Error("导入失败",
						zap.String("file", filepath.Base(f)),
						zap.Error(err),
					)
				} else {
					atomic.AddInt64(&successCount, 1)
					logger.Debug("导入成功", zap.String("file", filepath.Base(f)))
				}
			}(file)
		}

		wg.Wait()

		if i+batchSize < totalFiles && im.config.Import.BatchPause {
			logger.Info("等待系统释放内存...",
				zap.Int("pause_seconds", im.config.Import.BatchDelay),
			)
			time.Sleep(time.Duration(im.config.Import.BatchDelay) * time.Second)
		}
	}

	duration := time.Since(startTime)
	result := &ImportResult{
		TotalFiles:   totalFiles,
		SuccessCount: int(successCount),
		FailedCount:  int(failedCount),
		Duration:     duration,
	}

	logger.Info("所有文件导入完成",
		zap.Int("total_files", result.TotalFiles),
		zap.Int("success_count", result.SuccessCount),
		zap.Int("failed_count", result.FailedCount),
		zap.Duration("duration", duration),
	)

	return result, nil
}

// importSingleFile 导入单个文件，并对 Region 未就绪问题重试。
func (im *Importer) importSingleFile(ctx context.Context, filePath string) error {
	filename := filepath.Base(filePath)
	maxAttempts := im.config.Import.RetryCount
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		logger.Debug("开始导入文件",
			zap.String("file", filename),
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", maxAttempts),
		)

		if err := im.runLoadCommand(ctx, filePath); err != nil {
			lastErr = err
			if !isRetryableImportError(err) || attempt == maxAttempts {
				return err
			}

			logger.Warn("检测到 Region 未就绪，准备重试导入",
				zap.String("file", filename),
				zap.Int("attempt", attempt),
				zap.Error(err),
			)

			if im.regionReady != nil {
				if readyErr := im.regionReady(ctx); readyErr != nil {
					lastErr = fmt.Errorf("等待 Region 就绪失败: %w", readyErr)
					if attempt == maxAttempts {
						return lastErr
					}
				}
			}

			time.Sleep(time.Duration(attempt) * importRetryBaseDelay)
			continue
		}

		return nil
	}

	return lastErr
}

func (im *Importer) runLoadCommand(ctx context.Context, filePath string) error {
	cmd := fmt.Sprintf("%s -h %s -e \"load '%s' verify=false\"",
		im.config.IoTDB.CLIPath,
		im.config.IoTDB.Host,
		filePath,
	)

	stdout, stderr, err := im.executor.Exec(ctx, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("执行命令失败: %w: %s", err, strings.TrimSpace(stderr))
	}

	if !containsSuccess(stdout) && !containsSuccess(stderr) {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		return fmt.Errorf("导入失败: %s", msg)
	}

	return nil
}

func isRetryableImportError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	if strings.Contains(message, "readonly") {
		return false
	}

	retryablePatterns := []string{
		"schemaregion",
		"doesn't exist",
		"failed to get replicaset of consensus group",
		"auto create or verify schema error",
		"consensus group",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}

	if strings.Contains(message, "status code: 701") &&
		(strings.Contains(message, "schema") || strings.Contains(message, "region")) {
		return true
	}

	return false
}

// containsSuccess 检查输出是否包含成功标记。
func containsSuccess(output string) bool {
	message := strings.ToLower(output)
	return strings.Contains(message, "successfully") ||
		strings.Contains(message, "success")
}

// logMemoryUsage 记录内存使用情况
func (im *Importer) logMemoryUsage(ctx context.Context) {
	cmd := "free -m | grep Mem | awk '{print $7}'"
	output, err := im.executor.ExecSimple(ctx, cmd)
	if err != nil {
		logger.Debug("获取内存使用失败", zap.Error(err))
		return
	}

	var freeMem int
	if _, err := fmt.Sscanf(output, "%d", &freeMem); err == nil {
		logger.Debug("当前可用内存", zap.Int("mb", freeMem))
	}
}
