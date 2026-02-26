package restorer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/downloader"
	"github.com/vnnox/iotdb-restore-tool/pkg/k8s"
	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
)

// Restorer 恢复器接口
type Restorer interface {
	Restore(ctx context.Context, opts RestoreOptions) (*RestoreResult, error)
}

// RestoreOptions 恢复选项
type RestoreOptions struct {
	Timestamp  string
	DryRun     bool
	SkipDelete bool // 跳过删除现有数据库
}

// RestoreResult 恢复结果
type RestoreResult struct {
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	TotalFiles   int
	SuccessCount int
	FailedCount  int
	BackupFile   string
	Timestamp    string
	Error        error
}

// IoTDBRestorer IoTDB 恢复器
type IoTDBRestorer struct {
	executor  *k8s.Executor
	config    *config.Config
	result    *RestoreResult
	startTime time.Time
}

// NewRestorer 创建恢复器
func NewRestorer(executor *k8s.Executor, cfg *config.Config) *IoTDBRestorer {
	return &IoTDBRestorer{
		executor: executor,
		config:   cfg,
	}
}

// Restore 执行完整的恢复流程
func (r *IoTDBRestorer) Restore(ctx context.Context, opts RestoreOptions) (*RestoreResult, error) {
	r.startTime = time.Now()
	r.result = &RestoreResult{
		StartTime: r.startTime,
		Timestamp: opts.Timestamp,
	}

	logger.Info("开始执行恢复操作",
		zap.String("timestamp", opts.Timestamp),
		zap.Bool("dry_run", opts.DryRun),
	)

	if opts.DryRun {
		return r.dryRun(ctx)
	}

	// 1. 下载备份文件
	backupFile := fmt.Sprintf("emsau_%s_%s.tar.gz", r.config.Kubernetes.PodName, opts.Timestamp)
	r.result.BackupFile = backupFile

	if err := r.downloadBackup(ctx, backupFile); err != nil {
		r.result.Error = err
		return r.result, fmt.Errorf("下载备份文件失败: %w", err)
	}

	// 2. 解压备份文件
	if err := r.extractBackup(ctx, backupFile); err != nil {
		r.result.Error = err
		return r.result, fmt.Errorf("解压备份文件失败: %w", err)
	}

	// 3. 删除现有数据库（可选）
	if !opts.SkipDelete {
		if err := r.deleteDatabases(ctx); err != nil {
			logger.Warn("删除数据库失败，继续执行", zap.Error(err))
		}
	}

	// 4. 导入 tsfile 文件
	importResult, err := r.importTsFiles(ctx)
	if err != nil {
		r.result.Error = err
		return r.result, fmt.Errorf("导入 tsfile 文件失败: %w", err)
	}

	r.result.TotalFiles = importResult.TotalFiles
	r.result.SuccessCount = importResult.SuccessCount
	r.result.FailedCount = importResult.FailedCount

	// 5. 清理临时文件
	r.cleanup(ctx, backupFile)

	// 6. 记录最终结果
	r.result.EndTime = time.Now()
	r.result.Duration = r.result.EndTime.Sub(r.result.StartTime)

	logger.Info("恢复操作完成",
		zap.Int("total_files", r.result.TotalFiles),
		zap.Int("success_count", r.result.SuccessCount),
		zap.Int("failed_count", r.result.FailedCount),
		zap.Duration("duration", r.result.Duration),
	)

	return r.result, nil
}

// downloadBackup 下载备份文件到 Pod
func (r *IoTDBRestorer) downloadBackup(ctx context.Context, backupFile string) error {
	logger.Info("步骤 1: 下载备份文件", zap.String("file", backupFile))

	// 检查文件是否已存在
	remotePath := filepath.Join("/tmp", backupFile)
	exists, err := r.executor.FileExists(ctx, remotePath)
	if err != nil {
		return err
	}

	if exists {
		logger.Info("备份文件已存在，跳过下载")
		return nil
	}

	// 构建备份文件 URL
	backupURL := fmt.Sprintf("%s/%s", r.config.Backup.BaseURL, backupFile)

	// 根据策略选择下载方式
	strategy := r.config.Backup.DownloadStrategy
	if strategy == "" {
		strategy = "local" // 默认使用本地下载+传输
	}

	logger.Info("使用下载策略", zap.String("strategy", strategy))

	switch strategy {
	case "local":
		// 本地下载 + 传输到 Pod
		return r.downloadAndTransfer(ctx, backupURL, backupFile, remotePath)
	case "pod":
		// Pod 直接下载（原有方式）
		return r.downloadInPod(ctx, backupURL, backupFile, remotePath)
	default:
		return fmt.Errorf("未知的下载策略: %s", strategy)
	}
}

