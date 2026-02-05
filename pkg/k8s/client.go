package k8s

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// expandPath 扩展路径中的 ~ 为用户主目录
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		usr, err := user.Current()
		if err == nil {
			if path == "~" {
				return usr.HomeDir
			}
			return filepath.Join(usr.HomeDir, path[2:])
		}
		// 如果获取用户信息失败，尝试使用 $HOME
		home := os.Getenv("HOME")
		if home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// NewClient 创建 Kubernetes 客户端
// 支持多种认证方式：
// 1. in-cluster 配置（在 Pod 中运行时）
// 2. kubeconfig 文件（本地开发）
// 3. 指定的 kubeconfig 文件路径
func NewClient(kubeconfigPath string) (*kubernetes.Clientset, error) {
	var config *rest.Config
	var err error

	// 扩展路径中的 ~
	kubeconfigPath = expandPath(kubeconfigPath)

	// 如果指定了 kubeconfig 路径，使用它
	if kubeconfigPath != "" && kubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("从 kubeconfig 文件构建配置失败: %w", err)
		}
	} else {
		// 尝试 in-cluster 配置
		config, err = rest.InClusterConfig()
		if err != nil {
			// 如果 in-cluster 失败，尝试默认 kubeconfig
			kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
			if _, err := os.Stat(kubeconfig); err == nil {
				config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
				if err != nil {
					return nil, fmt.Errorf("从默认 kubeconfig 构建配置失败: %w", err)
				}
			} else {
				return nil, fmt.Errorf("无法加载 in-cluster 配置，且 kubeconfig 文件不存在: %w", err)
			}
		}
	}

	// 创建 clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("创建 clientset 失败: %w", err)
	}

	return clientset, nil
}

// NewConfig 创建 REST 配置
func NewConfig(kubeconfigPath string) (*rest.Config, error) {
	var config *rest.Config
	var err error

	// 扩展路径中的 ~
	kubeconfigPath = expandPath(kubeconfigPath)

	// 如果指定了 kubeconfig 路径，使用它
	if kubeconfigPath != "" && kubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("从 kubeconfig 文件构建配置失败: %w", err)
		}
	} else {
		// 尝试 in-cluster 配置
		config, err = rest.InClusterConfig()
		if err != nil {
			// 如果 in-cluster 失败，尝试默认 kubeconfig
			kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
			if _, err := os.Stat(kubeconfig); err == nil {
				config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
				if err != nil {
					return nil, fmt.Errorf("从默认 kubeconfig 构建配置失败: %w", err)
				}
			} else {
				return nil, fmt.Errorf("无法加载 in-cluster 配置，且 kubeconfig 文件不存在: %w", err)
			}
		}
	}

	return config, nil
}

// TestConnection 测试与 Kubernetes API 的连接
func TestConnection(ctx context.Context, clientset *kubernetes.Clientset) error {
	_, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("连接 Kubernetes API 失败: %w", err)
	}
	return nil
}
