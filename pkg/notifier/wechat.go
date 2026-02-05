package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/restorer"
	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
)

// Notifier 通知器接口
type Notifier interface {
	Send(ctx context.Context, result *restorer.RestoreResult) error
}

// WechatNotifier 企微通知器
type WechatNotifier struct {
	webhookURL string
	httpClient *http.Client
	config     *config.NotificationConfig
}

// NewWechatNotifier 创建企微通知器
func NewWechatNotifier(cfg *config.NotificationConfig) *WechatNotifier {
	return &WechatNotifier{
		webhookURL: cfg.Wechat.WebhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		config: cfg,
	}
}

// Send 发送通知
func (w *WechatNotifier) Send(ctx context.Context, result *restorer.RestoreResult) error {
	if !w.config.Wechat.Enabled {
		logger.Info("企微通知未启用")
		return nil
	}

	message := BuildMessage(result, w.config.Environment)

	logger.Info("发送企微通知",
		zap.String("webhook", w.webhookURL),
		zap.Int("length", len(message)),
	)

	// 构建请求数据
	reqData := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": message,
		},
	}

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	// 发送请求
	req, err := http.NewRequestWithContext(ctx, "POST", w.webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("服务器返回错误状态码: %d", resp.StatusCode)
	}

	logger.Info("企微通知发送成功")
	return nil
}
