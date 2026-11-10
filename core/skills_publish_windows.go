package core

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 相对已打开的根目录发布，ReplaceIfExists 保持为零，不覆盖已有目录。
func publishSkillDir(root *os.Root, from, to string) error {
	// 保留 os.Root 对 NUL、CON 等 Windows 设备名称和越界路径的拒绝。
	if _, err := root.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return os.ErrExist
		}
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	oldName, err := windows.NewNTUnicodeString(from)
	if err != nil {
		return err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: oldName, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var source windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&source, windows.DELETE|windows.SYNCHRONIZE, &oa, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(source)
	name, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	var info struct {
		ReplaceIfExists uint32
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [65]uint16 // Skill 标识最多 64 个 ASCII 字符。
	}
	if len(name) > len(info.FileName) {
		return windows.ERROR_FILENAME_EXCED_RANGE
	}
	info.RootDirectory = windows.Handle(dir.Fd())
	info.FileNameLength = uint32((len(name) - 1) * 2)
	copy(info.FileName[:], name)
	size := uint32(unsafe.Sizeof(info)) // 单字符名称也必须包含完整的固定结构头和对齐空间。
	return windows.NtSetInformationFile(source, &status, (*byte)(unsafe.Pointer(&info)), size, windows.FileRenameInformation)
}