// downloadAndTransfer 本地下载后传输到 Pod（新策略）
func (r *IoTDBRestorer) downloadAndTransfer(ctx context.Context, backupURL, backupFile, remotePath string) error {
	logger.Info("使用本地下载 + 传输策略",
		zap.String("url", backupURL),
		zap.String("remote", remotePath),
	)

	// 1. 本地下载
	downloader := downloader.NewOSSDownloader()
	localTempDir := r.config.Backup.LocalTempDir
	if localTempDir == "" {
		localTempDir = os.TempDir()
	}

	localPath, err := downloader.DownloadToLocal(ctx, backupURL, localTempDir)
	if err != nil {
		return fmt.Errorf("本地下载失败: %w", err)
	}

	logger.Info("本地下载完成", zap.String("path", localPath))

	// 2. 确保本地文件会被清理（使用 defer）
	defer func() {
		if err := os.Remove(localPath); err != nil {
			logger.Warn("清理本地文件失败",
				zap.String("path", localPath),
				zap.Error(err),
			)
		} else {
			logger.Info("本地文件已清理", zap.String("path", localPath))
		}
	}()

	// 3. 传输到 Pod（带重试）
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			logger.Warn("传输重试",
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", maxRetries),
			)
			time.Sleep(time.Duration(attempt) * 5 * time.Second)
		}

		// 创建传输器
		transfer := k8s.NewTransfer(
			r.executor.Clientset,
			r.executor.RestConfig,
			r.executor.Namespace,
			r.executor.PodName,
		)

		if err := transfer.CopyFile(ctx, localPath, remotePath); err != nil {
			logger.Warn("传输失败",
				zap.Int("attempt", attempt+1),
				zap.Error(err),
			)

			// 最后一次重试失败，降级到 Pod 下载
			if attempt == maxRetries-1 {
				logger.Warn("本地传输失败，降级到 Pod 下载", zap.Error(err))
				return r.downloadInPod(ctx, backupURL, backupFile, remotePath)
			}
			continue
		}

		// 传输成功
		logger.Info("文件传输完成",
			zap.String("local", localPath),
			zap.String("remote", remotePath),
		)
		return nil
	}

	return fmt.Errorf("传输失败，已重试 %d 次", maxRetries)
}

// downloadInPod 在 Pod 中直接下载（原有方式）
func (r *IoTDBRestorer) downloadInPod(ctx context.Context, backupURL, backupFile, remotePath string) error {
	logger.Info("使用 Pod 下载策略",
		zap.String("url", backupURL),
		zap.String("remote", remotePath),
	)

	cmd := fmt.Sprintf("wget -q -O '%s' '%s'", remotePath, backupURL)

	logger.Info("开始下载备份文件",
		zap.String("url", backupURL),
		zap.String("dest", remotePath),
	)

	stdout, stderr, err := r.executor.Exec(ctx, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("下载失败: %w: %s", err, stderr)
	}

	logger.Info("下载完成", zap.String("output", stdout))
	return nil
}


// extractBackup 解压备份文件
func (r *IoTDBRestorer) extractBackup(ctx context.Context, backupFile string) error {
	logger.Info("步骤 2: 解压备份文件")

	// 备份当前数据目录
	backupCmd := fmt.Sprintf(
		"[ -d %s/backup_before_restore ] && mv %s/backup_before_restore %s/backup_before_restore_old_$(date +%%s) || true",
		r.config.IoTDB.DataDir,
		r.config.IoTDB.DataDir,
		r.config.IoTDB.DataDir,
	)

	if _, _, err := r.executor.Exec(ctx, []string{"sh", "-c", backupCmd}); err != nil {
		logger.Warn("备份数据目录失败", zap.Error(err))
	}

	// 检查备份文件大小
	checkCmd := fmt.Sprintf("ls -lh /tmp/%s | awk '{print $5}'", backupFile)
	sizeOutput, _ := r.executor.ExecSimple(ctx, checkCmd)
	logger.Info("备份文件大小", zap.String("size", strings.TrimSpace(sizeOutput)))

	// 使用 pigz 进行并行解压（如果可用），否则使用 gzip
	// 方案 1: 优先使用 pigz（多线程）
	extractCmd := fmt.Sprintf("cd /tmp && tar --overwrite -I 'pigz -p 4' -xf %s -C %s/ 2>&1 | tail -10", backupFile, r.config.IoTDB.DataDir)

	// 降级方案：如果 pigz 不可用，使用普通 gzip
	fallbackCmd := fmt.Sprintf("cd /tmp && tar --overwrite -xzf %s -C %s/ 2>&1 | tail -10", backupFile, r.config.IoTDB.DataDir)

	// 先检查 pigz 是否可用
	checkPigz := "command -v pigz >/dev/null 2>&1"
	if _, _, err := r.executor.Exec(ctx, []string{"sh", "-c", checkPigz}); err == nil {
		logger.Info("使用 pigz 并行解压（4 线程）")
		logger.Info("开始解压", zap.String("cmd", extractCmd))
		stdout, stderr, err := r.executor.Exec(ctx, []string{"sh", "-c", extractCmd})
		if err != nil {
			logger.Warn("pigz 解压失败，尝试使用 gzip", zap.Error(err))
			// 降级到 gzip
			logger.Info("使用 gzip 单线程解压")
			logger.Info("开始解压", zap.String("cmd", fallbackCmd))
			stdout, stderr, err = r.executor.Exec(ctx, []string{"sh", "-c", fallbackCmd})
			if err != nil {
				return fmt.Errorf("解压失败: %w: %s", err, stderr)
			}
		}
		logger.Info("解压完成", zap.String("output", stdout))
	} else {
		logger.Info("pigz 不可用，使用 gzip 单线程解压（建议安装 pigz 以加速）")
		logger.Info("开始解压", zap.String("cmd", fallbackCmd))
		stdout, stderr, err := r.executor.Exec(ctx, []string{"sh", "-c", fallbackCmd})
		if err != nil {
			return fmt.Errorf("解压失败: %w: %s", err, stderr)
		}
		logger.Info("解压完成", zap.String("output", stdout))
	}

	// 显示解压后的文件结构
	listCmd := fmt.Sprintf("find %s -type f | head -20", r.config.IoTDB.DataDir)
	listOutput, err := r.executor.ExecSimple(ctx, listCmd)
	if err == nil {
		logger.Info("解压后的文件结构", zap.String("files", listOutput))
	}

	// 统计解压的文件数量和总大小
	statCmd := fmt.Sprintf("find %s -type f | wc -l && du -sh %s", r.config.IoTDB.DataDir, r.config.IoTDB.DataDir)
	statOutput, _ := r.executor.ExecSimple(ctx, statCmd)
	if statOutput != "" {
		logger.Info("解压统计", zap.String("stats", strings.TrimSpace(statOutput)))
	}

	return nil
}

