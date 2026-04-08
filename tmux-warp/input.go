package main

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

const inputTimeout = 10 * time.Second

// termios mirrors the C struct termios for Linux.
type termios struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [32]uint8
	Ispeed uint32
	Ospeed uint32
}

const (
	tcgets = 0x5401
	tcsets = 0x5402

	icanon = 0x2
	echo   = 0x8
	isig   = 0x1

	vmin  = 6
	vtime = 5
)

func ioctlGetTermios(fd uintptr) (*termios, error) {
	t := new(termios)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, tcgets, uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return nil, errno
	}
	return t, nil
}

func ioctlSetTermios(fd uintptr, t *termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, tcsets, uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

// RawTerminal manages raw mode on /dev/tty for single-char reads.
type RawTerminal struct {
	file    *os.File
	origios termios
}

func openRawTerminal() (*RawTerminal, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

	fd := f.Fd()
	origios, err := ioctlGetTermios(fd)
	if err != nil {
		f.Close()
		return nil, err
	}

	raw := *origios
	raw.Lflag &^= icanon | echo | isig
	raw.Cc[vmin] = 1
	raw.Cc[vtime] = 0

	if err := ioctlSetTermios(fd, &raw); err != nil {
		f.Close()
		return nil, err
	}

	return &RawTerminal{file: f, origios: *origios}, nil
}

func (rt *RawTerminal) Close() error {
	_ = ioctlSetTermios(rt.file.Fd(), &rt.origios)
	return rt.file.Close()
}

// ReadChar reads a single byte with a timeout. Returns the byte and whether
// the read was successful. Returns (0, false) on timeout or error.
func (rt *RawTerminal) ReadChar() (byte, bool) {
	ch := make(chan byte, 1)
	go func() {
		buf := make([]byte, 1)
		n, err := rt.file.Read(buf)
		if err == nil && n == 1 {
			ch <- buf[0]
		}
	}()

	select {
	case b := <-ch:
		return b, true
	case <-time.After(inputTimeout):
		return 0, false
	}
}

// IsCancel returns true if the byte is Escape (0x1b) or Ctrl+C (0x03).
func IsCancel(b byte) bool {
	return b == 0x1b || b == 0x03
}
