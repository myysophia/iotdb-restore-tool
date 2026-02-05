package restorer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/k8s"
)

// Batch 批次信息
type Batch struct {
	Number    int
	Files     []string
	Processed int
	Success   int
	Failed    int
	StartTime time.Time
	EndTime   time.Time
}

// Batcher 批次处理器
type Batcher struct {
	executor    *k8s.Executor
	config      *config.Config
	batchSize   int
	concurrency int
}

// NewBatcher 创建批次处理器
func NewBatcher(executor *k8s.Executor, cfg *config.Config) *Batcher {
	return &Batcher{
		executor:    executor,
		config:      cfg,
		batchSize:   cfg.Import.BatchSize,
		concurrency: cfg.Import.Concurrency,
	}
}

// ProcessBatches 处理多个批次
func (b *Batcher) ProcessBatches(ctx context.Context, files []string) ([]*Batch, error) {
	totalFiles := len(files)
	totalBatches := (totalFiles + b.batchSize - 1) / b.batchSize

	batches := make([]*Batch, 0, totalBatches)

	for i := 0; i < totalFiles; i += b.batchSize {
		end := i + b.batchSize
		if end > totalFiles {
			end = totalFiles
		}

		batchFiles := files[i:end]
		batchNum := i/b.batchSize + 1

		batch := &Batch{
			Number:    batchNum,
			Files:     batchFiles,
			StartTime: time.Now(),
		}

		// 处理批次
		success, failed, err := b.processBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("批次 %d 处理失败: %w", batchNum, err)
		}

		batch.Success = success
		batch.Failed = failed
		batch.Processed = len(batchFiles)
		batch.EndTime = time.Now()

		batches = append(batches, batch)

		// 记录进度
		progress := float64(end) / float64(totalFiles) * 100
		fmt.Printf("批次 %d/%d 完成: %d/%d 文件 (%.1f%%)\n",
			batchNum, totalBatches, end, totalFiles, progress,
		)

		// 批次间暂停
		if i+b.batchSize < totalFiles && b.config.Import.BatchPause {
			fmt.Printf("等待 %d 秒...\n", b.config.Import.BatchDelay)
			time.Sleep(time.Duration(b.config.Import.BatchDelay) * time.Second)
		}
	}

	return batches, nil
}

// processBatch 处理单个批次
func (b *Batcher) processBatch(ctx context.Context, batch *Batch) (int, int, error) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, b.concurrency)

	var successCount int32
	var failedCount int32

	for _, file := range batch.Files {
		wg.Add(1)
		go func(f string) {
			defer wg.Done()

			// 获取信号量
			sem <- struct{}{}
			defer func() { <-sem }()

			// 导入文件
			if err := b.importFile(ctx, f); err != nil {
				atomic.AddInt32(&failedCount, 1)
				// logger.Error("导入失败", zap.String("file", f), zap.Error(err))
			} else {
				atomic.AddInt32(&successCount, 1)
				// logger.Debug("导入成功", zap.String("file", f))
			}
		}(file)
	}

	wg.Wait()

	return int(successCount), int(failedCount), nil
}

// importFile 导入单个文件
func (b *Batcher) importFile(ctx context.Context, filePath string) error {
	cmd := fmt.Sprintf("%s -h %s -e \"load '%s' verify=false\"",
		b.config.IoTDB.CLIPath,
		b.config.IoTDB.Host,
		filePath,
	)

	stdout, stderr, err := b.executor.Exec(ctx, []string{"sh", "-c", cmd})

	if err != nil {
		return fmt.Errorf("执行失败: %w: %s", err, stderr)
	}

	if !containsSuccess(stdout) && !containsSuccess(stderr) {
		return fmt.Errorf("导入失败: %s", stderr)
	}

	return nil
}
