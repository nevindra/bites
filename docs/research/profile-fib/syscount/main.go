// syscount runs a command under ptrace and counts its system calls by number, for every thread,
// the way `strace -c -f` does (strace is not installed here). It is for counts only: tracing slows
// the server by a large factor, so no time taken under it means anything, and with many
// connections the slowdown makes each wake-up serve more requests than at full speed.
//
//	syscount -out counts.json -- taskset -c 2,3,10,11 ./server -addr 127.0.0.1:19001
//
// On SIGUSR1 it writes the counts so far to -out (by rename); on the traced command's exit, too.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
)

var names = map[uint64]string{0: "read", 1: "write", 3: "close", 9: "mmap", 14: "rt_sigprocmask", 15: "rt_sigreturn", 19: "readv", 20: "writev", 24: "sched_yield",
	28: "madvise", 35: "nanosleep", 44: "sendto", 45: "recvfrom", 46: "sendmsg", 47: "recvmsg", 54: "setsockopt", 202: "futex", 230: "clock_nanosleep",
	232: "epoll_wait", 233: "epoll_ctl", 234: "tgkill", 281: "epoll_pwait", 288: "accept4", 441: "epoll_pwait2", 228: "clock_gettime", 39: "getpid", 186: "gettid", 13: "rt_sigaction", 131: "sigaltstack", 56: "clone", 435: "clone3", 158: "arch_prctl", 273: "set_robust_list", 318: "getrandom"}

var (
	mu     sync.Mutex
	counts = map[string]int{}
	out    = flag.String("out", "counts.json", "where the counts go")
)

func dump() {
	mu.Lock()
	b, _ := json.Marshal(counts)
	mu.Unlock()
	os.WriteFile(*out+".tmp", b, 0o644)
	os.Rename(*out+".tmp", *out)
}

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: syscount -out f -- cmd args...")
		os.Exit(2)
	}
	runtime.LockOSThread() // every ptrace call must come from the thread that attached
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			dump()
		}
	}()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	// forward SIGTERM to the child so the harness can stop it
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	go func() { <-term; syscall.Kill(cmd.Process.Pid, syscall.SIGTERM) }()

	pid := cmd.Process.Pid
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
		panic(err)
	}
	const opts = syscall.PTRACE_O_TRACESYSGOOD | syscall.PTRACE_O_TRACECLONE | 0x100000 /* PTRACE_O_EXITKILL */
	if err := syscall.PtraceSetOptions(pid, opts); err != nil {
		panic(err)
	}
	inCall := map[int]bool{}
	syscall.PtraceSyscall(pid, 0)
	live := 1
	for live > 0 {
		wp, err := syscall.Wait4(-1, &ws, syscall.WALL, nil)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			break
		}
		switch {
		case ws.Exited() || ws.Signaled():
			delete(inCall, wp)
			live--
			continue
		case !ws.Stopped():
			continue
		}
		sig := ws.StopSignal()
		inject := 0
		switch {
		case sig == syscall.SIGTRAP|0x80: // syscall stop
			if !inCall[wp] {
				var regs syscall.PtraceRegs
				if syscall.PtraceGetRegs(wp, &regs) == nil {
					n := regs.Orig_rax
					name, ok := names[n]
					if !ok {
						name = fmt.Sprintf("sys_%d", n)
					}
					mu.Lock()
					counts[name]++
					mu.Unlock()
				}
			}
			inCall[wp] = !inCall[wp]
		case sig == syscall.SIGTRAP: // a ptrace event, here the clone of a new thread
			if ws.TrapCause() == syscall.PTRACE_EVENT_CLONE {
				if np, err := syscall.PtraceGetEventMsg(wp); err == nil {
					live++
					_ = np // the new thread reports a SIGSTOP of its own, resumed below
				}
			}
		case sig == syscall.SIGSTOP:
			// a new thread's first stop, or ours
		default:
			inject = int(sig) // a signal for the program (SIGURG preemption, SIGUSR2, ...): pass it on
		}
		syscall.PtraceSyscall(wp, inject)
	}
	dump()
}
