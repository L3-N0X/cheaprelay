package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGOPCacheWithFFmpeg feeds an ffmpeg-generated MPEG-TS stream through the
// cache and checks that keyframes are found and the snapshot starts with
// PAT, PMT and a keyframe that ffmpeg can decode.
func TestGOPCacheWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.ts")
	// 5s at 30fps with a keyframe every 30 frames.
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=s=640x360:r=30", "-f", "lavfi", "-i", "sine=f=440:r=48000",
		"-t", "5", "-c:v", "libx264", "-preset", "ultrafast", "-g", "30", "-bf", "0",
		"-c:a", "aac", "-f", "mpegts", src).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("random_access_indicator", func(t *testing.T) {
		checkGOPCache(t, dir, data)
	})
	t.Run("nal_detection", func(t *testing.T) {
		// Clear the random access indicator so only NAL parsing can find keyframes.
		stripped := append([]byte(nil), data...)
		for i := 0; i+tsPacketSize <= len(stripped); i += tsPacketSize {
			p := stripped[i : i+tsPacketSize]
			if p[3]&0x20 != 0 && p[4] > 0 {
				p[5] &^= 0x40
			}
		}
		checkGOPCache(t, dir, stripped)
	})
}

func checkGOPCache(t *testing.T, dir string, data []byte) {
	c := newGOPCache(gopCacheMaxBytes)
	keyframes := 0
	for i := 0; i+tsPacketSize <= len(data); i += tsPacketSize {
		before := len(c.gop)
		c.push(data[i : i+tsPacketSize])
		if c.gop != nil && len(c.gop) == tsPacketSize && before != 0 {
			keyframes++
		}
	}
	if c.videoType != streamTypeH264 {
		t.Fatalf("video stream not found in PMT (type %#x)", c.videoType)
	}
	if keyframes < 3 {
		t.Fatalf("expected at least 3 keyframe resets, got %d", keyframes)
	}

	snap := c.snapshot()
	if snap == nil {
		t.Fatal("empty snapshot")
	}
	snapFile := filepath.Join(dir, t.Name()[len("TestGOPCacheWithFFmpeg/"):]+".ts")
	if err := os.WriteFile(snapFile, snap, 0o644); err != nil {
		t.Fatal(err)
	}
	// The snapshot must decode from its very first video frame.
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-xerror",
		"-i", snapFile, "-map", "0:v", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("snapshot not decodable: %v: %s", err, out)
	}
}

func TestIsKeyframePES(t *testing.T) {
	pes := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 5, 0, 0, 0, 0, 0}
	idr := append(append([]byte{}, pes...), 0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x67, 0x64)
	nonIDR := append(append([]byte{}, pes...), 0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x41, 0x9a)
	if !isKeyframePES(idr, streamTypeH264) {
		t.Error("SPS not detected as keyframe")
	}
	if isKeyframePES(nonIDR, streamTypeH264) {
		t.Error("non-IDR slice detected as keyframe")
	}
}
