// Package fakecli is the stand-in for easytier-cli the tests execute. A test
// binary re-executes itself with the argv the credential issuer builds, so
// TestMain hands control here before the testing package parses its own flags.
package fakecli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// NowEnv is the unix second the stand-in treats as "now". Tests pin it to their
// fake clock, so an issued credential expires in the same frame as the server.
const NowEnv = "MESH_TEST_CLI_NOW"

// DelayEnv makes every call sleep that many milliseconds before answering, so a
// test can hold an issuance open while another operation races it.
const DelayEnv = "MESH_TEST_CLI_DELAY_MS"

// FailGenerateEnv makes credential generation fail after its delay, the way a
// seed that is reachable but refuses (or dies mid-answer) behaves.
const FailGenerateEnv = "MESH_TEST_CLI_GENERATE_FAIL"

// StderrBytesEnv makes a failing generation write that many bytes to stderr
// first, so a test can prove the failure output stays inside its budget.
const StderrBytesEnv = "MESH_TEST_CLI_STDERR_BYTES"

// StdoutBytesEnv makes a revocation write that many bytes to stdout before
// answering, so a test can prove each stream keeps its own budget.
const StdoutBytesEnv = "MESH_TEST_CLI_STDOUT_BYTES"

// HoldPipeEnv makes a successful generation leave a child holding stdout and
// stderr for that many milliseconds. The parent answers and exits at once, so
// only the inherited pipes keep the caller's Wait open.
const HoldPipeEnv = "MESH_TEST_CLI_HOLD_MS"

// SecretPrefix keeps the stand-in's deterministic secret from looking like the
// sha256 of a bare id.
const SecretPrefix = "fakecli|"

// IDEnv, SecretEnv and ExpiryEnv make the stand-in answer something other than
// it was asked for — a changed CLI version, a reordered field, an ignored
// --ttl — so a test can prove the control plane refuses an answer that does not
// belong to the request.
const (
	IDEnv     = "MESH_TEST_CLI_GENERATE_ID"
	SecretEnv = "MESH_TEST_CLI_GENERATE_SECRET"
	ExpiryEnv = "MESH_TEST_CLI_GENERATE_EXPIRY"
)

// Invoked handles the call and exits the process when it is running as the
// stand-in easytier-cli; it reports false for a normal test run.
func Invoked() bool {
	if len(os.Args) < 2 || os.Args[1] != "-p" {
		return false
	}
	// os.Args[2] is the rpc address; the rest is what the issuer built.
	handle(os.Args[3:])
	return false
}

// SecretFor returns the credential secret the stand-in generates for id: a
// deterministic 32-byte value in the shape the seed produces (a base64 X25519
// private key), so a test can prove that the answer it received and the
// credential it was handed describe the same issuance.
func SecretFor(id string) string {
	sum := sha256.Sum256([]byte(SecretPrefix + id))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func handle(args []string) {
	if len(args) >= 2 && args[0] == "hold-pipe" {
		holdPipe(args[1])
	}
	joined := " " + strings.Join(args, " ") + " "
	delay()
	switch {
	case strings.Contains(joined, " credential generate "):
		holdPipes()
		if os.Getenv(FailGenerateEnv) != "" {
			noisyStderr()
			fmt.Fprintln(os.Stderr, "fakecli: the seed refused to sign")
			os.Exit(1)
		}
		id := valueOf(args, "--credential-id")
		ttl, _ := strconv.ParseInt(valueOf(args, "--ttl"), 10, 64)
		if raw := os.Getenv(IDEnv); raw != "" {
			id = raw
		}
		secret := SecretFor(id)
		if raw := os.Getenv(SecretEnv); raw != "" {
			secret = raw
		}
		expiry := nowUnix() + ttl
		if raw := os.Getenv(ExpiryEnv); raw != "" {
			if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
				expiry = value
			}
		}
		fmt.Printf("{\"credential_id\":%q,\"credential_secret\":%q,\"expiry_unix\":%q}\n",
			id, secret, strconv.FormatInt(expiry, 10))
	case strings.Contains(joined, " credential revoke "):
		// 真实的 easytier-cli 对种子不认识的 id 打印这一行并以 0 退出，签发通道
		// 只看退出码。种子重启过、凭据早已过期、上一次撤销已经生效都走这一支，
		// 所以 stand-in 也这样回答：撤销是幂等的。
		//
		// 测试要量两条输出流的预算时，先写 stderr 再写 stdout：共用一份预算的实现
		// 会把后者挤成截断，各自有界的实现两条都放得下。
		if raw := os.Getenv(StdoutBytesEnv); raw != "" {
			noisyStderr()
			writeBytes(os.Stdout, raw)
			os.Exit(0)
		}
		fmt.Println("Credential not found")
	default:
		fmt.Fprintf(os.Stderr, "fakecli: unexpected args %v\n", args)
		os.Exit(2)
	}
	os.Exit(0)
}

func valueOf(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

// noisyStderr writes the requested number of bytes to stderr before failing.
func noisyStderr() {
	writeBytes(os.Stderr, os.Getenv(StderrBytesEnv))
}

// writeBytes writes exactly n bytes of filler to w.
func writeBytes(w *os.File, raw string) {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return
	}
	chunk := bytes.Repeat([]byte("x"), 4096)
	for written := 0; written < n; {
		size := min(len(chunk), n-written)
		if _, err := w.Write(chunk[:size]); err != nil {
			return
		}
		written += size
	}
}

// holdPipes starts a child that inherits this process's stdout and stderr and
// sleeps there: the pipes stay open after this process exits.
func holdPipes() {
	raw := os.Getenv(HoldPipeEnv)
	if raw == "" {
		return
	}
	if _, err := strconv.Atoi(raw); err != nil {
		return
	}
	child := exec.Command(os.Args[0], "-p", os.Args[2], "hold-pipe", raw)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	// 不 Wait：这个进程马上退出，孙进程由系统收养，睡完自己结束。
	_ = child.Start()
}

// holdPipe is the child side: it keeps writing into the inherited pipe, so it
// dies as soon as the caller force-closes its end (a write to a closed pipe
// fails). A leaked holder would otherwise outlive the test and, on Windows,
// keep the test binary locked.
func holdPipe(raw string) {
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		os.Exit(0)
	}
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stdout.Write([]byte(".")); err != nil {
			os.Exit(0)
		}
		time.Sleep(50 * time.Millisecond)
	}
	os.Exit(0)
}

func delay() {
	raw := os.Getenv(DelayEnv)
	if raw == "" {
		return
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func nowUnix() int64 {
	if raw := os.Getenv(NowEnv); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return value
		}
	}
	return time.Now().Unix()
}
