package stt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/renameio/v2"
)

const (
	minWavSize      = 1000  // ~30ms of audio at 16kHz mono 16-bit; catches accidental double-taps
	bytesPerSec     = 32000 // 16-bit mono 16kHz = 32000 bytes/sec
	defaultModel    = ".local/share/whisper/ggml-small.bin"
	defaultModelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin"
)

func ModelPath() (string, error) {
	if m := os.Getenv("WHISPER_MODEL"); m != "" {
		return m, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory for default model path: %w", err)
	}
	return filepath.Join(home, defaultModel), nil
}

// EnsureModel checks that the model file exists. If it is missing and the
// default model is in use (WHISPER_MODEL not set), it downloads ggml-small.bin
// from huggingface automatically.
func EnsureModel(modelPath string) error {
	if _, err := os.Stat(modelPath); err == nil {
		return nil // already exists
	}
	if os.Getenv("WHISPER_MODEL") != "" {
		return fmt.Errorf("model file %s not found (not auto-downloading because WHISPER_MODEL is set)", modelPath)
	}

	log.Printf("model %s not found, downloading from %s", modelPath, defaultModelURL)

	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		return fmt.Errorf("create model directory: %w", err)
	}

	// Give systemd 10 minutes for the download.
	if err := sdNotify("EXTEND_TIMEOUT_USEC=600000000"); err != nil {
		log.Printf("sd_notify: %v", err)
	}

	resp, err := http.Get(defaultModelURL)
	if err != nil {
		return fmt.Errorf("download model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download model: HTTP %d", resp.StatusCode)
	}

	t, err := renameio.NewPendingFile(modelPath)
	if err != nil {
		return err
	}
	defer t.Cleanup()

	pw := &progressWriter{
		dst:       t,
		total:     resp.ContentLength,
		threshold: 50 * 1024 * 1024, // log every 50 MB
	}
	if _, err := io.Copy(pw, resp.Body); err != nil {
		return fmt.Errorf("download model: %w", err)
	}

	if err := t.CloseAtomicallyReplace(); err != nil {
		return fmt.Errorf("download model: %w", err)
	}

	log.Printf("model downloaded to %s (%d bytes)", modelPath, pw.written)
	return nil
}

type progressWriter struct {
	dst       io.Writer
	total     int64
	written   int64
	lastLog   int64
	threshold int64
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.dst.Write(p)
	pw.written += int64(n)
	if pw.written-pw.lastLog >= pw.threshold {
		if pw.total > 0 {
			log.Printf("downloading model: %d / %d MB (%d%%)",
				pw.written/(1024*1024), pw.total/(1024*1024),
				pw.written*100/pw.total)
		} else {
			log.Printf("downloading model: %d MB", pw.written/(1024*1024))
		}
		pw.lastLog = pw.written
	}
	return n, err
}

func modelName(modelPath string) string {
	name := filepath.Base(modelPath)
	name = strings.TrimSuffix(name, ".bin")
	name = strings.TrimSuffix(name, ".gguf")
	return name
}

// Transcribe runs whisper-cli on the given WAV file and returns the
// transcribed text and inference duration. The context controls
// cancellation/timeout.
func Transcribe(ctx context.Context, wavPath, modelPath string) (string, time.Duration, error) {
	threads := os.Getenv("WHISPER_THREADS")
	if threads == "" {
		threads = strconv.Itoa(runtime.NumCPU())
	}
	cmd := exec.CommandContext(ctx, "whisper-cli",
		"-m", modelPath,
		"-f", wavPath,
		"-l", "auto",
		"-nt",
		"-ng",
		"-t", threads,
		"-bs", "1",
		"-bo", "1",
	)
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	out, err := cmd.Output()
	dur := time.Since(start)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", dur, fmt.Errorf("whisper-cli: %w\nstderr: %s", err, exitErr.Stderr)
		}
		return "", dur, fmt.Errorf("whisper-cli: %w", err)
	}

	text := strings.TrimSpace(string(out))
	return text, dur, nil
}

// TranscribeTimeout returns a context timeout proportional to the audio length.
func TranscribeTimeout(wavPath string) time.Duration {
	info, err := os.Stat(wavPath)
	if err != nil {
		return 60 * time.Second
	}
	audioSecs := int(info.Size()) / bytesPerSec
	timeoutSecs := audioSecs*3 + 30 // ~3x realtime + 30s for model loading overhead
	return time.Duration(timeoutSecs) * time.Second
}

// Archive copies the WAV and transcription result to ~/stt/YYYY-MM-DD/recording-NNNNN/.
// Returns the archive directory path on success.
func Archive(wavPath, modelPath, text string, dur time.Duration) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	dayDir := filepath.Join(home, "stt", time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return "", err
	}

	next := nextRecordingNumber(dayDir)
	recDir := filepath.Join(dayDir, fmt.Sprintf("recording-%05d", next))
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		return "", err
	}

	if err := copyFile(wavPath, filepath.Join(recDir, "mic-raw-capture.wav")); err != nil {
		return "", fmt.Errorf("archive wav: %w", err)
	}

	meta := fmt.Sprintf("duration_ms: %d\ntext: %q\n", dur.Milliseconds(), text)
	metaPath := filepath.Join(recDir, modelName(modelPath)+".txt")
	if err := os.WriteFile(metaPath, []byte(meta), 0o644); err != nil {
		return "", fmt.Errorf("archive metadata: %w", err)
	}

	return recDir, nil
}

// captureActiveWindow returns the currently focused X11 window ID,
// or empty string on failure. Used to target paste at the correct
// window even if focus changes during transcription.
func captureActiveWindow() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xdotool", "getactivewindow").Output()
	if err != nil {
		log.Printf("xdotool getactivewindow: %v", err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Paste copies text to the PRIMARY X11 selection and simulates Shift+Insert.
// If windowID is non-empty, the key event targets that specific window.
func Paste(text, windowID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "xclip", "-selection", "primary")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xclip: %w", err)
	}

	args := []string{"key", "--clearmodifiers"}
	if windowID != "" {
		args = append(args, "--window", windowID)
	}
	args = append(args, "shift+Insert")
	if err := exec.CommandContext(ctx, "xdotool", args...).Run(); err != nil {
		return fmt.Errorf("xdotool: %w", err)
	}
	return nil
}

// WavSize returns the file size, or 0 if the file doesn't exist.
func WavSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func nextRecordingNumber(dayDir string) int {
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		return 1
	}

	var nums []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "recording-") {
			continue
		}
		numStr := strings.TrimPrefix(name, "recording-")
		if n, err := strconv.Atoi(numStr); err == nil {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		return 1
	}
	sort.Ints(nums)
	return nums[len(nums)-1] + 1
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	t, err := renameio.NewPendingFile(dst)
	if err != nil {
		return err
	}
	defer t.Cleanup()

	if _, err := io.Copy(t, in); err != nil {
		return err
	}
	return t.CloseAtomicallyReplace()
}
