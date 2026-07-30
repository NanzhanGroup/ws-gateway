//go:build !windows

package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxLockRetries = 5
const lockRetryDelay = 800 * time.Millisecond

// lockDir 返回 PID 锁目录
// 优先取 $WS_PATH/data，其次 /etc/environment，最后兜底 /data/app/ws/data
func lockDir() string {
	wsPath := os.Getenv("WS_PATH")
	if wsPath == "" {
		wsPath = readSystemEnv("WS_PATH")
	}
	if wsPath == "" {
		wsPath = "/data/app/ws"
	}
	return filepath.Join(wsPath, "data")
}

// readSystemEnv 从 /etc/environment 读取指定 key 的值
func readSystemEnv(key string) string {
	data, err := os.ReadFile("/etc/environment")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// LockPIDFile 尝试加锁 PID 文件（排他非阻塞）。
// 如果另一个实例已在运行，返回非 nil 错误。
// name 用于区分不同服务的锁文件，如 "weixin", "email", "token-cache"
//
// 安全机制：
//   - 如果锁被占用但持锁进程已死（孤儿僵尸等），清理旧锁后重试
//   - 最多重试 maxLockRetries 次，间隔 lockRetryDelay
func LockPIDFile(name string) (*os.File, error) {
	dir := lockDir()
	os.MkdirAll(dir, 0755)
	lockPath := filepath.Join(dir, name+".pid")

	for attempt := 0; attempt < maxLockRetries; attempt++ {
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return nil, fmt.Errorf("创建 PID 锁文件失败 %s: %w", lockPath, err)
		}

		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lockFile.Close()

			// 检查持锁进程是否还活着
			if pid := readPIDFromFile(lockPath); pid > 0 {
				if !isProcessAlive(pid) {
					// 持锁进程已死，清理旧锁文件后重试
					os.Remove(lockPath)
					if attempt < maxLockRetries-1 {
						time.Sleep(lockRetryDelay)
						continue
					}
				}
			}

			return nil, fmt.Errorf("%s 已在运行（PID 文件被锁定: %s）", name, lockPath)
		}

		// 加锁成功，写入当前 PID
		lockFile.Truncate(0)
		lockFile.Seek(0, 0)
		fmt.Fprintf(lockFile, "%d\n", os.Getpid())
		lockFile.Sync()

		return lockFile, nil
	}

	return nil, fmt.Errorf("%s 加锁失败（重试 %d 次后放弃）", name, maxLockRetries)
}

// UnlockPIDFile 释放 PID 文件锁并清理
func UnlockPIDFile(f *os.File, name string) {
	if f == nil {
		return
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
	lockPath := filepath.Join(lockDir(), name+".pid")
	os.Remove(lockPath)
}

// readPIDFromFile 从 PID 文件读取进程号，失败返回 0
func readPIDFromFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// isProcessAlive 检查进程是否存活（信号 0 不发送信号，只检查存在性）
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
