//go:build !linux

package api

import "errors"

// execBinary 一键更新只在 Linux 容器里做（applyBlock 先拦着），别的系统走不到这里
func execBinary(bin string, args, env []string) error {
	return errors.New("只支持 Linux")
}
