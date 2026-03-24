package notifier

import (
	"fmt"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/restorer"
)

// MessageTemplate 消息模板
type MessageTemplate struct {
	Title       string
	Environment string
	Pod         string
	BackupFile  string
	Statistics  Statistics
}

// Statistics 统计信息
type Statistics struct {
	StartTime    string
	EndTime      string
	Duration     string
	TotalFiles   int
	SuccessCount int
	FailedCount  int
}

// BuildMessage 构建消息
func BuildMessage(result *restorer.RestoreResult, environment string) string {
	duration := formatDuration(result.Duration)
	status := "✅"
	if result.Error != nil {
		status = "❌"
	}

	message := fmt.Sprintf("## IoTDB 数据恢复通知\n\n")
	message += fmt.Sprintf("%s **环境**: %s\n", status, environment)
	message += fmt.Sprintf("> **备份文件**: `%s`\n\n", result.BackupFile)

	message += "---\n\n"
	message += "### 📊 恢复统计\n\n"
	message += "| 项目 | 详情 |\n"
	message += "|------|------|\n"
	message += fmt.Sprintf("| **开始时间** | %s |\n", result.StartTime.Format("2006-01-02 15:04:05"))
	message += fmt.Sprintf("| **结束时间** | %s |\n", result.EndTime.Format("2006-01-02 15:04:05"))
	message += fmt.Sprintf("| **执行时长** | %s |\n", duration)
	message += fmt.Sprintf("| **总文件数** | %d 个 |\n", result.TotalFiles)
	message += fmt.Sprintf("| **成功导入** | %d 个 |\n", result.SuccessCount)
	message += fmt.Sprintf("| **失败数量** | %d 个 |\n", result.FailedCount)

	message += "\n---\n\n"

	if result.Probe != nil && result.Probe.Executed {
		message += "### 🩺 数据库自检\n\n"
		message += "| 项目 | 详情 |\n"
		message += "|------|------|\n"
		message += fmt.Sprintf("| **数据库** | %s |\n", result.Probe.Database)
		message += fmt.Sprintf("| **探测序列** | `%s` |\n", result.Probe.SeriesPath)
		message += fmt.Sprintf("| **写入时间戳** | %d |\n", result.Probe.Timestamp)
		message += fmt.Sprintf("| **写入值** | %d |\n", result.Probe.Value)
		if result.Probe.QueryResult != "" {
			message += fmt.Sprintf("| **查询结果** | %s |\n", result.Probe.QueryResult)
		}
		if result.Probe.Error != "" {
			message += fmt.Sprintf("| **自检状态** | 失败 |\n")
			message += fmt.Sprintf("| **自检错误** | %s |\n", result.Probe.Error)
		} else {
			message += fmt.Sprintf("| **自检状态** | 成功 |\n")
		}
		message += "\n---\n\n"
	}

	if result.Error != nil {
		message += "### ❌ 恢复失败\n\n"
		message += fmt.Sprintf("错误信息: %s\n", result.Error.Error())
	} else {
		message += "### ✅ 恢复操作已完成\n\n"
	}

	message += fmt.Sprintf("系统时间: %s", time.Now().Format("2006-01-02 15:04:05"))

	return message
}

// formatDuration 格式化时长
func formatDuration(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%d小时%d分%d秒", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%d分%d秒", minutes, seconds)
	} else {
		return fmt.Sprintf("%d秒", seconds)
	}
}

// BuildSuccessMessage 构建成功消息
func BuildSuccessMessage(result *restorer.RestoreResult, environment string) string {
	return BuildMessage(result, environment)
}

// BuildErrorMessage 构建错误消息
func BuildErrorMessage(result *restorer.RestoreResult, environment string, err error) string {
	result.Error = err
	return BuildMessage(result, environment)
}
