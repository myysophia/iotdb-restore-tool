package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodChecker Pod 检查器接口
type PodChecker struct {
	clientset *kubernetes.Clientset
	namespace string
}

// NewPodChecker 创建 Pod 检查器
func NewPodChecker(clientset *kubernetes.Clientset, namespace string) *PodChecker {
	return &PodChecker{
		clientset: clientset,
		namespace: namespace,
	}
}

// Exists 检查 Pod 是否存在
func (p *PodChecker) Exists(ctx context.Context, podName string) (bool, error) {
	_, err := p.clientset.CoreV1().Pods(p.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("获取 Pod 失败: %w", err)
	}
	return true, nil
}

// GetStatus 获取 Pod 状态
func (p *PodChecker) GetStatus(ctx context.Context, podName string) (*corev1.PodPhase, error) {
	pod, err := p.clientset.CoreV1().Pods(p.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("获取 Pod 状态失败: %w", err)
	}
	return &pod.Status.Phase, nil
}

// GetPod 获取完整 Pod 信息
func (p *PodChecker) GetPod(ctx context.Context, podName string) (*corev1.Pod, error) {
	pod, err := p.clientset.CoreV1().Pods(p.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("获取 Pod 信息失败: %w", err)
	}
	return pod, nil
}

// IsRunning 检查 Pod 是否处于运行状态
func (p *PodChecker) IsRunning(ctx context.Context, podName string) (bool, error) {
	phase, err := p.GetStatus(ctx, podName)
	if err != nil {
		return false, err
	}
	return *phase == corev1.PodRunning, nil
}

// GetPodInfo 获取 Pod 的详细信息（用于日志）
func (p *PodChecker) GetPodInfo(ctx context.Context, podName string) (map[string]interface{}, error) {
	pod, err := p.GetPod(ctx, podName)
	if err != nil {
		return nil, err
	}

	info := map[string]interface{}{
		"name":      pod.Name,
		"namespace": pod.Namespace,
		"phase":     pod.Status.Phase,
		"node":      pod.Spec.NodeName,
		"created":   pod.CreationTimestamp,
	}

	// 获取容器信息
	containers := make([]map[string]string, len(pod.Spec.Containers))
	for i, container := range pod.Spec.Containers {
		containers[i] = map[string]string{
			"name":  container.Name,
			"image": container.Image,
		}
	}
	info["containers"] = containers

	return info, nil
}
