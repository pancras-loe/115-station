//go:build linux

package api

import "syscall"

// execBinary 用新程序替换当前进程（pid 不变，容器不会以为主进程退出了）
func execBinary(bin string, args, env []string) error {
	return syscall.Exec(bin, args, env)
}
