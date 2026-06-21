package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExpandPath(t *testing.T) {
	os.Setenv("TEST_FUSION_VAR", "my_test_val")
	defer os.Unsetenv("TEST_FUSION_VAR")

	// Test windows-style
	res1 := expandPath("%TEST_FUSION_VAR%/subdir")
	expected1 := filepath.Clean("my_test_val/subdir")
	if res1 != expected1 {
		t.Errorf("expandPath(%%TEST_FUSION_VAR%%/subdir) = %q; expected %q", res1, expected1)
	}

	// Test unix-style
	res2 := expandPath("$TEST_FUSION_VAR/subdir")
	expected2 := filepath.Clean("my_test_val/subdir")
	if res2 != expected2 {
		t.Errorf("expandPath($TEST_FUSION_VAR/subdir) = %q; expected %q", res2, expected2)
	}
}

func TestFilepathClean(t *testing.T) {
	p1 := "C:\\Users\\John/AppData\\Local\\"
	cleaned1 := filepathClean(p1)
	
	// It should clean trailing backslashes/slashes
	if strings.HasSuffix(cleaned1, "\\") || strings.HasSuffix(cleaned1, "/") {
		t.Errorf("filepathClean did not strip trailing slashes: %q", cleaned1)
	}

	if filepathClean("A/B") != filepathClean("A\\B") {
		t.Errorf("expected A/B and A\\B to clean to same format")
	}
}

func TestDownloadBinary(t *testing.T) {
	// Create mock HTTP server
	dummyContent := "dummy executable data"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(dummyContent)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, dummyContent)
	}))
	defer server.Close()

	// Temp output file
	tempDir, err := os.MkdirTemp("", "fusiondb-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	destPath := filepath.Join(tempDir, "downloaded_test.exe")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = downloadBinary(ctx, server.URL, destPath, "")
	if err != nil {
		t.Fatalf("downloadBinary failed: %v", err)
	}

	// Check content
	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	if string(content) != dummyContent {
		t.Errorf("downloaded content = %q; expected %q", string(content), dummyContent)
	}
}

func TestDownloadBinary_WithChecksumVerification(t *testing.T) {
	dummyContent := "dummy executable data for checksum test"
	h := sha256.New()
	h.Write([]byte(dummyContent))
	expectedChecksum := hex.EncodeToString(h.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(dummyContent)))
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, dummyContent)
	}))
	defer server.Close()

	tmpDir, _ := os.MkdirTemp("", "fusiondb-checksum-test")
	defer os.RemoveAll(tmpDir)
	destPath := filepath.Join(tmpDir, "binary")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := downloadBinary(ctx, server.URL, destPath, expectedChecksum)
	if err != nil {
		t.Fatalf("downloadBinary with correct checksum failed: %v", err)
	}
}

func TestDownloadBinary_ChecksumMismatch(t *testing.T) {
	dummyContent := "content that won't match"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(dummyContent)))
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, dummyContent)
	}))
	defer server.Close()

	tmpDir, _ := os.MkdirTemp("", "fusiondb-checksum-mismatch-test")
	defer os.RemoveAll(tmpDir)
	destPath := filepath.Join(tmpDir, "binary")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := downloadBinary(ctx, server.URL, destPath, "deadbeef")
	if err == nil {
		t.Fatal("expected error for checksum mismatch, got nil")
	}

	if _, statErr := os.Stat(destPath); !os.IsNotExist(statErr) {
		t.Error("expected temp file to be deleted after checksum mismatch")
	}
}

func TestRequireHTTPS_RemoteHTTPExits(t *testing.T) {
	err := requireHTTPS("http://example.com/binary")
	if err == nil {
		t.Error("expected error for non-localhost HTTP URL, got nil")
	}
}

func TestRequireHTTPS_LocalhostHTTPAllowed(t *testing.T) {
	for _, host := range []string{
		"http://127.0.0.1:8080/binary",
		"http://localhost:9000/binary",
	} {
		err := requireHTTPS(host)
		if err != nil {
			t.Errorf("expected localhost HTTP to be allowed, got error for %s: %v", host, err)
		}
	}
}

func TestRequireHTTPS_HTTPSAlwaysAllowed(t *testing.T) {
	err := requireHTTPS("https://releases.fusiondb.io/fusiondb.exe")
	if err != nil {
		t.Errorf("expected HTTPS to be allowed, got: %v", err)
	}
}

func TestWriteSampleManifest(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fusiondb-manifest-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manifestPath := filepath.Join(tempDir, "manifest.json")
	err = writeSampleManifest(manifestPath)
	if err != nil {
		t.Fatalf("writeSampleManifest failed: %v", err)
	}

	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Errorf("manifest file was not created")
	}
}
