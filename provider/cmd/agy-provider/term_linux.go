package main

import (
	"os"
	"syscall"
	"unsafe"
)

// echoOff turns off terminal echo for reading a secret, returning the function
// that puts it back. It talks to the terminal directly rather than pulling in a
// dependency for it; there is no cgo here and none is needed.
func echoOff() (func(), error) {
	fd := int(os.Stdin.Fd())
	var original syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, &original); err != nil {
		return nil, err
	}
	quiet := original
	quiet.Lflag &^= syscall.ECHO
	quiet.Lflag |= syscall.ICANON | syscall.ISIG
	if err := ioctl(fd, syscall.TCSETS, &quiet); err != nil {
		return nil, err
	}
	return func() { _ = ioctl(fd, syscall.TCSETS, &original) }, nil
}

func ioctl(fd int, request uint, termios *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(request), uintptr(unsafe.Pointer(termios)))
	if errno != 0 {
		return errno
	}
	return nil
}
