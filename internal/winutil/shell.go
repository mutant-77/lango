package winutil

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

const swShowNormal = 1

// ShellExecute executes file in dir using ShellExecuteW.
func ShellExecute(file, dir string) error {
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var dirPtr uintptr
	if dir != "" {
		p, err := windows.UTF16PtrFromString(dir)
		if err != nil {
			return err
		}
		dirPtr = uintptr(unsafe.Pointer(p))
	}
	ret, _, _ := procShellExecuteW.Call(
		0,
		0,
		uintptr(unsafe.Pointer(filePtr)),
		0,
		dirPtr,
		swShowNormal,
	)
	if ret <= 32 {
		return fmt.Errorf("ShellExecute failed with code %d", ret)
	}
	return nil
}
