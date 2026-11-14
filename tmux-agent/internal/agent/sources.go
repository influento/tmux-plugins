package agent

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/influento/tmux-plugins/internal/lockfile"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// The watcher's event sources: inotify for files, pidfds for processes,
// a datagram socket for the hooks. Each turns what it sees into a value
// on a channel, so the watcher waits in one select and never polls.

// fileMask is what a watched file or folder reports: writes, and entries
// that come, go or are renamed into place.
const fileMask = syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE | syscall.IN_CREATE | syscall.IN_MOVED_TO |
	syscall.IN_DELETE | syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF

// fileWatch reports changes to a set of files and folders. A folder's
// entries pass a filter of its own.
type fileWatch struct {
	fd      int
	f       *os.File
	mu      sync.Mutex
	byPath  map[string]int // path → watch descriptor
	byWd    map[int]watched
	changes chan string // the path whose watch fired
}

type watched struct {
	path string
	keep func(name string) bool // nil: every entry
}

func newFileWatch() (*fileWatch, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &fileWatch{
		fd: fd, f: os.NewFile(uintptr(fd), "inotify"),
		byPath: map[string]int{}, byWd: map[int]watched{}, changes: make(chan string, 64),
	}
	go w.read()
	return w, nil
}

// set makes the watched paths exactly those given, with their filters. A
// path that does not exist yet is left out; a later set adds it.
func (w *fileWatch) set(paths map[string]func(string) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for path, wd := range w.byPath {
		if _, ok := paths[path]; !ok {
			_, _ = syscall.InotifyRmWatch(w.fd, uint32(wd)) // gone already: nothing to remove
			delete(w.byPath, path)
			delete(w.byWd, wd)
		}
	}
	for path, keep := range paths {
		if path == "" {
			continue
		}
		wd, err := syscall.InotifyAddWatch(w.fd, path, fileMask)
		if err != nil {
			continue // not there yet
		}
		w.byPath[path] = wd
		w.byWd[wd] = watched{path: path, keep: keep}
	}
}

// read sends the path of each watch that fires and passes its filter,
// until the watch closes.
func (w *fileWatch) read() {
	buf := make([]byte, 64<<10)
	for {
		n, err := w.f.Read(buf)
		if err != nil {
			return // closed
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := strings.TrimRight(string(buf[off+syscall.SizeofInotifyEvent:off+syscall.SizeofInotifyEvent+int(ev.Len)]), "\x00")
			off += syscall.SizeofInotifyEvent + int(ev.Len)
			w.mu.Lock()
			wt, ok := w.byWd[int(ev.Wd)]
			if ok && ev.Mask&syscall.IN_IGNORED != 0 {
				// The file went: a later set watches what takes its place.
				delete(w.byWd, int(ev.Wd))
				delete(w.byPath, wt.path)
			}
			w.mu.Unlock()
			if !ok || (name != "" && wt.keep != nil && !wt.keep(name)) {
				continue
			}
			select {
			case w.changes <- wt.path:
			default: // the watcher has changes to look at already
			}
		}
	}
}

func (w *fileWatch) close() { _ = w.f.Close() }

// exitWatch reports when processes end, through pidfds: a pidfd turns
// readable when its process exits.
type exitWatch struct {
	mu     sync.Mutex
	files  map[int]*os.File
	exited chan int
}

func newExitWatch() *exitWatch {
	return &exitWatch{files: map[int]*os.File{}, exited: make(chan int, 16)}
}

// sysPidfdOpen is pidfd_open(2), the same number on amd64 and arm64.
const sysPidfdOpen = 434

// set makes the watched processes exactly pids. One already gone is
// reported at once.
func (x *exitWatch) set(pids []int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	want := map[int]bool{}
	for _, pid := range pids {
		want[pid] = true
	}
	for pid, f := range x.files {
		if !want[pid] {
			_ = f.Close() // ends its wait
			delete(x.files, pid)
		}
	}
	for pid := range want {
		if _, ok := x.files[pid]; ok || pid <= 0 {
			continue
		}
		fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
		if errno != 0 {
			x.report(pid) // gone already
			continue
		}
		if err := syscall.SetNonblock(int(fd), true); err != nil {
			_ = syscall.Close(int(fd))
			continue
		}
		f := os.NewFile(fd, "pidfd")
		x.files[pid] = f
		go x.wait(pid, f)
	}
}

// wait reports pid once its pidfd turns readable; a close ends the wait
// without a report.
func (x *exitWatch) wait(pid int, f *os.File) {
	rc, err := f.SyscallConn()
	if err != nil {
		return
	}
	// The poller is edge-triggered and forgets a readiness that came
	// before the wait, so each try asks the pidfd itself.
	if err := rc.Read(readable); err == nil {
		x.report(pid)
	}
}

// readable reports whether fd can be read now, without waiting.
func readable(fd uintptr) bool {
	p := struct {
		fd              int32
		events, revents int16
	}{fd: int32(fd), events: pollIn}
	var now syscall.Timespec
	n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&p)), 1, uintptr(unsafe.Pointer(&now)), 0, 0, 0)
	return errno == 0 && n == 1 && p.revents&pollIn != 0
}

const pollIn = 0x1 // POLLIN

func (x *exitWatch) report(pid int) {
	select {
	case x.exited <- pid:
	default: // the watcher looks at every process again anyway
	}
}

func (x *exitWatch) close() { x.set(nil) }

// Wakes are what a hook tells a pane's watcher, one word per datagram.
const (
	wakePing = "ping" // only: is a watcher there?
	wakeScan = "scan" // something the watcher follows may have changed
)

// watchSocket is where the watcher of pane on the tmux server in $TMUX
// listens; "" outside tmux. The server's pid keeps two servers' panes
// apart.
func watchSocket(getenv func(string) string, pane string) string {
	server := strings.Split(getenv("TMUX"), ",")
	if pane == "" || len(server) < 2 || server[1] == "" {
		return ""
	}
	return filepath.Join(store.RuntimeDir(getenv), "watch-"+server[1]+"-"+strings.TrimPrefix(pane, "%")+".sock")
}

// wake sends word to the watcher of pane and reports whether one took it.
func (e *Env) wake(pane, word string) bool {
	path := watchSocket(e.getenv, pane)
	if path == "" {
		return false
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return false
	}
	defer c.Close()
	_, err = c.Write([]byte(word))
	return err == nil
}

// ErrWatched is a pane that has a live watcher already.
var ErrWatched = errors.New("the pane has a watcher")

// listenWakes takes the pane's watch socket for as long as the watcher
// lives: a lock next to it makes the watcher the pane's only one
// (ErrWatched otherwise), and a socket left by one that is gone is
// replaced. close removes the socket and lets the lock go.
func listenWakes(path string) (conn *net.UnixConn, close func(), err error) {
	unlock, ok, err := lockfile.TryLock(path + ".lock")
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, ErrWatched
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		unlock()
		return nil, nil, err
	}
	conn, err = net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return conn, func() {
		_ = conn.Close()
		_ = os.Remove(path)
		unlock()
	}, nil
}
