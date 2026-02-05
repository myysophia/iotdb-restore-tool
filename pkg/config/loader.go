package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Load 从文件加载配置
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// 设置配置文件
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs")
		v.AddConfigPath("/etc/iotdb-restore")
		v.AddConfigPath(".")
	}

	// 环境变量前缀
	v.SetEnvPrefix("IOTDB_RESTORE")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// 读取配置文件
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
	}

	// 解析配置
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	// 设置默认值
	cfg.SetDefaults()

	// 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}

	return &cfg, nil
}

// LoadWithOverrides 从文件加载配置并使用命令行参数覆盖
func LoadWithOverrides(configPath string, overrides map[string]interface{}) (*Config, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return nil, err
	}

	// 应用覆盖
	if namespace, ok := overrides["namespace"].(string); ok && namespace != "" {
		cfg.Kubernetes.Namespace = namespace
	}
	if podName, ok := overrides["pod_name"].(string); ok && podName != "" {
		cfg.Kubernetes.PodName = podName
	}
	if concurrency, ok := overrides["concurrency"].(int); ok && concurrency > 0 {
		cfg.Import.Concurrency = concurrency
	}
	if batchSize, ok := overrides["batch_size"].(int); ok && batchSize > 0 {
		cfg.Import.BatchSize = batchSize
	}
	if timestamp, ok := overrides["timestamp"].(string); ok && timestamp != "" {
		// 时间戳会在后续处理
		_ = timestamp
	}

	return cfg, nil
}
