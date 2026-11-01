//go:build windows

package aicontract

import (
	"syscall"
	"unsafe"
)

func deepSeekUserEnvironmentKey() string {
	path, _ := syscall.UTF16PtrFromString(`Environment`)
	name, _ := syscall.UTF16PtrFromString("DEEPSEEK_API_KEY")
	var key syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, path, 0, syscall.KEY_READ, &key) != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)
	var valueType uint32
	var size uint32
	if syscall.RegQueryValueEx(key, name, nil, &valueType, nil, &size) != nil || size < 2 {
		return ""
	}
	buffer := make([]uint16, size/2)
	if syscall.RegQueryValueEx(key, name, nil, &valueType, (*byte)(unsafe.Pointer(&buffer[0])), &size) != nil {
		return ""
	}
	return syscall.UTF16ToString(buffer)
}
