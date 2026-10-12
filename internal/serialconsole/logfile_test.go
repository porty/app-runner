package serialconsole

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestLogFileKeepsOnlyThePreviousRunAndAppendsOnResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vms", "vm-id", "serial.log")
	for _, run := range []string{"first run\n", "second run\n", "third run\n"} {
		log := openLog(path, true)
		if log == nil {
			t.Fatal("log was not opened")
		}
		log.Write([]byte(run[:3]))
		log.Write([]byte(run[3:]))
		log.Close()
	}
	if got := readFile(t, path); got != "third run\n" {
		t.Fatalf("current log = %q", got)
	}
	if got := readFile(t, path+".1"); got != "second run\n" {
		t.Fatalf("previous log = %q", got)
	}
	if matches, _ := filepath.Glob(path + ".*"); len(matches) != 1 {
		t.Fatalf("more than one previous run was kept: %v", matches)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	resumed := openLog(path, false)
	resumed.Write([]byte("after restart\n"))
	resumed.Close()
	if got := readFile(t, path); got != "third run\nafter restart\n" {
		t.Fatalf("resumed log = %q", got)
	}
	if got := readFile(t, path+".1"); got != "second run\n" {
		t.Fatalf("resuming rotated the log: previous = %q", got)
	}
	resumed.Write([]byte("after close\n"))
	if got := readFile(t, path); got != "third run\nafter restart\n" {
		t.Fatalf("write after close reached the log: %q", got)
	}
}

func TestLogFileFailuresAreIgnored(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	log := openLog(filepath.Join(blocker, "serial.log"), true)
	if log != nil {
		t.Fatal("a log was opened beneath a regular file")
	}
	log.Write([]byte("ignored"))
	log.Close()
	if openLog("", true) != nil {
		t.Fatal("an empty path opened a log")
	}

	path := filepath.Join(t.TempDir(), "serial.log")
	broken := openLog(path, true)
	_ = broken.file.Close() // make the next write fail
	broken.Write([]byte("lost"))
	if broken.file != nil {
		t.Fatal("a failed log was not abandoned")
	}
	broken.Write([]byte("still lost"))
	broken.Close()
}

func TestHubWritesSerialOutputToALogPerRun(t *testing.T) {
	socketPath, accepted := startFakeSerial(t)
	logPath := filepath.Join(t.TempDir(), "serial.log")
	hub := New(func(string) string { return socketPath }, func(string) string { return logPath })
	t.Cleanup(hub.Close)

	waitForLog := func(want string) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			contents, _ := os.ReadFile(logPath)
			if string(contents) == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("log = %q, want %q", contents, want)
			}
			time.Sleep(time.Millisecond)
		}
	}

	if err := hub.Start("vm"); err != nil {
		t.Fatal(err)
	}
	qemu := <-accepted
	_, _ = qemu.Write([]byte("boot one\n"))
	waitForLog("boot one\n")
	hub.Stop("vm")
	_ = qemu.Close()

	if err := hub.Resume("vm"); err != nil {
		t.Fatal(err)
	}
	qemu = <-accepted
	_, _ = qemu.Write([]byte("still boot one\n"))
	waitForLog("boot one\nstill boot one\n")
	hub.Stop("vm")
	_ = qemu.Close()

	if err := hub.Start("vm"); err != nil {
		t.Fatal(err)
	}
	qemu = <-accepted
	t.Cleanup(func() { _ = qemu.Close() })
	_, _ = qemu.Write([]byte("boot two\n"))
	waitForLog("boot two\n")
	if got := readFile(t, logPath+".1"); got != "boot one\nstill boot one\n" {
		t.Fatalf("previous run log = %q", got)
	}
}

func TestHubConsoleWorksWhenTheLogCannotBeOpened(t *testing.T) {
	socketPath, accepted := startFakeSerial(t)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	hub := New(func(string) string { return socketPath }, func(string) string { return filepath.Join(blocker, "serial.log") })
	t.Cleanup(hub.Close)
	if err := hub.Start("vm"); err != nil {
		t.Fatalf("an unusable log blocked the console: %v", err)
	}
	qemu := <-accepted
	t.Cleanup(func() { _ = qemu.Close() })
	if _, err := qemu.Write([]byte("booted\n")); err != nil {
		t.Fatal(err)
	}
	stream, err := hub.Attach("vm")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	output := make([]byte, len("booted\n"))
	if _, err := io.ReadFull(stream, output); err != nil {
		t.Fatal(err)
	}
	if string(output) != "booted\n" {
		t.Fatalf("console output = %q", output)
	}
}
