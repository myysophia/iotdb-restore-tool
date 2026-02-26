package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileLock 文件锁
type FileLock struct {
	lockFile string
	file     *os.File
	mu       sync.Mutex
}

// NewFileLock 创建文件锁
func NewFileLock(lockDir string, name string) (*FileLock, error) {
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("创建锁目录失败: %w", err)
	}

	lockFile := filepath.Join(lockDir, name+".lock")

	return &FileLock{
		lockFile: lockFile,
	}, nil
}

// TryLock 尝试获取锁（非阻塞）
func (fl *FileLock) TryLock() error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	// 尝试创建并打开文件
	file, err := os.OpenFile(fl.lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			// 锁文件已存在，检查是否过期
			if info, err := os.Stat(fl.lockFile); err == nil {
				// 如果锁文件超过 3 小时，认为是死锁
				if time.Since(info.ModTime()) > 3*time.Hour {
					os.Remove(fl.lockFile)
					// 重试
					file, err = os.OpenFile(fl.lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
					if err != nil {
						return fmt.Errorf("获取锁失败（重试后）: %w", err)
					}
				} else {
					// 锁文件未过期，返回错误
					pid := fl.readPID()
					return fmt.Errorf("任务正在运行中（PID: %s，锁文件: %s）", pid, fl.lockFile)
				}
			} else {
				return fmt.Errorf("获取锁失败: %w", err)
			}
		} else {
			return fmt.Errorf("获取锁失败: %w", err)
		}
	}

	// 写入当前 PID
	pid := os.Getpid()
	fmt.Fprintf(file, "%d\n", pid)
	fmt.Fprintf(file, "started_at: %s\n", time.Now().Format(time.RFC3339))

	fl.file = file
	return nil
}

// Lock 阻塞式获取锁
func (fl *FileLock) Lock(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := fl.TryLock(); err == nil {
				return nil
			}
		}
	}
}

// Unlock 释放锁
func (fl *FileLock) Unlock() error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	if fl.file != nil {
		fl.file.Close()
		fl.file = nil
	}

	if err := os.Remove(fl.lockFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("释放锁失败: %w", err)
	}

	return nil
}

// readPID 读取锁文件中的 PID
func (fl *FileLock) readPID() string {
	data, err := os.ReadFile(fl.lockFile)
	if err != nil {
		return "unknown"
	}

	// 第一行是 PID
	lines := string(data)
	if len(lines) > 0 {
		return lines
	}

	return "unknown"
}

// IsLocked 检查是否已锁定
func (fl *FileLock) IsLocked() bool {
	_, err := os.Stat(fl.lockFile)
	return !os.IsNotExist(err)
}
