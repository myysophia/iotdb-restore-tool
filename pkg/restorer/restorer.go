package restorer

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
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
	exists, err := r.executor.FileExists(ctx, filepath.Join("/tmp", backupFile))
	if err != nil {
		return err
	}

	if exists {
		logger.Info("备份文件已存在，跳过下载")
		return nil
	}

	// 在 Pod 中执行下载命令
	backupURL := fmt.Sprintf("%s/%s", r.config.Backup.BaseURL, backupFile)
	cmd := fmt.Sprintf("wget -q -O /tmp/%s '%s'", backupFile, backupURL)

	logger.Info("开始下载备份文件",
		zap.String("url", backupURL),
		zap.String("dest", "/tmp/"+backupFile),
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

	// 解压文件
	extractCmd := fmt.Sprintf("tar --overwrite -xzf /tmp/%s -C %s/", backupFile, r.config.IoTDB.DataDir)

	logger.Info("开始解压", zap.String("cmd", extractCmd))

	stdout, stderr, err := r.executor.Exec(ctx, []string{"sh", "-c", extractCmd})
	if err != nil {
		return fmt.Errorf("解压失败: %w: %s", err, stderr)
	}

	logger.Info("解压完成", zap.String("output", stdout))

	// 显示解压后的文件结构
	listCmd := fmt.Sprintf("find %s -type f | head -20", r.config.IoTDB.DataDir)
	listOutput, err := r.executor.ExecSimple(ctx, listCmd)
	if err == nil {
		logger.Info("解压后的文件结构", zap.String("files", listOutput))
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
