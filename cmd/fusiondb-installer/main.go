package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

func main() {
	fmt.Println("==============================================================")
	fmt.Println("                  FusionDB Installer v1.0                     ")
	fmt.Println("==============================================================")

	var defaultDir, defaultDbDir string
	if runtime.GOOS == "windows" {
		defaultDir = "%USERPROFILE%\\.fusiondb"
		defaultDbDir = "%USERPROFILE%\\.fusiondb\\data"
	} else {
		defaultDir = "$HOME/.fusiondb"
		defaultDbDir = "$HOME/.fusiondb/data"
	}

	// Define command-line flags
	installDirRaw := flag.String("dir", defaultDir, "Target installation directory")
	dbDirRaw := flag.String("db-dir", defaultDbDir, "Default database data directory")
	downloadURL := flag.String("url", "http://localhost:8080/"+getExeName(), "Download URL for "+getExeName())
	expectedChecksum := flag.String("checksum", "", "Expected SHA-256 hex digest of the downloaded binary")
	serveMock := flag.Bool("serve", false, "Start a mock HTTP server to compile and serve "+getExeName()+" locally")
	localBinaryPath := flag.String("local-binary", "", "Path to a local "+getExeName()+" to install (skips download)")
	flag.Parse()

	// 1. Expand paths
	installDir := expandPath(*installDirRaw)
	dbDir := expandPath(*dbDirRaw)
	binDir := filepath.Join(installDir, "bin")
	samplesDir := filepath.Join(installDir, "samples")
	installedExePath := filepath.Join(binDir, getExeName())

	fmt.Printf("[→] Installation Target: %s\n", installDir)
	fmt.Printf("[→] Database Directory:  %s\n", dbDir)
	fmt.Println("--------------------------------------------------------------")

	// 2. Create target directories
	fmt.Println("[→] Creating installation directories...")
	dirs := []string{binDir, dbDir, samplesDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Printf("[!] Error creating directory %s: %v\n", dir, err)
			os.Exit(1)
		}
	}
	fmt.Println("[✓] Directories created successfully.")

	// 3. Obtain the executable
	var err error
	if *localBinaryPath != "" {
		fmt.Printf("[→] Installing local binary from: %s\n", *localBinaryPath)
		err = copyLocalFile(*localBinaryPath, installedExePath)
		if err != nil {
			fmt.Printf("[!] Error copying local binary: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[✓] Copy completed.")
	} else if len(embeddedBinary) > 0 {
		fmt.Printf("[→] Extracting embedded %s...\n", getExeName())
		err = os.WriteFile(installedExePath, embeddedBinary, 0755)
		if err != nil {
			fmt.Printf("[!] Error extracting embedded binary: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[✓] Extraction completed.")
	} else {
		urlToUse := *downloadURL
		var mockServer *http.Server

		if *serveMock {
			fmt.Printf("[→] Starting mock server (compiling %s from source)...\n", getExeName())
			urlToUse, mockServer, err = startMockServer(installDir)
			if err != nil {
				fmt.Printf("[!] Error starting mock server: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("[✓] Mock server serving compiled binary at: %s\n", urlToUse)
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				mockServer.Shutdown(ctx)
			}()
		}

		fmt.Printf("[→] Downloading %s from: %s\n", getExeName(), urlToUse)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		err = downloadBinary(ctx, urlToUse, installedExePath, *expectedChecksum)
		if err != nil {
			fmt.Printf("[!] Error downloading binary: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("\n[✓] Download completed.")
	}

	// 4. Update PATH
	fmt.Println("[→] Setting up PATH environment variable...")
	pathAdded, err := updatePath(binDir)
	if err != nil {
		fmt.Printf("[!] Warning: Could not update PATH: %v\n", err)
		fmt.Printf("    Please manually add the following folder to your PATH:\n")
		fmt.Printf("    %s\n", binDir)
	} else if pathAdded {
		fmt.Println("[✓] PATH updated successfully. (Requires restarting open terminals)")
	} else {
		fmt.Println("[✓] PATH is already configured correctly.")
	}

	// 5. Create a sample UFL manifest
	fmt.Println("[→] Creating sample UFL manifest...")
	sampleManifestPath := filepath.Join(samplesDir, "sample_manifest.json")
	err = writeSampleManifest(sampleManifestPath)
	if err != nil {
		fmt.Printf("[!] Error writing sample manifest: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[✓] Sample manifest created.")

	// 6. Initialize & Seed Database
	fmt.Println("[→] Initializing and seeding the database...")
	// Run: fusiondb.exe -db <dbDir> seed <samplesDir>
	cmd := exec.Command(installedExePath, "-db="+dbDir, "seed", samplesDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Printf("[!] Error seeding database: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[✓] Database initialized and seeded successfully.")

	// 7. Verification Query
	fmt.Println("[→] Verifying database queries...")
	// Run: fusiondb.exe -db <dbDir> query person:john_doe
	verifyCmd := exec.Command(installedExePath, "-db="+dbDir, "query", "person:john_doe")
	output, err := verifyCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("[!] Verification query failed: %v\n", err)
		fmt.Printf("    Output: %s\n", string(output))
		os.Exit(1)
	}
	fmt.Printf("[✓] Verification query output:\n%s", string(output))

	// 8. Print next steps
	fmt.Println("==============================================================")
	fmt.Println("              FusionDB Installed Successfully!                ")
	fmt.Println("==============================================================")
	fmt.Println("To run FusionDB, open a NEW terminal window and try running:")
	fmt.Printf("  %s -db=\"%s\" query \"person:john_doe\"\n", getExeName(), dbDir)
	fmt.Println("==============================================================")
}

// expandPath replaces environment variables like %USERPROFILE% or $HOME with their values.
func expandPath(path string) string {
	// Replace Windows-style %VAR% with Go-style $VAR
	re := regexp.MustCompile(`%([^%]+)%`)
	path = re.ReplaceAllStringFunc(path, func(m string) string {
		varName := strings.Trim(m, "%")
		return os.Getenv(varName)
	})
	// Expand unix style $VAR or ${VAR}
	return filepath.Clean(os.ExpandEnv(path))
}

// copyLocalFile copies a file from src to dst.
func copyLocalFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// requireHTTPS returns an error if the URL uses unencrypted HTTP to a non-localhost host.
func requireHTTPS(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", urlStr, err)
	}
	if u.Scheme != "http" {
		return nil
	}
	host := u.Hostname()
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return nil
	}
	return fmt.Errorf("HTTPS required for remote binary downloads; refusing HTTP URL: %s", urlStr)
}

// downloadBinary downloads a file with streaming SHA-256 verification and exponential backoff retry.
// If expectedSHA256 is non-empty, the download is aborted and the temp file deleted on mismatch.
func downloadBinary(ctx context.Context, urlStr, destPath, expectedSHA256 string) error {
	if err := requireHTTPS(urlStr); err != nil {
		slog.Error("protocol enforcement failure", "error", err.Error())
		os.Exit(1)
	}

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		lastErr = downloadAttempt(attemptCtx, urlStr, destPath, expectedSHA256)
		cancel()

		if lastErr == nil {
			return nil
		}

		// Checksum mismatch is not retryable
		if strings.HasPrefix(lastErr.Error(), "integrity violation") {
			return lastErr
		}

		slog.Error("download attempt failed, will retry",
			"attempt", attempt+1,
			"max_retries", maxRetries,
			"error", lastErr.Error())
	}
	return fmt.Errorf("download failed after %d attempts: %w", maxRetries, lastErr)
}

// downloadAttempt performs a single download attempt with streaming SHA-256 verification.
func downloadAttempt(ctx context.Context, urlStr, destPath, expectedSHA256 string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned bad status: %s", resp.Status)
	}

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to open destination: %w", err)
	}

	totalBytes := resp.ContentLength
	var downloaded int64
	startTime := time.Now()

	hasher := sha256.New()
	// TeeReader: data flows body → hasher (tap) → out (sink); progress tracked inline
	tee := io.TeeReader(resp.Body, hasher)

	printProgress := func(down, tot int64, speed float64) {
		if tot <= 0 {
			return
		}
		const barWidth = 30
		pct := float64(down) / float64(tot) * 100
		filled := int(float64(barWidth) * float64(down) / float64(tot))
		bar := strings.Repeat("=", filled)
		if filled < barWidth {
			bar += ">"
			bar += strings.Repeat("-", barWidth-filled-1)
		}
		speedStr := fmt.Sprintf("%.1f MB/s", speed/(1024*1024))
		if speed < 1024*1024 {
			speedStr = fmt.Sprintf("%.1f KB/s", speed/1024)
		}
		fmt.Printf("\r[%s] %.1f%% (%.1f MB / %.1f MB) at %s  ",
			bar, pct, float64(down)/(1024*1024), float64(tot)/(1024*1024), speedStr)
	}

	buffer := make([]byte, 32*1024)
	lastUpdate := time.Now()

	for {
		n, readErr := tee.Read(buffer)
		if n > 0 {
			_, wErr := out.Write(buffer[:n])
			if wErr != nil {
				out.Close()
				os.Remove(destPath)
				return fmt.Errorf("write error: %w", wErr)
			}
			downloaded += int64(n)

			now := time.Now()
			if now.Sub(lastUpdate) >= 100*time.Millisecond || downloaded == totalBytes {
				elapsed := now.Sub(startTime).Seconds()
				var speed float64
				if elapsed > 0 {
					speed = float64(downloaded) / elapsed
				}
				printProgress(downloaded, totalBytes, speed)
				lastUpdate = now
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			os.Remove(destPath)
			return fmt.Errorf("read error during download: %w", readErr)
		}
	}

	out.Close()

	if expectedSHA256 != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != strings.ToLower(expectedSHA256) {
			os.Remove(destPath)
			return fmt.Errorf("integrity violation: expected SHA-256 %s, got %s", expectedSHA256, got)
		}
	}

	return nil
}

