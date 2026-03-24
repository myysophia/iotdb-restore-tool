package restorer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/downloader"
	"github.com/vnnox/iotdb-restore-tool/pkg/k8s"
	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
)

const (
	podBackupPath      = "/tmp"
	regionReadyTimeout = 10 * time.Minute
	regionPollInterval = 5 * time.Second
	probeSeriesPath    = "root.energy.__restore_probe.restore_check"
	probeTimeseriesSQL = "create timeseries root.energy.__restore_probe.restore_check with datatype=INT64, encoding=RLE, compressor=SNAPPY"
)

var managedDatabases = []string{"root.emsplus", "root.energy"}

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

// ProbeResult 记录恢复后的数据库写读探测结果。
type ProbeResult struct {
	Executed    bool
	Database    string
	SeriesPath  string
	Timestamp   int64
	Value       int64
	QueryResult string
	Error       string
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
	Probe        *ProbeResult
	Error        error
}

// IoTDBRestorer IoTDB 恢复器
type IoTDBRestorer struct {
	executor  *k8s.Executor
	config    *config.Config
	result    *RestoreResult
	startTime time.Time
}

type regionSnapshot struct {
	Databases         map[string]bool
	RunningSchema     map[string]int
	RunningData       map[string]int
	LastObservedAtUTC time.Time
}

// NewRestorer 创建恢复器
func NewRestorer(executor *k8s.Executor, cfg *config.Config) *IoTDBRestorer {
	return &IoTDBRestorer{
		executor: executor,
		config:   cfg,
	}
}

// Restore 执行完整的恢复流程
func (r *IoTDBRestorer) Restore(ctx context.Context, opts RestoreOptions) (result *RestoreResult, err error) {
	r.startTime = time.Now()
	r.result = &RestoreResult{
		StartTime: r.startTime,
		Timestamp: opts.Timestamp,
	}
	result = r.result

	defer func() {
		r.result.EndTime = time.Now()
		r.result.Duration = r.result.EndTime.Sub(r.result.StartTime)
		if err != nil {
			r.result.Error = err
		}
	}()

	logger.Info("开始执行恢复操作",
		zap.String("timestamp", opts.Timestamp),
		zap.Bool("dry_run", opts.DryRun),
		zap.Bool("skip_delete", opts.SkipDelete),
	)

	if opts.DryRun {
		return r.dryRun(ctx)
	}

	if !opts.SkipDelete {
		if err = r.deleteDatabasesAndCleanup(ctx); err != nil {
			return r.result, fmt.Errorf("删除数据库和清理旧数据失败: %w", err)
		}

		if err = r.restartPodAndWaitReady(ctx); err != nil {
			return r.result, fmt.Errorf("重启并等待 Pod 就绪失败: %w", err)
		}
	}

	if err = r.ensureDatabasesAndRegionsReady(ctx); err != nil {
		return r.result, fmt.Errorf("数据库和 Region 就绪检查失败: %w", err)
	}

	backupFile := fmt.Sprintf("emsau_%s_%s.tar.gz", r.config.Kubernetes.PodName, opts.Timestamp)
	r.result.BackupFile = backupFile

	if err = r.downloadBackup(ctx, backupFile); err != nil {
		return r.result, fmt.Errorf("下载备份文件失败: %w", err)
	}
	defer r.cleanup(ctx, backupFile)

	if err = r.extractBackup(ctx, backupFile); err != nil {
		return r.result, fmt.Errorf("解压备份文件失败: %w", err)
	}

	importResult, err := r.importTsFiles(ctx)
	if err != nil {
		return r.result, fmt.Errorf("导入 tsfile 文件失败: %w", err)
	}

	r.result.TotalFiles = importResult.TotalFiles
	r.result.SuccessCount = importResult.SuccessCount
	r.result.FailedCount = importResult.FailedCount

	if err = r.verifyDatabaseWriteRead(ctx); err != nil {
		return r.result, fmt.Errorf("数据库写读探测失败: %w", err)
	}

	logger.Info("恢复操作完成",
		zap.Int("total_files", r.result.TotalFiles),
		zap.Int("success_count", r.result.SuccessCount),
		zap.Int("failed_count", r.result.FailedCount),
		zap.Duration("duration", time.Since(r.startTime)),
	)

	return r.result, nil
}

