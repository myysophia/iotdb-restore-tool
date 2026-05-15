package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/downloader"
	"github.com/vnnox/iotdb-restore-tool/pkg/k8s"
	"github.com/vnnox/iotdb-restore-tool/pkg/lock"
	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"github.com/vnnox/iotdb-restore-tool/pkg/notifier"
	"github.com/vnnox/iotdb-restore-tool/pkg/restorer"
	"go.uber.org/zap"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

var (
	configFile  string
	namespace   string
	podName     string
	timestamp   string
	concurrency int
	batchSize   int
	dryRun      bool
	skipDelete  bool
	debug       bool
	versionFlag bool
)

func main() {
	if hasVersionArg(os.Args[1:]) {
		printVersion()
		return
	}

	var rootCmd = &cobra.Command{
		Use:   "iotdb-restore",
		Short: "IoTDB 数据库恢复工具",
		Long:  `IoTDB 数据库恢复工具 - 用于从 OSS 下载备份文件并恢复到 IoTDB 数据库`,
		Run:   runRoot,
	}

	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "configs/config.yaml", "配置文件路径")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes 命名空间（覆盖配置文件）")
	rootCmd.PersistentFlags().StringVarP(&podName, "pod-name", "p", "", "Pod 名称（覆盖配置文件）")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "调试模式")
	rootCmd.PersistentFlags().BoolVar(&versionFlag, "version", false, "显示版本信息并退出")

	// restore 命令
	var restoreCmd = &cobra.Command{
		Use:   "restore",
		Short: "执行数据恢复",
		Long:  `从 OSS 下载备份文件并恢复到 IoTDB 数据库`,
		RunE:  runRestore,
	}

	restoreCmd.Flags().StringVarP(&timestamp, "timestamp", "t", "", "备份文件时间戳（如：20260203083502）")
	restoreCmd.Flags().IntVar(&concurrency, "concurrency", 0, "并发数（覆盖配置文件）")
	restoreCmd.Flags().IntVar(&batchSize, "batch-size", 0, "批次大小（覆盖配置文件）")
	restoreCmd.Flags().BoolVar(&dryRun, "dry-run", false, "干运行模式（仅检查，不执行）")
	restoreCmd.Flags().BoolVar(&skipDelete, "skip-delete", false, "跳过删除现有数据库")

	// check 命令
	var checkCmd = &cobra.Command{
		Use:   "check",
		Short: "检查 Pod 状态",
		Long:  `检查 Kubernetes Pod 的运行状态和连接性`,
		RunE:  runCheck,
	}

	// version 命令
	var versionCmd = &cobra.Command{
		Use:   "version",
		Short: "显示版本信息",
		Run: func(cmd *cobra.Command, args []string) {
			printVersion()
		},
	}

	rootCmd.AddCommand(restoreCmd)
	rootCmd.AddCommand(checkCmd)
	rootCmd.AddCommand(versionCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func runRoot(cmd *cobra.Command, args []string) {
	if versionFlag {
		printVersion()
		return
	}

	fmt.Println("IoTDB 数据库恢复工具")
	fmt.Println("请使用子命令: restore, check, version")
	fmt.Println("\n示例:")
	fmt.Println("  iotdb-restore restore -t 20260203083502")
	fmt.Println("  iotdb-restore check")
	fmt.Println("  iotdb-restore --help")
}

func runRestore(cmd *cobra.Command, args []string) error {
	// 加载配置
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	// 覆盖配置
	if namespace != "" {
		cfg.Kubernetes.Namespace = namespace
	}
	if podName != "" {
		cfg.Kubernetes.PodName = podName
	}
	if concurrency > 0 {
		cfg.Import.Concurrency = concurrency
	}
	if batchSize > 0 {
		cfg.Import.BatchSize = batchSize
	}

	// 调试模式
	if debug {
		cfg.Log.Level = "debug"
	}

	// 初始化日志
	if err := logger.Init(cfg.Log.Level, cfg.Log.Format); err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer logger.Sync()

	logger.Info("IoTDB Restore Tool 启动",
		zap.String("version", Version),
		zap.String("namespace", cfg.Kubernetes.Namespace),
		zap.String("pod", cfg.Kubernetes.PodName),
		zap.String("source_type", cfg.Backup.SourceType),
		zap.Int("concurrency", cfg.Import.Concurrency),
		zap.Int("batch_size", cfg.Import.BatchSize),
		zap.Bool("dry_run", dryRun),
	)

	// 创建文件锁（防止并发执行）
	fileLock, err := lock.NewFileLock("/tmp", "iotdb-restore")
	if err != nil {
		logger.Error("创建文件锁失败", zap.Error(err))
		return fmt.Errorf("创建文件锁失败: %w", err)
	}

	// 尝试获取锁
	if err := fileLock.TryLock(); err != nil {
		logger.Error("无法获取锁，可能已有任务在运行",
			zap.Error(err),
			zap.String("lock_file", "/tmp/iotdb-restore.lock"),
		)
		fmt.Fprintf(os.Stderr, "\n⚠️  错误: %v\n", err)
		fmt.Fprintf(os.Stderr, "\n如果确定没有其他任务在运行，可以手动删除锁文件:\n")
		fmt.Fprintf(os.Stderr, "  rm -f /tmp/iotdb-restore.lock\n\n")
		return err
	}
	defer fileLock.Unlock()

	logger.Info("文件锁获取成功", zap.String("lock_file", "/tmp/iotdb-restore.lock"))

	// 执行恢复
	if err := executeRestore(cfg); err != nil {
		logger.Error("恢复失败", zap.Error(err))
		return err
	}

	return nil
}

func runCheck(cmd *cobra.Command, args []string) error {
	// 加载配置
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	if namespace != "" {
		cfg.Kubernetes.Namespace = namespace
	}
	if podName != "" {
		cfg.Kubernetes.PodName = podName
	}

	// 初始化日志
	if err := logger.Init(cfg.Log.Level, cfg.Log.Format); err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer logger.Sync()

	if err := executeCheck(cfg); err != nil {
		logger.Error("检查失败", zap.Error(err))
		return err
	}

	return nil
}

func executeRestore(cfg *config.Config) error {
	ctx := context.Background()

	// 1. 检测或验证时间戳（仅 OSS 模式使用）
	if cfg.Backup.UsesClusterStream() {
		if timestamp != "" {
			logger.Info("当前为同集群直连恢复模式，忽略 timestamp 参数",
				zap.String("timestamp", timestamp),
			)
		}
		timestamp = ""
	} else {
		if timestamp == "" {
			detector := downloader.NewDetector()
			detectedTimestamp, err := detector.DetectTimestamp(ctx, cfg.Backup.BaseURL, cfg.Kubernetes.PodName)
			if err != nil {
				return fmt.Errorf("自动检测时间戳失败: %w", err)
			}
			timestamp = detectedTimestamp
			logger.Info("自动检测到时间戳", zap.String("timestamp", timestamp))
		} else {
			// 验证时间戳格式
			if err := downloader.ValidateTimestamp(timestamp); err != nil {
				return fmt.Errorf("时间戳格式错误: %w", err)
			}
		}
	}

	// 2. 创建 Kubernetes 客户端
	clientset, err := k8s.NewClient(cfg.Kubernetes.KubeConfig)
	if err != nil {
		return fmt.Errorf("创建 Kubernetes 客户端失败: %w", err)
	}

	// 3. 检查 Pod
	checker := k8s.NewPodChecker(clientset, cfg.Kubernetes.Namespace)
	exists, err := checker.Exists(ctx, cfg.Kubernetes.PodName)
	if err != nil {
		return fmt.Errorf("检查 Pod 失败: %w", err)
	}
	if !exists {
		return fmt.Errorf("Pod %s/%s 不存在", cfg.Kubernetes.Namespace, cfg.Kubernetes.PodName)
	}

	running, err := checker.IsRunning(ctx, cfg.Kubernetes.PodName)
	if err != nil {
		return fmt.Errorf("检查 Pod 状态失败: %w", err)
	}
	if !running {
		logger.Warn("Pod 未处于运行状态", zap.String("pod", cfg.Kubernetes.PodName))
	}

	// 4. 创建执行器
	restConfig, err := k8s.NewConfig(cfg.Kubernetes.KubeConfig)
	if err != nil {
		return fmt.Errorf("创建 REST 配置失败: %w", err)
	}

	executor := k8s.NewExecutor(clientset, restConfig, cfg.Kubernetes.Namespace, cfg.Kubernetes.PodName, nil)

	// 5. 执行恢复
	r := restorer.NewRestorer(executor, cfg)
	result, err := r.Restore(ctx, restorer.RestoreOptions{
		Timestamp:  timestamp,
		DryRun:     dryRun,
		SkipDelete: skipDelete,
	})

	if err != nil {
		// 发送失败通知
		sendNotification(cfg, result, err)
		return err
	}

	// 6. 发送成功通知
	if err := sendNotification(cfg, result, nil); err != nil {
		logger.Warn("发送通知失败", zap.Error(err))
	}

	logger.Info("恢复操作完成",
		zap.Int("total_files", result.TotalFiles),
		zap.Int("success_count", result.SuccessCount),
		zap.Int("failed_count", result.FailedCount),
		zap.Duration("duration", result.Duration),
	)

	return nil
}

func executeCheck(cfg *config.Config) error {
	ctx := context.Background()

	fmt.Println("\n=== Kubernetes Pod 检查 ===")

	// 创建 Kubernetes 客户端
	fmt.Println("1. 连接 Kubernetes...")
	clientset, err := k8s.NewClient(cfg.Kubernetes.KubeConfig)
	if err != nil {
		return fmt.Errorf("创建 Kubernetes 客户端失败: %w", err)
	}

	if err := k8s.TestConnection(ctx, clientset); err != nil {
		return fmt.Errorf("测试连接失败: %w", err)
	}
	fmt.Println("   ✓ 连接成功")

	// 创建 Pod 检查器
	fmt.Printf("\n2. 检查 Pod %s/%s...\n", cfg.Kubernetes.Namespace, cfg.Kubernetes.PodName)
	checker := k8s.NewPodChecker(clientset, cfg.Kubernetes.Namespace)

	exists, err := checker.Exists(ctx, cfg.Kubernetes.PodName)
	if err != nil {
		return fmt.Errorf("检查 Pod 存在性失败: %w", err)
	}
	if !exists {
		return fmt.Errorf("Pod %s/%s 不存在", cfg.Kubernetes.Namespace, cfg.Kubernetes.PodName)
	}
	fmt.Println("   ✓ Pod 存在")

	phase, err := checker.GetStatus(ctx, cfg.Kubernetes.PodName)
	if err != nil {
		return fmt.Errorf("获取 Pod 状态失败: %w", err)
	}
	fmt.Printf("   ✓ Pod 状态: %s\n", *phase)

	running, err := checker.IsRunning(ctx, cfg.Kubernetes.PodName)
	if err != nil {
		return fmt.Errorf("检查 Pod 运行状态失败: %w", err)
	}
	if !running {
		fmt.Println("   ⚠ 警告: Pod 未处于运行状态")
	} else {
		fmt.Println("   ✓ Pod 正在运行")
	}

	// 测试命令执行
	fmt.Println("\n3. 测试命令执行...")
	restConfig, err := k8s.NewConfig(cfg.Kubernetes.KubeConfig)
	if err != nil {
		return fmt.Errorf("创建 REST 配置失败: %w", err)
	}

	executor := k8s.NewExecutor(clientset, restConfig, cfg.Kubernetes.Namespace, cfg.Kubernetes.PodName, nil)
	output, err := executor.ExecSimple(ctx, "pwd")
	if err != nil {
		return fmt.Errorf("执行命令失败: %w", err)
	}
	fmt.Printf("   ✓ 当前目录: %s", output)

	fmt.Println("\n=== 检查完成 ===")
	return nil
}

func sendNotification(cfg *config.Config, result *restorer.RestoreResult, err error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 如果有错误，设置到结果中
	if err != nil {
		result.Error = err
	}

	var errs []error

	// 企微通知：成功和失败都发送
	wechatNotifier := notifier.NewWechatNotifier(&cfg.Notification)
	if wechatErr := wechatNotifier.Send(ctx, result); wechatErr != nil {
		logger.Warn("发送企微通知失败", zap.Error(wechatErr))
		errs = append(errs, wechatErr)
	}

	// 电话告警：仅失败时发送
	if err != nil {
		phoneNotifier := notifier.NewPhoneNotifier(&cfg.Notification)
		if phoneErr := phoneNotifier.Send(ctx, result); phoneErr != nil {
			logger.Warn("发送电话告警失败", zap.Error(phoneErr))
			errs = append(errs, phoneErr)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("通知发送部分失败: %v", errs)
	}
	return nil
}

func printVersion() {
	fmt.Printf("IoTDB Restore Tool v%s (commit: %s, built at: %s)\n", Version, Commit, Date)
}

func hasVersionArg(args []string) bool {
	for _, arg := range args {
		if arg == "--version" {
			return true
		}
	}
	return false
}
