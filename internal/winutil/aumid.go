package winutil

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32                                    = windows.NewLazySystemDLL("shell32.dll")
	procSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
)

// SetAUMID sets the Application User Model ID for the current process
// by calling shell32!SetCurrentProcessExplicitAppUserModelID.
func SetAUMID(id string) error {
	utf16Ptr, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return err
	}
	r1, _, err := procSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(utf16Ptr)))
	if r1 != 0 {
		if err != nil && err != windows.ERROR_SUCCESS {
			return err
		}
		return windows.Errno(r1)
	}
	return nil
}
