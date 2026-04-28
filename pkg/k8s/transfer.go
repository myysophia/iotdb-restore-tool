package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
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
	return t.getContainerNameForPod(ctx, t.namespace, t.podName)
}

func (t *Transfer) getContainerNameForPod(ctx context.Context, namespace, podName string) (string, error) {
	if namespace == t.namespace && podName == t.podName && t.container != "" {
		return t.container, nil
	}

	pod, err := t.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("获取 Pod 信息失败: %w", err)
	}

	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("Pod 中没有容器")
	}

	containerName := pod.Spec.Containers[0].Name
	if namespace == t.namespace && podName == t.podName {
		t.container = containerName
	}
	return containerName, nil
}

// CopyDirectoryAsArchiveFromPod 流式将源 Pod 中的目录打包写入目标 Pod 的归档文件。
func (t *Transfer) CopyDirectoryAsArchiveFromPod(ctx context.Context, sourceNamespace, sourcePod, sourceBaseDir string, sourcePaths []string, targetArchivePath string) error {
	if len(sourcePaths) == 0 {
		return fmt.Errorf("sourcePaths 不能为空")
	}

	logger.Info("开始从源 Pod 拉取目录归档到目标 Pod",
		zap.String("source_namespace", sourceNamespace),
		zap.String("source_pod", sourcePod),
		zap.String("target_namespace", t.namespace),
		zap.String("target_pod", t.podName),
		zap.String("source_base_dir", sourceBaseDir),
		zap.Strings("source_paths", sourcePaths),
		zap.String("target_archive_path", targetArchivePath),
	)

	quotedPaths := make([]string, 0, len(sourcePaths))
	for _, item := range sourcePaths {
		quotedPaths = append(quotedPaths, shellQuote(item))
	}

	sourceCmd := []string{
		"sh", "-c",
		fmt.Sprintf("cd %s && tar -cf - %s", shellQuote(sourceBaseDir), strings.Join(quotedPaths, " ")),
	}
	targetCmd := []string{
		"sh", "-c",
		fmt.Sprintf("mkdir -p %s && cat > %s", shellQuote(path.Dir(targetArchivePath)), shellQuote(targetArchivePath)),
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pipeReader, pipeWriter := io.Pipe()
	var sourceStderr bytes.Buffer
	var targetStderr bytes.Buffer
	var wg sync.WaitGroup

	var sourceErr error
	var targetErr error

	wg.Add(2)

	go func() {
		defer wg.Done()
		defer pipeWriter.Close()

		sourceErr = t.execPodCommand(streamCtx, sourceNamespace, sourcePod, sourceCmd, nil, pipeWriter, &sourceStderr)
		if sourceErr != nil {
			_ = pipeWriter.CloseWithError(sourceErr)
			cancel()
		}
	}()

	go func() {
		defer wg.Done()
		defer pipeReader.Close()

		targetErr = t.execPodCommand(streamCtx, t.namespace, t.podName, targetCmd, pipeReader, nil, &targetStderr)
		if targetErr != nil {
			_ = pipeReader.CloseWithError(targetErr)
			cancel()
		}
	}()

	wg.Wait()

	if sourceErr != nil {
		return fmt.Errorf("源 Pod 打包失败: %w: %s", sourceErr, strings.TrimSpace(sourceStderr.String()))
	}
	if targetErr != nil {
		return fmt.Errorf("目标 Pod 写入归档失败: %w: %s", targetErr, strings.TrimSpace(targetStderr.String()))
	}

	logger.Info("源 Pod 目录归档传输完成",
		zap.String("source_namespace", sourceNamespace),
		zap.String("source_pod", sourcePod),
		zap.String("target_namespace", t.namespace),
		zap.String("target_pod", t.podName),
		zap.String("target_archive_path", targetArchivePath),
	)

	return nil
}

// CopyDirectoryFromPod 流式复制源 Pod 中的目录到目标 Pod 目录。
func (t *Transfer) CopyDirectoryFromPod(ctx context.Context, sourceNamespace, sourcePod, sourceBaseDir string, sourcePaths []string, targetDir string) error {
	if len(sourcePaths) == 0 {
		return fmt.Errorf("sourcePaths 不能为空")
	}

	logger.Info("开始从源 Pod 拉取目录到目标 Pod",
		zap.String("source_namespace", sourceNamespace),
		zap.String("source_pod", sourcePod),
		zap.String("target_namespace", t.namespace),
		zap.String("target_pod", t.podName),
		zap.String("source_base_dir", sourceBaseDir),
		zap.Strings("source_paths", sourcePaths),
		zap.String("target_dir", targetDir),
	)

	quotedPaths := make([]string, 0, len(sourcePaths))
	for _, path := range sourcePaths {
		quotedPaths = append(quotedPaths, shellQuote(path))
	}

	sourceCmd := []string{
		"sh", "-c",
		fmt.Sprintf("cd %s && tar -cf - %s", shellQuote(sourceBaseDir), strings.Join(quotedPaths, " ")),
	}
	targetCmd := []string{
		"sh", "-c",
		fmt.Sprintf("mkdir -p %s && tar -xf - -C %s", shellQuote(targetDir), shellQuote(targetDir)),
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pipeReader, pipeWriter := io.Pipe()
	var sourceStderr bytes.Buffer
	var targetStderr bytes.Buffer
	var wg sync.WaitGroup

	var sourceErr error
	var targetErr error

	wg.Add(2)

	go func() {
		defer wg.Done()
		defer pipeWriter.Close()

		sourceErr = t.execPodCommand(streamCtx, sourceNamespace, sourcePod, sourceCmd, nil, pipeWriter, &sourceStderr)
		if sourceErr != nil {
			_ = pipeWriter.CloseWithError(sourceErr)
			cancel()
		}
	}()

	go func() {
		defer wg.Done()
		defer pipeReader.Close()

		targetErr = t.execPodCommand(streamCtx, t.namespace, t.podName, targetCmd, pipeReader, nil, &targetStderr)
		if targetErr != nil {
			_ = pipeReader.CloseWithError(targetErr)
			cancel()
		}
	}()

	wg.Wait()

	if sourceErr != nil {
		return fmt.Errorf("源 Pod 打包失败: %w: %s", sourceErr, strings.TrimSpace(sourceStderr.String()))
	}
	if targetErr != nil {
		return fmt.Errorf("目标 Pod 解包失败: %w: %s", targetErr, strings.TrimSpace(targetStderr.String()))
	}

	logger.Info("源 Pod 目录传输完成",
		zap.String("source_namespace", sourceNamespace),
		zap.String("source_pod", sourcePod),
		zap.String("target_namespace", t.namespace),
		zap.String("target_pod", t.podName),
	)

	return nil
}

func (t *Transfer) execPodCommand(ctx context.Context, namespace, podName string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	containerName, err := t.getContainerNameForPod(ctx, namespace, podName)
	if err != nil {
		return err
	}

	req := t.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		Param("container", containerName).
		Param("command", command[0]).
		Param("tty", "false")

	for _, cmd := range command[1:] {
		req.Param("command", cmd)
	}

	if stdin != nil {
		req.Param("stdin", "true")
	}
	if stdout != nil {
		req.Param("stdout", "true")
	}
	if stderr != nil {
		req.Param("stderr", "true")
	}

	executor, err := remotecommand.NewSPDYExecutor(t.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("创建执行器失败: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	err = executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil && execCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("命令执行超时: %w", err)
	}
	if err != nil {
		return fmt.Errorf("命令执行失败: %w", err)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
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
		speed := float64(tr.readBytes) / elapsed / 1024 / 1024                  // MB/s
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
