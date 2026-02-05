package config

import "time"

// Config 是应用程序的完整配置结构
type Config struct {
	Kubernetes   KubeConfig        `mapstructure:"kubernetes"`
	IoTDB        IoTDBConfig       `mapstructure:"iotdb"`
	Backup       BackupConfig      `mapstructure:"backup"`
	Import       ImportConfig      `mapstructure:"import"`
	Notification NotificationConfig `mapstructure:"notification"`
	Log          LogConfig         `mapstructure:"log"`
}

// KubeConfig Kubernetes 配置
type KubeConfig struct {
	Namespace  string `mapstructure:"namespace"`
	PodName    string `mapstructure:"pod_name"`
	KubeConfig string `mapstructure:"kubeconfig"`
	Context    string `mapstructure:"context"`
}

// IoTDBConfig IoTDB 数据库配置
type IoTDBConfig struct {
	DataDir  string `mapstructure:"data_dir"`
	CLIPath  string `mapstructure:"cli_path"`
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// BackupConfig 备份文件配置
type BackupConfig struct {
	BaseURL              string `mapstructure:"base_url"`
	DownloadDir          string `mapstructure:"download_dir"`
	AutoDetectTimestamp  bool   `mapstructure:"auto_detect_timestamp"`
	TimestampPattern     string `mapstructure:"timestamp_pattern"`
}

// ImportConfig 导入配置
type ImportConfig struct {
	Concurrency int    `mapstructure:"concurrency"`
	BatchSize   int    `mapstructure:"batch_size"`
	RetryCount  int    `mapstructure:"retry_count"`
	BatchDelay  int    `mapstructure:"batch_delay"`
	BatchPause  bool   `mapstructure:"batch_pause"`
}

// NotificationConfig 通知配置
type NotificationConfig struct {
	Wechat      WechatConfig `mapstructure:"wechat"`
	Environment string       `mapstructure:"environment"`
	Enabled     bool         `mapstructure:"enabled"`
}

// WechatConfig 企微通知配置
type WechatConfig struct {
	WebhookURL string `mapstructure:"webhook_url"`
	Enabled    bool   `mapstructure:"enabled"`
}

// LogConfig 日志配置
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	Output string `mapstructure:"output"`
}

// ImportStats 导入统计
type ImportStats struct {
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	TotalFiles   int
	SuccessCount int
	FailedCount  int
	BackupFile   string
	Timestamp    string
}

// Validate 验证配置
func (c *Config) Validate() error {
	// TODO: 实现配置验证逻辑
	return nil
}

// SetDefaults 设置默认值
func (c *Config) SetDefaults() {
	if c.Import.Concurrency <= 0 {
		c.Import.Concurrency = 1
	}
	if c.Import.BatchSize <= 0 {
		c.Import.BatchSize = 3
	}
	if c.Import.RetryCount <= 0 {
		c.Import.RetryCount = 3
	}
	if c.Import.BatchDelay <= 0 {
		c.Import.BatchDelay = 3
	}
	if c.IoTDB.Host == "" {
		c.IoTDB.Host = "iotdb-datanode"
	}
	if c.IoTDB.CLIPath == "" {
		c.IoTDB.CLIPath = "/iotdb/sbin/start-cli.sh"
	}
	if c.Backup.DownloadDir == "" {
		c.Backup.DownloadDir = "/tmp"
	}
}
