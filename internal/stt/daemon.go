package stt

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Daemon struct {
	socketPath string
	wavPath    string
	modelPath  string

	mu     sync.Mutex // protects state, cancel, notify, generation
	state  State
	notify Notifier
	cancel context.CancelFunc
	gen    uint64 // incremented on each transcription cycle

	wg       sync.WaitGroup // tracks in-flight transcription goroutine
	recorder Recorder
}

func NewDaemon(socketPath string) (*Daemon, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return nil, fmt.Errorf("XDG_RUNTIME_DIR is not set; cannot determine secure runtime path")
	}
	modelPath, err := ModelPath()
	if err != nil {
		return nil, err
	}
	return &Daemon{
		socketPath: socketPath,
		wavPath:    filepath.Join(runtime, "whisper-stt.wav"),
		modelPath:  modelPath,
	}, nil
}

func (d *Daemon) Run() error {
	// Ensure model file exists, downloading the default if necessary.
	if err := EnsureModel(d.modelPath); err != nil {
		return err
	}
	info, err := os.Stat(d.modelPath)
	if err != nil {
		return fmt.Errorf("model file %s: %w", d.modelPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("model file %s is not a regular file", d.modelPath)
	}

	// Check for stale socket.
	if conn, err := net.Dial("unix", d.socketPath); err == nil {
		conn.Close()
		return fmt.Errorf("daemon already running (socket %s accepts connections)", d.socketPath)
	}
	os.Remove(d.socketPath)

	ln, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.socketPath, err)
	}
	defer ln.Close()
	defer os.Remove(d.socketPath)

	// Handle SIGTERM gracefully: close the listener to stop accepting
	// connections, then clean up in the post-Accept path below.
	// We must NOT acquire d.mu here — handleToggle can hold it during
	// recorder.Stop(), which would deadlock.
	// Installed before recoverCrash so that SIGTERM during a long
	// recovery transcription triggers a clean shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("received %v, shutting down", sig)
		if err := sdNotify("STOPPING=1"); err != nil {
			log.Printf("sd_notify: %v", err)
		}
		ln.Close()
	}()

	// Recording-of-death recovery: transcribe any WAV left behind by a
	// previous crash. Runs before READY=1 so the daemon isn't ready
	// until recovery completes.
	if err := d.recoverCrash(); err != nil {
		log.Printf("recovery failed: %v", err)
	}

	log.Printf("stt-for-i3 %s (socket %s, model %s)", vcsRevision(), d.socketPath, d.modelPath)
	if err := sdNotify("READY=1"); err != nil {
		log.Printf("sd_notify: %v", err)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener closed (shutdown). Clean up recording state.
			d.mu.Lock()
			if d.state == Recording {
				d.recorder.Stop()
				os.Remove(d.wavPath)
			}
			if d.state == Transcribing && d.cancel != nil {
				d.cancel()
				d.cancel = nil
			}
			d.mu.Unlock()
			d.wg.Wait()
			return nil
		}
		d.handleConn(conn)
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}
	cmd := strings.TrimSpace(scanner.Text())

	switch cmd {
	case "toggle":
		newState, err := d.handleToggle()
		if err != nil {
			fmt.Fprintf(conn, "error %v\n", err)
			return
		}
		fmt.Fprintf(conn, "ok %s\n", newState)

	default:
		fmt.Fprintf(conn, "error unknown command: %s\n", cmd)
	}
}

func (d *Daemon) handleToggle() (State, error) {
	// Capture the focused window before acquiring the lock — xdotool
	// can take up to 2s and we don't want to hold the mutex that long.
	// The value is only used when transitioning from Recording.
	windowID := captureActiveWindow()

	d.mu.Lock()
	defer d.mu.Unlock()

	switch d.state {
	case Idle:
		return d.startRecording()
	case Recording:
		return d.stopRecordingAndTranscribe(windowID)
	case Transcribing:
		return d.cancelTranscription()
	}
	return d.state, fmt.Errorf("invalid state: %v", d.state)
}

// startRecording must be called with d.mu held.
func (d *Daemon) startRecording() (State, error) {
	os.Remove(d.wavPath)

	if err := d.recorder.Start(d.wavPath); err != nil {
		if err := d.notify.Show("critical", "Whisper", fmt.Sprintf("Failed to start recording: %v", err)); err != nil {
			log.Printf("notification: %v", err)
		}
		return Idle, err
	}

	d.state = Recording
	if err := d.notify.Show("low", "Whisper", "Recording..."); err != nil {
		log.Printf("notification: %v", err)
	}
	log.Printf("state → recording (wav %s)", d.wavPath)

	// Watch for unexpected arecord death.
	go func() {
		select {
		case <-d.recorder.Done():
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.state != Recording {
				return // expected exit (we called Stop)
			}
			log.Printf("arecord died unexpectedly: %v", d.recorder.WaitErr())
			d.state = Idle
			os.Remove(d.wavPath)
			if err := d.notify.Show("critical", "Whisper", "Recording failed"); err != nil {
				log.Printf("notification: %v", err)
			}
		case <-d.recorder.Stopped():
			return
		}
	}()

	return Recording, nil
}

// stopRecordingAndTranscribe must be called with d.mu held.
// windowID is captured by handleToggle before acquiring the lock.
func (d *Daemon) stopRecordingAndTranscribe(windowID string) (State, error) {
	if err := d.recorder.Stop(); err != nil {
		// arecord exits non-zero on SIGTERM — expected, not an error.
		log.Printf("arecord stopped (exit: %v)", err)
	}

	d.state = Transcribing
	if err := d.notify.Show("low", "Whisper", "Transcribing..."); err != nil {
		log.Printf("notification: %v", err)
	}
	log.Printf("state → transcribing")

	ctx, cancel := context.WithTimeout(context.Background(), TranscribeTimeout(d.wavPath))
	d.cancel = cancel
	d.gen++
	gen := d.gen

	d.wg.Add(1)
	go d.transcribe(ctx, gen, false, windowID)

	return Transcribing, nil
}

