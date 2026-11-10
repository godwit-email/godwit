package core

import (
	"os"

	"golang.org/x/sys/unix"
)

// 不降级为普通 Rename：目标目录即使为空，也不能被创建操作替换。
func publishSkillDir(root *os.Root, from, to string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	fd := int(dir.Fd())
	return unix.Renameat2(fd, from, fd, to, unix.RENAME_NOREPLACE)
}