// startMockServer compiles the local fusiondb code and starts a local server to serve it.
func startMockServer(installDir string) (string, *http.Server, error) {
	tempExe := filepath.Join(installDir, "temp_build_"+getExeName())
	
	// Compile cmd/fusiondb
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", tempExe, "./cmd/fusiondb")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("failed to compile fusiondb: %v, output: %s", err, string(output))
	}

	// Listen on dynamic free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Remove(tempExe)
		return "", nil, err
	}

	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/"+getExeName(), func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, tempExe)
		// Clean up the temp exe after serving
		go func() {
			time.Sleep(1 * time.Second)
			os.Remove(tempExe)
		}()
	})

	server := &http.Server{
		Handler: mux,
	}

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("[!] Mock server error: %v\n", err)
		}
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/%s", port, getExeName())
	return url, server, nil
}

// writeSampleManifest writes a sample UFL JSON manifest to path.
func writeSampleManifest(path string) error {
	sample := map[string]interface{}{
		"ufl_version": "1.0",
		"action":      "fuse",
		"entity": map[string]interface{}{
			"id":   "person:john_doe",
			"type": "Person",
			"tier": "verified",
			"vector": []float32{0.12, -0.05, 0.88},
			"kv": map[string]interface{}{
				"full_name": "John Michael Doe",
				"active":    true,
			},
			"relations": map[string]interface{}{
				"secondary": []map[string]interface{}{
					{
						"predicate": "has_email",
						"object":    "john@example.com",
					},
				},
			},
		},
	}

	data, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// getExeName returns the database executable name based on the OS.
func getExeName() string {
	if runtime.GOOS == "windows" {
		return "fusiondb.exe"
	}
	return "fusiondb"
}