// cancelTranscription must be called with d.mu held.
func (d *Daemon) cancelTranscription() (State, error) {
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.gen++ // invalidate the stale transcription goroutine
	d.state = Idle
	if err := d.notify.Show("low", "Whisper", "Cancelled"); err != nil {
		log.Printf("notification: %v", err)
	}
	log.Printf("state → idle (cancelled)")
	return Idle, nil
}

func (d *Daemon) transcribe(ctx context.Context, gen uint64, recovery bool, windowID string) {
	defer d.wg.Done()
	defer func() {
		d.mu.Lock()
		// Only clean up if this is still our generation — a rapid
		// cancel+re-record cycle may have started a new transcription.
		if d.gen == gen && d.cancel != nil {
			d.cancel()
			d.cancel = nil
		}
		d.mu.Unlock()
	}()

	// isOurGen checks whether this goroutine still owns the current
	// transcription cycle. A cancel+re-record sequence increments
	// d.gen, making the old goroutine stale. Must be called with
	// d.mu held.
	isOurGen := func() bool { return d.gen == gen }

	// setIdle transitions to Idle with a notification, but only if
	// this goroutine still owns the cycle. Returns true if the
	// transition happened. Acquires d.mu internally.
	setIdle := func(urgency, body string) bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		if !isOurGen() {
			return false
		}
		d.state = Idle
		if body != "" {
			if err := d.notify.Show(urgency, "Whisper", body); err != nil {
				log.Printf("notification: %v", err)
			}
		} else {
			if err := d.notify.Close(); err != nil {
				log.Printf("notification: %v", err)
			}
		}
		return true
	}

	// removeWav removes the WAV file only if this goroutine still
	// owns the cycle (another cycle may be writing to the same path).
	// The lock is held across the remove to prevent a TOCTOU race
	// with a new recording cycle starting on the same path.
	removeWav := func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if isOurGen() {
			os.Remove(d.wavPath)
		}
	}

	size := WavSize(d.wavPath)
	if size < minWavSize {
		log.Printf("wav too small (%d bytes), discarding", size)
		removeWav()
		setIdle("low", "Recording too short")
		return
	}

	timeout := TranscribeTimeout(d.wavPath)
	if err := sdNotify(fmt.Sprintf("EXTEND_TIMEOUT_USEC=%d", timeout.Microseconds())); err != nil {
		log.Printf("sd_notify: %v", err)
	}
	log.Printf("transcribing %s (%d bytes, ~%ds audio, model %s)", d.wavPath, size, size/bytesPerSec, modelName(d.modelPath))

	text, dur, err := Transcribe(ctx, d.wavPath, d.modelPath)
	if err != nil {
		if ctx.Err() != nil {
			// Cancelled by user — already handled in cancelTranscription.
			log.Printf("transcription cancelled")
			removeWav()
			return
		}
		log.Printf("transcription failed: %v", err)
		removeWav() // don't keep corrupt wav for infinite recovery loop
		setIdle("critical", fmt.Sprintf("Transcription failed: %v", err))
		return
	}

	log.Printf("transcribed in %dms: %q (%d chars)", dur.Milliseconds(), text, len(text))

	if archiveDir, err := Archive(d.wavPath, d.modelPath, text, dur); err != nil {
		log.Printf("archive failed: %v", err)
		// Non-fatal — continue to paste.
	} else {
		log.Printf("archived to %s", archiveDir)
	}

	removeWav()

	if text == "" {
		setIdle("low", "(no speech detected)")
		log.Printf("state → idle (empty)")
		return
	}

	if recovery {
		// Don't paste into an arbitrary window at startup.
		setIdle("low", fmt.Sprintf("Recovered: %s", text))
		log.Printf("state → idle (recovered)")
		return
	}

	if err := Paste(text, windowID); err != nil {
		log.Printf("paste failed: %v", err)
		setIdle("critical", fmt.Sprintf("Paste failed: %v", err))
		return
	}

	setIdle("", "") // close notification
	log.Printf("state → idle (done)")
}

func (d *Daemon) recoverCrash() error {
	size := WavSize(d.wavPath)
	if size == 0 {
		return nil // no wav file
	}

	if size < minWavSize {
		log.Printf("recovery: discarding small wav (%d bytes)", size)
		return os.Remove(d.wavPath)
	}

	log.Printf("recovery: found %d byte wav from previous session", size)

	timeout := TranscribeTimeout(d.wavPath)
	if err := sdNotify(fmt.Sprintf("EXTEND_TIMEOUT_USEC=%d", timeout.Microseconds())); err != nil {
		log.Printf("sd_notify: %v", err)
	}

	d.state = Transcribing
	if err := d.notify.Show("low", "Whisper", "Recovering previous recording..."); err != nil {
		log.Printf("notification: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	d.cancel = cancel
	d.gen++
	gen := d.gen

	// Recover synchronously before accepting connections — the daemon
	// isn't ready until recovery completes.
	d.wg.Add(1)
	d.transcribe(ctx, gen, true, "") // no window ID for recovery
	return nil
}

func vcsRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(no build info)"
	}
	var rev, t string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			t = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "(no vcs info)"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev + " " + t
}
