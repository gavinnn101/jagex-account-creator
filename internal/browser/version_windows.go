//go:build windows

package browser

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readChromeVersion reads the file version from the version resource of the
// binary. A binary with no version resource returns an error.
func readChromeVersion(_ context.Context, chromePath string) (string, error) {
	size, err := windows.GetFileVersionInfoSize(chromePath, nil)
	if err != nil {
		return "", fmt.Errorf("reading version resource of %s: %w", chromePath, err)
	}
	buf := make([]byte, size)
	//nolint:gosec // G103: the Windows version API takes a raw pointer to the buffer.
	if err := windows.GetFileVersionInfo(chromePath, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return "", fmt.Errorf("reading version resource of %s: %w", chromePath, err)
	}
	var info *windows.VS_FIXEDFILEINFO
	var infoSize uint32
	//nolint:gosec // G103: the Windows version API takes raw pointers to the buffer and the result.
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&info), &infoSize); err != nil {
		return "", fmt.Errorf("reading version resource of %s: %w", chromePath, err)
	}
	if uintptr(infoSize) < unsafe.Sizeof(windows.VS_FIXEDFILEINFO{}) {
		return "", fmt.Errorf("reading version resource of %s: fixed file info is %d bytes", chromePath, infoSize)
	}
	return fmt.Sprintf("%d.%d.%d.%d",
		info.FileVersionMS>>16, info.FileVersionMS&0xffff,
		info.FileVersionLS>>16, info.FileVersionLS&0xffff), nil
}