// downloadBackup 下载备份文件到 Pod
func (r *IoTDBRestorer) downloadBackup(ctx context.Context, backupFile string) error {
	logger.Info("步骤 1: 下载备份文件", zap.String("file", backupFile))

	remotePath := filepath.Join(podBackupPath, backupFile)
	exists, err := r.executor.FileExists(ctx, remotePath)
	if err != nil {
		return err
	}

	if exists {
		logger.Info("备份文件已存在，跳过下载")
		return nil
	}

	backupURL := fmt.Sprintf("%s/%s", r.config.Backup.BaseURL, backupFile)
	strategy := r.config.Backup.DownloadStrategy
	if strategy == "" {
		strategy = "local"
	}

	logger.Info("使用下载策略", zap.String("strategy", strategy))

	switch strategy {
	case "local":
		return r.downloadAndTransfer(ctx, backupURL, backupFile, remotePath)
	case "pod":
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

	defer func() {
		if removeErr := os.Remove(localPath); removeErr != nil {
			logger.Warn("清理本地文件失败",
				zap.String("path", localPath),
				zap.Error(removeErr),
			)
		} else {
			logger.Info("本地文件已清理", zap.String("path", localPath))
		}
	}()

	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			logger.Warn("传输重试",
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", maxRetries),
			)
			time.Sleep(time.Duration(attempt) * 5 * time.Second)
		}

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

			if attempt == maxRetries-1 {
				logger.Warn("本地传输失败，降级到 Pod 下载", zap.Error(err))
				return r.downloadInPod(ctx, backupURL, backupFile, remotePath)
			}
			continue
		}

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

// extractBackup 解压备份文件到 data_dir，避免覆盖运行中节点的持久化元数据。
func (r *IoTDBRestorer) extractBackup(ctx context.Context, backupFile string) error {
	logger.Info("步骤 2: 解压备份文件到数据目录")

	checkCmd := fmt.Sprintf("ls -lh %s/%s | awk '{print $5}'", podBackupPath, backupFile)
	sizeOutput, _ := r.executor.ExecSimple(ctx, checkCmd)
	logger.Info("备份文件大小", zap.String("size", strings.TrimSpace(sizeOutput)))

	scanRoot := r.restoreScanRoot()
	extractCmd := fmt.Sprintf("cd %s && tar --overwrite -I 'pigz -p 4' -xf %s -C %s 2>&1 | tail -10", podBackupPath, backupFile, r.config.IoTDB.DataDir)
	fallbackCmd := fmt.Sprintf("cd %s && tar --overwrite -xzf %s -C %s 2>&1 | tail -10", podBackupPath, backupFile, r.config.IoTDB.DataDir)

	checkPigz := "command -v pigz >/dev/null 2>&1"
	if _, _, err := r.executor.Exec(ctx, []string{"sh", "-c", checkPigz}); err == nil {
		logger.Info("使用 pigz 并行解压（4 线程）")
		logger.Info("开始解压", zap.String("cmd", extractCmd))
		stdout, stderr, execErr := r.executor.Exec(ctx, []string{"sh", "-c", extractCmd})
		if execErr != nil {
			logger.Warn("pigz 解压失败，尝试使用 gzip", zap.Error(execErr))
			logger.Info("开始解压", zap.String("cmd", fallbackCmd))
			stdout, stderr, execErr = r.executor.Exec(ctx, []string{"sh", "-c", fallbackCmd})
			if execErr != nil {
				return fmt.Errorf("解压失败: %w: %s", execErr, stderr)
			}
		}
		logger.Info("解压完成", zap.String("output", stdout))
	} else {
		logger.Info("pigz 不可用，使用 gzip 单线程解压（建议安装 pigz 以加速）")
		logger.Info("开始解压", zap.String("cmd", fallbackCmd))
		stdout, stderr, execErr := r.executor.Exec(ctx, []string{"sh", "-c", fallbackCmd})
		if execErr != nil {
			return fmt.Errorf("解压失败: %w: %s", execErr, stderr)
		}
		logger.Info("解压完成", zap.String("output", stdout))
	}

	listOutput, err := r.executor.ExecSimple(ctx, fmt.Sprintf("find %s -type f | head -20", scanRoot))
	if err == nil {
		logger.Info("解压后的文件结构", zap.String("files", listOutput))
	}

	statCmd := fmt.Sprintf("find %s -type f | wc -l && du -sh %s", scanRoot, scanRoot)
	statOutput, _ := r.executor.ExecSimple(ctx, statCmd)
	if statOutput != "" {
		logger.Info("解压统计", zap.String("stats", strings.TrimSpace(statOutput)))
	}

	return nil
}