// deleteDatabases 删除现有数据库
func (r *IoTDBRestorer) deleteDatabases(ctx context.Context) error {
	logger.Info("步骤 3: 删除现有数据库")

	databases := []string{"root.emsplus", "root.energy"}

	for _, db := range databases {
		cmd := fmt.Sprintf("%s -h %s -e \"delete database %s\" 2>/dev/null || echo 'Database %s does not exist'",
			r.config.IoTDB.CLIPath,
			r.config.IoTDB.Host,
			db,
			db,
		)

		_, _, err := r.executor.Exec(ctx, []string{"sh", "-c", cmd})
		if err != nil {
			logger.Warn("删除数据库失败",
				zap.String("database", db),
				zap.Error(err),
			)
		} else {
			logger.Info("删除数据库成功", zap.String("database", db))
		}
	}

	// 刷新数据
	flushCmd := fmt.Sprintf("%s -h %s -e \"flush\"", r.config.IoTDB.CLIPath, r.config.IoTDB.Host)
	if _, _, err := r.executor.Exec(ctx, []string{"sh", "-c", flushCmd}); err != nil {
		logger.Warn("刷新数据失败", zap.Error(err))
	}

	return nil
}

// importTsFiles 导入 tsfile 文件
func (r *IoTDBRestorer) importTsFiles(ctx context.Context) (*ImportResult, error) {
	logger.Info("步骤 4: 开始导入 tsfile 文件")

	// 查找所有 tsfile 文件
	findCmd := fmt.Sprintf("find %s/iotdb/data/datanode -name '*.tsfile' -type f", r.config.IoTDB.DataDir)
	output, stderr, err := r.executor.Exec(ctx, []string{"sh", "-c", findCmd})
	if err != nil {
		return nil, fmt.Errorf("查找 tsfile 文件失败: %w: %s", err, stderr)
	}

	if output == "" {
		return nil, fmt.Errorf("未找到任何 tsfile 文件")
	}

	// 创建导入器
	importer := NewImporter(r.executor, r.config)

	// 解析文件列表
	files := parseFileList(output)
	logger.Info("找到 tsfile 文件", zap.Int("count", len(files)))

	// 执行导入
	result, err := importer.Import(ctx, files)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// cleanup 清理临时文件
func (r *IoTDBRestorer) cleanup(ctx context.Context, backupFile string) {
	logger.Info("步骤 5: 清理临时文件")

	cleanupCmd := fmt.Sprintf("rm -f /tmp/%s", backupFile)
	if _, _, err := r.executor.Exec(ctx, []string{"sh", "-c", cleanupCmd}); err != nil {
		logger.Warn("清理临时文件失败", zap.Error(err))
	} else {
		logger.Info("临时文件已删除")
	}
}

// dryRun 干运行（仅检查，不实际执行）
func (r *IoTDBRestorer) dryRun(ctx context.Context) (*RestoreResult, error) {
	logger.Info("干运行模式，不执行实际操作")

	r.result.EndTime = time.Now()
	r.result.Duration = r.result.EndTime.Sub(r.result.StartTime)

	return r.result, nil
}

// parseFileList 解析文件列表
func parseFileList(output string) []string {
	lines := splitLines(output)
	files := make([]string, 0, len(lines))

	for _, line := range lines {
		if line != "" {
			files = append(files, line)
		}
	}

	return files
}

// splitLines 分割字符串为行
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}

	lines := make([]string, 0)
	start := 0

	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}

	if start < len(s) {
		lines = append(lines, s[start:])
	}

	return lines
}
