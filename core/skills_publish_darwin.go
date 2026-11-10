package core

import (
	"os"

	"golang.org/x/sys/unix"
)

// 普通 Rename 会替换已有空目录；RENAME_EXCL 把判重和发布放在同一原子操作中。
func publishSkillDir(root *os.Root, from, to string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	fd := int(dir.Fd())
	return unix.RenameatxNp(fd, from, fd, to, unix.RENAME_EXCL)
}