func (r *IoTDBRestorer) deleteDatabasesAndCleanup(ctx context.Context) error {
	logger.Info("步骤 0: 删除现有数据库并清理旧数据")

	for _, db := range managedDatabases {
		sql := fmt.Sprintf("delete database %s", db)
		if _, _, err := r.execSQL(ctx, sql); err != nil {
			logger.Warn("删除数据库失败，继续执行",
				zap.String("database", db),
				zap.Error(err),
			)
		} else {
			logger.Info("删除数据库成功", zap.String("database", db))
		}
	}

	if _, _, err := r.execSQL(ctx, "flush"); err != nil {
		logger.Warn("刷新数据失败", zap.Error(err))
	}

	liveDataRoot := r.liveDataDir()
	cleanupCommands := []string{
		"rm -rf /iotdb/data/backup_before_restore /iotdb/data/backup_before_restore_old_*",
		fmt.Sprintf("mkdir -p %s && rm -rf %s/* %s/.[!.]* %s/..?* 2>/dev/null || true", liveDataRoot, liveDataRoot, liveDataRoot, liveDataRoot),
	}

	for _, cmd := range cleanupCommands {
		if _, _, err := r.executor.Exec(ctx, []string{"sh", "-c", cmd}); err != nil {
			return fmt.Errorf("执行清理命令失败: %s: %w", cmd, err)
		}
	}

	return nil
}

