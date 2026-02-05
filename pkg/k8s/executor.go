package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Executor Pod 命令执行器
type Executor struct {
	clientset    *kubernetes.Clientset
	restConfig   *rest.Config
	namespace    string
	podName      string
	container    string
	config       *ExecutorConfig
}

// ExecutorConfig 执行器配置
type ExecutorConfig struct {
	Timeout      time.Duration
	StopOnError  bool
}

// NewExecutor 创建命令执行器
func NewExecutor(clientset *kubernetes.Clientset, restConfig *rest.Config, namespace, podName string, config *ExecutorConfig) *Executor {
	if config == nil {
		config = &ExecutorConfig{
			Timeout:     30 * time.Minute,
			StopOnError: false,
		}
	}

	return &Executor{
		clientset:  clientset,
		restConfig: restConfig,
		namespace:  namespace,
		podName:    podName,
		config:     config,
	}
}

// getContainerName 获取容器名称（优先使用第一个容器）
func (e *Executor) getContainerName(ctx context.Context) (string, error) {
	if e.container != "" {
		return e.container, nil
	}

	pod, err := e.clientset.CoreV1().Pods(e.namespace).Get(ctx, e.podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("获取 Pod 信息失败: %w", err)
	}

	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("Pod 中没有容器")
	}

	// 使用第一个容器
	e.container = pod.Spec.Containers[0].Name
	return e.container, nil
}

// Exec 在 Pod 中执行命令
func (e *Executor) Exec(ctx context.Context, command []string) (string, string, error) {
	// 获取容器名称
	containerName, err := e.getContainerName(ctx)
	if err != nil {
		return "", "", err
	}

	// 创建带超时的上下文
	execCtx, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()

	// 准备执行请求
	req := e.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(e.namespace).
		Name(e.podName).
		SubResource("exec").
		Param("container", containerName).
		Param("command", command[0]).
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "false")

	// 添加剩余的命令参数
	for _, cmd := range command[1:] {
		req.Param("command", cmd)
	}

	// 创建执行器
	executor, err := remotecommand.NewSPDYExecutor(e.restConfig, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("创建执行器失败: %w", err)
	}

	// 捕获 stdout 和 stderr
	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	if err != nil && execCtx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), fmt.Errorf("命令执行超时: %w", err)
	}

	if err != nil {
		accessor, _ := meta.Accessor(err)
		if accessor != nil {
			return stdout.String(), stderr.String(), fmt.Errorf("命令执行失败: %s", accessor.GetSelfLink())
		}
		return stdout.String(), stderr.String(), fmt.Errorf("命令执行失败: %w", err)
	}

	return stdout.String(), stderr.String(), nil
}

// ExecStream 在 Pod 中执行命令（流式输出）
func (e *Executor) ExecStream(ctx context.Context, command []string, stdout, stderr io.Writer) error {
	// 获取容器名称
	containerName, err := e.getContainerName(ctx)
	if err != nil {
		return err
	}

	execCtx, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()

	req := e.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(e.namespace).
		Name(e.podName).
		SubResource("exec").
		Param("container", containerName).
		Param("command", command[0]).
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "false")

	// 添加剩余的命令参数
	for _, cmd := range command[1:] {
		req.Param("command", cmd)
	}

	executor, err := remotecommand.NewSPDYExecutor(e.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("创建执行器失败: %w", err)
	}

	err = executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
	})

	if err != nil && execCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("命令执行超时: %w", err)
	}

	return err
}

// ExecSimple 简化版命令执行（只返回输出和错误）
func (e *Executor) ExecSimple(ctx context.Context, command string) (string, error) {
	stdout, stderr, err := e.Exec(ctx, []string{"sh", "-c", command})
	if err != nil {
		return stdout, fmt.Errorf("%w: %s", err, stderr)
	}
	return stdout, nil
}

// FileExists 检查 Pod 中文件是否存在
func (e *Executor) FileExists(ctx context.Context, filePath string) (bool, error) {
	output, err := e.ExecSimple(ctx, fmt.Sprintf("[ -f '%s' ] && echo 'exists' || echo 'not exists'", filePath))
	if err != nil {
		return false, err
	}
	return output == "exists\n", nil
}

// FileSize 获取 Pod 中文件的大小
func (e *Executor) FileSize(ctx context.Context, filePath string) (int64, error) {
	output, err := e.ExecSimple(ctx, fmt.Sprintf("stat -f%%z '%s' 2>/dev/null || stat -c%%s '%s' 2>/dev/null || echo '0'", filePath, filePath))
	if err != nil {
		return 0, err
	}

	var size int64
	_, err = fmt.Sscanf(output, "%d", &size)
	return size, err
}
