// Package fakecli is the stand-in for easytier-cli the tests execute. A test
// binary re-executes itself with the argv the credential issuer builds, so
// TestMain hands control here before the testing package parses its own flags.
package fakecli

import (
	"encoding/base64"
	"fmt"
	"os"
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

// SecretSeparator splits the stand-in's secret from the credential id it was
// generated for.
const SecretSeparator = "|"

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

// Owner returns the credential id a secret was generated for, or "" when the
// secret did not come from this stand-in.
func Owner(secret string) string {
	raw, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return ""
	}
	id, _, _ := strings.Cut(string(raw), SecretSeparator)
	return id
}

func handle(args []string) {
	joined := " " + strings.Join(args, " ") + " "
	delay()
	switch {
	case strings.Contains(joined, " credential generate "):
		if os.Getenv(FailGenerateEnv) != "" {
			fmt.Fprintln(os.Stderr, "fakecli: the seed refused to sign")
			os.Exit(1)
		}
		id := valueOf(args, "--credential-id")
		ttl, _ := strconv.ParseInt(valueOf(args, "--ttl"), 10, 64)
		secret := base64.StdEncoding.EncodeToString([]byte(id + SecretSeparator + strconv.FormatInt(time.Now().UnixNano(), 10)))
		fmt.Printf("{\"credential_id\":%q,\"credential_secret\":%q,\"expiry_unix\":%q}\n",
			id, secret, strconv.FormatInt(nowUnix()+ttl, 10))
	case strings.Contains(joined, " credential revoke "):
		// 真实的 easytier-cli 对种子不认识的 id 打印这一行并以 0 退出，签发通道
		// 只看退出码。种子重启过、凭据早已过期、上一次撤销已经生效都走这一支，
		// 所以 stand-in 也这样回答：撤销是幂等的。
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
