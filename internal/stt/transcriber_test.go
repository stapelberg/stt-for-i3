package stt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNextRecordingNumber(t *testing.T) {
	tests := []struct {
		name string
		dirs []string
		want int
	}{
		{"empty dir", nil, 1},
		{"one recording", []string{"recording-00001"}, 2},
		{"gap in sequence", []string{"recording-00001", "recording-00003"}, 4},
		{"non-recording dirs ignored", []string{"other-dir", "recording-00005"}, 6},
		{"zero-padded parsing", []string{"recording-00099"}, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, d := range tt.dirs {
				if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			got := nextRecordingNumber(dir)
			if got != tt.want {
				t.Errorf("nextRecordingNumber() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNextRecordingNumberNonexistentDir(t *testing.T) {
	got := nextRecordingNumber("/nonexistent/path")
	if got != 1 {
		t.Errorf("nextRecordingNumber(nonexistent) = %d, want 1", got)
	}
}

func TestTranscribeTimeout(t *testing.T) {
	tests := []struct {
		name string
		size int
		want time.Duration
	}{
		{"1 second of audio", bytesPerSec, 33 * time.Second},
		{"10 seconds of audio", 10 * bytesPerSec, 60 * time.Second},
		{"empty file", 0, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wav := filepath.Join(t.TempDir(), "test.wav")
			if err := os.WriteFile(wav, make([]byte, tt.size), 0o644); err != nil {
				t.Fatal(err)
			}
			got := TranscribeTimeout(wav)
			if got != tt.want {
				t.Errorf("TranscribeTimeout(%d bytes) = %v, want %v", tt.size, got, tt.want)
			}
		})
	}
}

func TestTranscribeTimeoutNonexistent(t *testing.T) {
	got := TranscribeTimeout("/nonexistent/file.wav")
	if got != 60*time.Second {
		t.Errorf("TranscribeTimeout(nonexistent) = %v, want 60s", got)
	}
}

func TestModelName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/path/to/ggml-small.bin", "ggml-small"},
		{"/path/to/ggml-large-v3.gguf", "ggml-large-v3"},
		{"/path/to/model", "model"},
		{"ggml-small.bin", "ggml-small"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := modelName(tt.path)
			if got != tt.want {
				t.Errorf("modelName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestEnsureModelAlreadyExists(t *testing.T) {
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Should return nil without doing anything.
	if err := EnsureModel(model); err != nil {
		t.Errorf("EnsureModel(existing) = %v, want nil", err)
	}
}

func TestEnsureModelCustomMissing(t *testing.T) {
	t.Setenv("WHISPER_MODEL", "/nonexistent/custom-model.bin")
	err := EnsureModel("/nonexistent/custom-model.bin")
	if err == nil {
		t.Fatal("EnsureModel(custom missing) = nil, want error")
	}
	if !strings.Contains(err.Error(), "WHISPER_MODEL") {
		t.Errorf("error should mention WHISPER_MODEL, got: %v", err)
	}
}

func TestWavSize(t *testing.T) {
	t.Run("nonexistent returns 0", func(t *testing.T) {
		got := WavSize("/nonexistent/file.wav")
		if got != 0 {
			t.Errorf("WavSize(nonexistent) = %d, want 0", got)
		}
	})

	t.Run("existing file", func(t *testing.T) {
		wav := filepath.Join(t.TempDir(), "test.wav")
		data := make([]byte, 12345)
		if err := os.WriteFile(wav, data, 0o644); err != nil {
			t.Fatal(err)
		}
		got := WavSize(wav)
		if got != 12345 {
			t.Errorf("WavSize() = %d, want 12345", got)
		}
	})
}
