package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vnnox/iotdb-restore-tool/pkg/logger"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Transfer 文件传输器
type Transfer struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
	namespace  string
	podName    string
	container  string
}

// NewTransfer 创建文件传输器
func NewTransfer(clientset *kubernetes.Clientset, restConfig *rest.Config, namespace, podName string) *Transfer {
	return &Transfer{
		clientset:  clientset,
		restConfig: restConfig,
		namespace:  namespace,
		podName:    podName,
	}
}

// getContainerName 获取容器名称（优先使用第一个容器）
func (t *Transfer) getContainerName(ctx context.Context) (string, error) {
	if t.container != "" {
		return t.container, nil
	}

	pod, err := t.clientset.CoreV1().Pods(t.namespace).Get(ctx, t.podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("获取 Pod 信息失败: %w", err)
	}

	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("Pod 中没有容器")
	}

	// 使用第一个容器
	t.container = pod.Spec.Containers[0].Name
	return t.container, nil
}

// CopyFile 复制文件到 Pod
func (t *Transfer) CopyFile(ctx context.Context, localPath, remotePath string) error {
	// 1. 打开本地文件
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer file.Close()

	// 2. 获取文件信息
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("获取文件信息失败: %w", err)
	}

	logger.Info("开始传输文件到 Pod",
		zap.String("local", localPath),
		zap.String("remote", remotePath),
		zap.Int64("size", fileInfo.Size()),
	)

	// 3. 创建进度读取器
	progressReader := &transferReader{
		reader:    file,
		totalSize: fileInfo.Size(),
		fileName:  localPath,
		startTime: time.Now(),
	}

	// 4. 流式传输文件到 Pod
	if err := t.streamFileToPod(ctx, progressReader, remotePath); err != nil {
		return fmt.Errorf("文件传输失败: %w", err)
	}

	progressReader.Finish()
	return nil
}

// streamFileToPod 流式传输文件到 Pod
func (t *Transfer) streamFileToPod(ctx context.Context, reader *transferReader, remotePath string) error {
	// 获取容器名称
	containerName, err := t.getContainerName(ctx)
	if err != nil {
		return err
	}

	// 创建执行请求：使用 cat 命令接收 stdin 数据
	req := t.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(t.namespace).
		Name(t.podName).
		SubResource("exec").
		Param("container", containerName).
		Param("command", "sh").
		Param("command", "-c").
		Param("command", fmt.Sprintf("cat > '%s'", remotePath)).
		Param("stdin", "true").
		Param("stdout", "false").
		Param("stderr", "true").
		Param("tty", "false")

	// 创建 SPDY 执行器
	executor, err := remotecommand.NewSPDYExecutor(t.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("创建执行器失败: %w", err)
	}

	// 设置超时上下文（30分钟）
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// 创建 stderr 捕获
	var stderr bytes.Buffer

	// 流式传输
	err = executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdin:  reader,
		Stdout: nil,
		Stderr: &stderr,
	})

	if err != nil && execCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("文件传输超时: %w", err)
	}

	if err != nil {
		return fmt.Errorf("文件传输失败: %w: %s", err, stderr.String())
	}

	return nil
}

// transferReader 传输进度读取器
type transferReader struct {
	reader     io.Reader
	totalSize  int64
	readBytes  int64
	fileName   string
	startTime  time.Time
	lastLog    time.Time
	lastLogged int64
}

// Read 实现 io.Reader 接口，并记录进度
func (tr *transferReader) Read(p []byte) (int, error) {
	n, err := tr.reader.Read(p)
	if err != nil {
		return n, err
	}

	tr.readBytes += int64(n)

	// 每 5 秒或每 10% 打印一次进度
	now := time.Now()
	percentComplete := float64(tr.readBytes) / float64(tr.totalSize) * 100

	shouldLog := now.Sub(tr.lastLog) > 5*time.Second ||
		(tr.totalSize > 0 && (tr.readBytes-tr.lastLogged) > (tr.totalSize/10))

	if shouldLog && tr.totalSize > 0 {
		elapsed := time.Since(tr.startTime).Seconds()
		speed := float64(tr.readBytes) / elapsed / 1024 / 1024 // MB/s
		remaining := float64(tr.totalSize-tr.readBytes) / (speed * 1024 * 1024) // 秒

		logger.Info("传输进度",
			zap.String("file", tr.fileName),
			zap.Float64("percent", percentComplete),
			zap.String("transferred", formatBytes(tr.readBytes)),
			zap.String("total", formatBytes(tr.totalSize)),
			zap.Float64("speed_mb_s", speed),
			zap.Duration("eta", time.Duration(remaining)*time.Second),
		)

		tr.lastLog = now
		tr.lastLogged = tr.readBytes
	}

	return n, nil
}

// Finish 完成传输并打印最终统计
func (tr *transferReader) Finish() {
	duration := time.Since(tr.startTime)
	avgSpeed := float64(tr.readBytes) / duration.Seconds() / 1024 / 1024

	logger.Info("传输完成",
		zap.String("file", tr.fileName),
		zap.String("size", formatBytes(tr.readBytes)),
		zap.Duration("duration", duration),
		zap.Float64("avg_speed_mb_s", avgSpeed),
	)
}

// formatBytes 格式化字节数
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
