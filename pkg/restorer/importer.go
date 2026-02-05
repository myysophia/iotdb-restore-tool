package restorer

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/k8s"
	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
)

// ImportResult 导入结果
type ImportResult struct {
	TotalFiles   int
	SuccessCount int
	FailedCount  int
	Duration     time.Duration
}

// Importer tsfile 导入器
type Importer struct {
	executor *k8s.Executor
	config   *config.Config
}

// NewImporter 创建导入器
func NewImporter(executor *k8s.Executor, cfg *config.Config) *Importer {
	return &Importer{
		executor: executor,
		config:   cfg,
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
	)

	// 使用信号量控制并发数
	sem := make(chan struct{}, im.config.Import.Concurrency)

	// 原子计数器
	var successCount int64
	var failedCount int64

	var wg sync.WaitGroup

	// 分批处理
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

		// 显示内存使用
		im.logMemoryUsage(ctx)

		// 并发导入当前批次
		for _, file := range batch {
			wg.Add(1)
			go func(f string) {
				defer wg.Done()

				// 获取信号量
				sem <- struct{}{}
				defer func() { <-sem }()

				// 导入单个文件
				if err := im.importSingleFile(ctx, f); err != nil {
					atomic.AddInt64(&failedCount, 1)
					logger.Error("导入失败",
						zap.String("file", filepath.Base(f)),
						zap.Error(err),
					)
				} else {
					atomic.AddInt64(&successCount, 1)
					logger.Debug("导入成功",
						zap.String("file", filepath.Base(f)),
					)
				}
			}(file)
		}

		// 等待当前批次完成
		wg.Wait()

		// 批次间暂停
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

// importSingleFile 导入单个文件
func (im *Importer) importSingleFile(ctx context.Context, filePath string) error {
	filename := filepath.Base(filePath)

	logger.Debug("开始导入文件", zap.String("file", filename))

	// 构建 IoTDB load 命令
	cmd := fmt.Sprintf("%s -h %s -e \"load '%s' verify=false\"",
		im.config.IoTDB.CLIPath,
		im.config.IoTDB.Host,
		filePath,
	)

	// 执行命令
	stdout, stderr, err := im.executor.Exec(ctx, []string{"sh", "-c", cmd})

	// 检查是否成功
	if err != nil {
		return fmt.Errorf("执行命令失败: %w: %s", err, stderr)
	}

	// 检查输出中是否包含 "successfully"
	if !containsSuccess(stdout) && !containsSuccess(stderr) {
		return fmt.Errorf("导入失败: %s", stderr)
	}

	return nil
}

// containsSuccess 检查输出是否包含成功标记
func containsSuccess(output string) bool {
	return contains(output, "successfully") ||
		contains(output, "success") ||
		contains(output, "Success")
}

// contains 检查字符串是否包含子串（不区分大小写）
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
		 len(s) > len(substr) && (
			s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			indexOf(s, substr) >= 0))
}

// indexOf 简单的字符串查找
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
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
