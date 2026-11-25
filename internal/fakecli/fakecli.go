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
	switch {
	case strings.Contains(joined, " credential generate "):
		id := valueOf(args, "--credential-id")
		ttl, _ := strconv.ParseInt(valueOf(args, "--ttl"), 10, 64)
		secret := base64.StdEncoding.EncodeToString([]byte(id + SecretSeparator + strconv.FormatInt(time.Now().UnixNano(), 10)))
		fmt.Printf("{\"credential_id\":%q,\"credential_secret\":%q,\"expiry_unix\":%q}\n",
			id, secret, strconv.FormatInt(nowUnix()+ttl, 10))
	case strings.Contains(joined, " credential revoke "):
		fmt.Println("{\"success\":true}")
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

func nowUnix() int64 {
	if raw := os.Getenv(NowEnv); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return value
		}
	}
	return time.Now().Unix()
}
