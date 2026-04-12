package stt

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Recorder struct {
	cmd      *exec.Cmd
	waitErr  error         // result of cmd.Wait(), valid after done is closed
	done     chan struct{} // closed when cmd.Wait() returns (broadcast-safe)
	stopped  chan struct{} // closed by Stop to tell the death-watcher to exit
	stopOnce sync.Once
}

func (r *Recorder) Start(wavPath string) error {
	r.cmd = exec.Command("arecord",
		"-f", "S16_LE",
		"-r", "16000",
		"-c", "1",
		"-t", "wav",
		wavPath,
	)
	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start arecord: %w", err)
	}

	r.done = make(chan struct{})
	r.stopped = make(chan struct{})
	r.stopOnce = sync.Once{}
	go func() {
		r.waitErr = r.cmd.Wait()
		close(r.done)
	}()
	return nil
}

func (r *Recorder) Stop() error {
	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	r.stopOnce.Do(func() { close(r.stopped) }) // signal death-watcher to exit
	// SIGTERM to let arecord finalize the WAV header.
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Process already exited — wait for the result.
		<-r.done
		return r.waitErr
	}
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case <-r.done:
		// arecord exits with non-zero status on SIGTERM, which is expected.
		var exitErr *exec.ExitError
		if errors.As(r.waitErr, &exitErr) && !exitErr.Exited() {
			return nil // killed by signal (our SIGTERM)
		}
		return r.waitErr
	case <-t.C:
		r.cmd.Process.Kill()
		<-r.done // reap the process
		return fmt.Errorf("arecord did not exit after SIGTERM, killed")
	}
}

// Done returns a channel that is closed when the process exits.
// Safe for multiple goroutines to observe concurrently.
func (r *Recorder) Done() <-chan struct{} {
	return r.done
}

// WaitErr returns the result of cmd.Wait(). Only valid after Done() is closed.
func (r *Recorder) WaitErr() error {
	return r.waitErr
}

func (r *Recorder) Stopped() <-chan struct{} {
	return r.stopped
}
