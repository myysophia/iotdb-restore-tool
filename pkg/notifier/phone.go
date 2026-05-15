package notifier

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"github.com/vnnox/iotdb-restore-tool/pkg/restorer"
	"go.uber.org/zap"
)

const phoneAPIURL = "http://apiyytz.ucpaas.com/qiongqiapi/startcallbyphonelist.php"

// PhoneNotifier 电话告警通知器
type PhoneNotifier struct {
	httpClient *http.Client
	config     *config.NotificationConfig
}

// NewPhoneNotifier 创建电话告警通知器
func NewPhoneNotifier(cfg *config.NotificationConfig) *PhoneNotifier {
	return &PhoneNotifier{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		config: cfg,
	}
}

// Send 发送电话告警（仅在恢复失败时触发）
func (p *PhoneNotifier) Send(ctx context.Context, result *restorer.RestoreResult) error {
	if !p.config.Phone.Enabled {
		logger.Info("电话告警未启用")
		return nil
	}

	// 成功时不发电话告警
	if result.Error == nil {
		logger.Info("恢复成功，跳过电话告警")
		return nil
	}

	words := BuildPhoneMessage(result, p.config.Environment)
	if words == "" {
		logger.Warn("电话告警消息为空，跳过发送")
		return nil
	}

	logger.Info("发送电话告警",
		zap.String("phone_list", p.config.Phone.PhoneList),
	)

	formData := url.Values{
		"appid":     {p.config.Phone.AppID},
		"appsecret": {p.config.Phone.AppSecret},
		"appkey":    {p.config.Phone.AppKey},
		"pitch":     {"0"},
		"speech":    {"0"},
		"voice":     {p.config.Phone.Voice},
		"call_num":  {p.config.Phone.CallNum},
		"words":     {words},
		"phonelist": {p.config.Phone.PhoneList},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", phoneAPIURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("创建电话告警请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送电话告警失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("电话告警服务返回错误状态码: %d", resp.StatusCode)
	}

	logger.Info("电话告警发送成功")
	return nil
}