func (r *IoTDBRestorer) restartPodAndWaitReady(ctx context.Context) error {
	logger.Info("步骤 1: 重启 Pod 并等待 Ready")

	checker := k8s.NewPodChecker(r.executor.Clientset, r.executor.Namespace)
	var previousUID string
	existingPod, err := checker.GetPod(ctx, r.executor.PodName)
	if err == nil && existingPod != nil {
		previousUID = string(existingPod.UID)
	}

	if err := checker.Delete(ctx, r.executor.PodName); err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, regionReadyTimeout)
	defer cancel()

	ticker := time.NewTicker(regionPollInterval)
	defer ticker.Stop()

	for {
		ready, pod, err := checker.IsReady(waitCtx, r.executor.PodName)
		if err != nil {
			return err
		}

		phase := "NotFound"
		readyCount := 0
		totalCount := 0
		currentUID := ""
		deleting := false
		if pod != nil {
			phase = string(pod.Status.Phase)
			totalCount = len(pod.Status.ContainerStatuses)
			currentUID = string(pod.UID)
			deleting = pod.DeletionTimestamp != nil
			for _, status := range pod.Status.ContainerStatuses {
				if status.Ready {
					readyCount++
				}
			}
		}

		logger.Info("等待 Pod 就绪",
			zap.String("pod", r.executor.PodName),
			zap.String("previous_uid", previousUID),
			zap.String("current_uid", currentUID),
			zap.String("phase", phase),
			zap.Bool("deleting", deleting),
			zap.Int("ready_containers", readyCount),
			zap.Int("total_containers", totalCount),
		)

		if ready && currentUID != "" && currentUID != previousUID && !deleting {
			logger.Info("Pod 已就绪", zap.String("pod", r.executor.PodName))
			return nil
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("等待 Pod Ready 超时: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (r *IoTDBRestorer) ensureDatabasesAndRegionsReady(ctx context.Context) error {
	logger.Info("步骤 2: 创建数据库并等待 Schema/Data Region 就绪")

	waitCtx, cancel := context.WithTimeout(ctx, regionReadyTimeout)
	defer cancel()

	ticker := time.NewTicker(regionPollInterval)
	defer ticker.Stop()

	var lastSnapshot *regionSnapshot
	for {
		snapshot, err := r.ensureDatabasesAndCollectSnapshot(waitCtx)
		if err != nil {
			logger.Warn("数据库或 Region 尚未可用，继续等待",
				zap.Error(err),
			)
		} else {
			lastSnapshot = snapshot

			allReady := true
			for _, db := range managedDatabases {
				dbExists := snapshot.Databases[db]
				schemaCount := snapshot.RunningSchema[db]
				dataCount := snapshot.RunningData[db]
				logger.Info("Region 就绪检查",
					zap.String("database", db),
					zap.Bool("database_exists", dbExists),
					zap.Int("running_schema_regions", schemaCount),
					zap.Int("running_data_regions", dataCount),
				)

				if !dbExists || schemaCount == 0 || dataCount == 0 {
					allReady = false
				}
			}

			if allReady {
				logger.Info("所有数据库和 Region 已就绪")
				return nil
			}
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("等待数据库和 Region 就绪超时，最后状态: %s", formatRegionSnapshot(lastSnapshot))
		case <-ticker.C:
		}
	}
}

func (r *IoTDBRestorer) ensureDatabasesAndCollectSnapshot(ctx context.Context) (*regionSnapshot, error) {
	databaseOutput, _, err := r.execSQL(ctx, "show databases")
	if err != nil {
		return nil, fmt.Errorf("show databases 执行失败: %w", err)
	}

	databases := parseDatabaseList(databaseOutput)
	for _, db := range managedDatabases {
		if databases[db] {
			continue
		}

		sql := fmt.Sprintf("create database %s", db)
		if _, _, err := r.execSQL(ctx, sql); err != nil {
			return nil, fmt.Errorf("创建数据库失败 %s: %w", db, err)
		}
	}

	if err := r.bootstrapRegions(ctx); err != nil {
		return nil, err
	}

	return r.collectRegionSnapshot(ctx)
}

func (r *IoTDBRestorer) collectRegionSnapshot(ctx context.Context) (*regionSnapshot, error) {
	databaseOutput, _, err := r.execSQL(ctx, "show databases")
	if err != nil {
		return nil, fmt.Errorf("查询数据库列表失败: %w", err)
	}

	schemaOutput, _, err := r.execSQL(ctx, "show schema regions")
	if err != nil {
		return nil, fmt.Errorf("查询 SchemaRegion 失败: %w", err)
	}

	dataOutput, _, err := r.execSQL(ctx, "show data regions")
	if err != nil {
		return nil, fmt.Errorf("查询 DataRegion 失败: %w", err)
	}

	snapshot := &regionSnapshot{
		Databases:         parseDatabaseList(databaseOutput),
		RunningSchema:     parseRunningRegionCounts(schemaOutput),
		RunningData:       parseRunningRegionCounts(dataOutput),
		LastObservedAtUTC: time.Now().UTC(),
	}
	return snapshot, nil
}

// importTsFiles 导入 tsfile 文件
func (r *IoTDBRestorer) importTsFiles(ctx context.Context) (*ImportResult, error) {
	logger.Info("步骤 3: 开始导入 tsfile 文件")

	findCmd := fmt.Sprintf("find %s -name '*.tsfile' -type f", r.restoreScanRoot())
	output, stderr, err := r.executor.Exec(ctx, []string{"sh", "-c", findCmd})
	if err != nil {
		return nil, fmt.Errorf("查找 tsfile 文件失败: %w: %s", err, stderr)
	}

	if output == "" {
		return nil, fmt.Errorf("未找到任何 tsfile 文件")
	}

	files := parseFileList(output)
	logger.Info("找到 tsfile 文件", zap.Int("count", len(files)))

	importer := NewImporter(r.executor, r.config, r.ensureDatabasesAndRegionsReady)
	return importer.Import(ctx, files)
}

func (r *IoTDBRestorer) verifyDatabaseWriteRead(ctx context.Context) error {
	logger.Info("步骤 4: 执行数据库写入和查询探测")

	probe := &ProbeResult{
		Executed:   true,
		Database:   "root.energy",
		SeriesPath: probeSeriesPath,
	}
	r.result.Probe = probe

	stdout, stderr, err := r.execSQL(ctx, probeTimeseriesSQL)
	if err != nil && !containsAlreadyExists(stdout, stderr, err) {
		probe.Error = err.Error()
		return err
	}

	probe.Timestamp = time.Now().UnixMilli()
	probe.Value = probe.Timestamp

	insertSQL := fmt.Sprintf(
		"insert into root.energy.__restore_probe(time, restore_check) values(%d, %d)",
		probe.Timestamp,
		probe.Value,
	)
	if _, _, err := r.execSQL(ctx, insertSQL); err != nil {
		probe.Error = err.Error()
		return err
	}

	querySQL := fmt.Sprintf(
		"select restore_check from root.energy.__restore_probe where time = %d",
		probe.Timestamp,
	)
	queryOutput, _, err := r.execSQL(ctx, querySQL)
	if err != nil {
		probe.Error = err.Error()
		return err
	}

	queryValue, err := extractSingleQueryValue(queryOutput)
	if err != nil {
		probe.Error = err.Error()
		return err
	}

	probe.QueryResult = queryValue
	if queryValue != strconv.FormatInt(probe.Value, 10) {
		probe.Error = fmt.Sprintf("探测查询值不匹配: expect=%d actual=%s", probe.Value, queryValue)
		return fmt.Errorf(probe.Error)
	}

	logger.Info("数据库探测成功",
		zap.String("series", probe.SeriesPath),
		zap.Int64("timestamp", probe.Timestamp),
		zap.Int64("value", probe.Value),
	)
	return nil
}

// cleanup 清理临时文件
func (r *IoTDBRestorer) cleanup(ctx context.Context, backupFile string) {
	logger.Info("步骤 5: 清理临时文件")

	cleanupCmd := fmt.Sprintf("rm -f %s/%s", podBackupPath, backupFile)
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

func (r *IoTDBRestorer) execSQL(ctx context.Context, sql string) (string, string, error) {
	cmd := fmt.Sprintf("%s -h %s -e \"%s\"",
		r.config.IoTDB.CLIPath,
		r.config.IoTDB.Host,
		strings.ReplaceAll(sql, "\"", "\\\""),
	)
	return r.executor.Exec(ctx, []string{"sh", "-c", cmd})
}

func (r *IoTDBRestorer) liveDataDir() string {
	return filepath.Join(r.config.IoTDB.DataDir, "datanode", "data")
}

func (r *IoTDBRestorer) restoreScanRoot() string {
	return filepath.Join(r.config.IoTDB.DataDir, strings.TrimPrefix(r.liveDataDir(), "/"))
}

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

func parseDatabaseList(output string) map[string]bool {
	rows := parseCLITable(output)
	result := make(map[string]bool, len(rows))
	for _, row := range rows {
		db := strings.TrimSpace(row["Database"])
		if db != "" {
			result[db] = true
		}
	}
	return result
}

func parseRunningRegionCounts(output string) map[string]int {
	rows := parseCLITable(output)
	counts := make(map[string]int)
	for _, row := range rows {
		if !strings.EqualFold(strings.TrimSpace(row["Status"]), "Running") {
			continue
		}
		db := strings.TrimSpace(row["Database"])
		if db == "" {
			continue
		}
		counts[db]++
	}
	return counts
}

func formatRegionSnapshot(snapshot *regionSnapshot) string {
	if snapshot == nil {
		return "no snapshot"
	}
	parts := make([]string, 0, len(managedDatabases))
	for _, db := range managedDatabases {
		parts = append(parts, fmt.Sprintf(
			"%s(db=%t,schema=%d,data=%d)",
			db,
			snapshot.Databases[db],
			snapshot.RunningSchema[db],
			snapshot.RunningData[db],
		))
	}
	return strings.Join(parts, ", ")
}

func (r *IoTDBRestorer) databaseExists(ctx context.Context, database string) (bool, error) {
	output, _, err := r.execSQL(ctx, "show databases")
	if err != nil {
		return false, err
	}
	return parseDatabaseList(output)[database], nil
}

func (r *IoTDBRestorer) bootstrapRegions(ctx context.Context) error {
	now := time.Now().UnixMilli()
	bootstrapSeries := []string{
		"root.emsplus.__restore_bootstrap.status",
		"root.energy.__restore_bootstrap.status",
	}

	for _, series := range bootstrapSeries {
		createSQL := fmt.Sprintf(
			"create timeseries %s with datatype=INT64, encoding=RLE, compressor=SNAPPY",
			series,
		)
		stdout, stderr, err := r.execSQL(ctx, createSQL)
		if err != nil && !containsAlreadyExists(stdout, stderr, err) {
			return fmt.Errorf("创建 bootstrap timeseries 失败 %s: %w", series, err)
		}

		insertSQL := fmt.Sprintf(
			"insert into %s(time,status) values(%d,1)",
			strings.TrimSuffix(series, ".status"),
			now,
		)
		if _, _, err := r.execSQL(ctx, insertSQL); err != nil {
			return fmt.Errorf("写入 bootstrap 数据失败 %s: %w", series, err)
		}
	}

	return nil
}

func containsAlreadyExists(stdout, stderr string, err error) bool {
	combined := strings.ToLower(strings.Join([]string{stdout, stderr, err.Error()}, " "))
	return strings.Contains(combined, "already exist") ||
		strings.Contains(combined, "already been created") ||
		strings.Contains(combined, "path already exists")
}

func extractSingleQueryValue(output string) (string, error) {
	rows := parseCLITable(output)
	if len(rows) == 0 {
		return "", fmt.Errorf("查询结果为空")
	}

	row := rows[0]
	for key, value := range row {
		if strings.EqualFold(strings.TrimSpace(key), "Time") {
			continue
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
	}

	return "", fmt.Errorf("未找到探测查询值")
}
