// Package app holds the control plane's use cases: the startup work, the device
// lifecycle and the client release devices compare themselves against.
package app

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mesh-server/db"
	"mesh-server/signing"
)

const (
	databaseName = "mesh.db"
	templateName = "peer.default.toml"
)

// Options carries the injectable parts of the environment.
type Options struct {
	// Now is the clock used for tokens, timestamps and status; tests replace it.
	Now func() time.Time
	// FetchClient downloads a published package so its digests can be computed.
	// Nil selects the hardened client, which refuses loopback and private
	// addresses; tests that publish from a local server pass their own.
	FetchClient *http.Client
	// FetchAllow lists the prefixes the hardened client may reach even though
	// they are not public, such as an operator-owned file server on the mesh.
	FetchAllow []netip.Prefix
	// CredentialCLI 是 easytier-cli 的路径，CredentialRPC 是种子 management RPC
	// 的地址（经宿主的转发服务）。CLI 为空时不提供签发能力。
	CredentialCLI string
	CredentialRPC string
	// ReleasePublicKey 是校验发布签名用的公钥。nil 表示用编进二进制的这把
	// （signing.DefaultPublicKey）；测试在这里放自己的密钥对，这样才能构造出
	// 一个真正有效的签名。它不改变信任锚：客户端用的是它自己那份常量。
	ReleasePublicKey ed25519.PublicKey
}

// App owns DATA_DIR: the SQLite database and peer.default.toml.
// Nothing outside DATA_DIR is state, and DATA_DIR holds no configuration.
type App struct {
	dataDir   string
	database  *db.DB
	now       func() time.Time
	fetch     *http.Client
	revokedMu sync.Mutex
	revoked   map[string]struct{}
	// locks 按设备串行化凭据生命周期：删除、换绑、确认、签发对同一台设备排队。
	locks *keyedLock
	// credential 为空表示这台控制面没有配置签发通道。
	credential *credentialIssuer
	// releaseKey 校验发布签名，见 Options.ReleasePublicKey。
	releaseKey ed25519.PublicKey
}

// Open prepares DATA_DIR and returns the ready application.
func Open(dataDir string, opts Options) (*App, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("data directory is empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("tighten data directory: %w", err)
	}
	database, err := db.Open(filepath.Join(dataDir, databaseName))
	if err != nil {
		return nil, err
	}
	a := &App{
		dataDir:    dataDir,
		database:   database,
		now:        opts.Now,
		fetch:      opts.FetchClient,
		revoked:    make(map[string]struct{}),
		locks:      newKeyedLock(),
		releaseKey: opts.ReleasePublicKey,
	}
	if a.releaseKey == nil {
		a.releaseKey = signing.DefaultPublicKey()
	}
	if strings.TrimSpace(opts.CredentialCLI) != "" {
		a.credential = &credentialIssuer{
			cliPath: strings.TrimSpace(opts.CredentialCLI),
			rpcAddr: strings.TrimSpace(opts.CredentialRPC),
		}
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.fetch == nil {
		a.fetch = newFetchClient(opts.FetchAllow)
	}
	return a, nil
}

// Close releases the database.
func (a *App) Close() error {
	return a.database.Close()
}

// Now is the application clock.
func (a *App) Now() time.Time { return a.now() }

// Probe verifies that DATA_DIR has the expected permissions and that the
// database can be written to and cleaned up again.
func (a *App) Probe(ctx context.Context) error {
	info, err := os.Stat(a.dataDir)
	if err != nil {
		return fmt.Errorf("stat data directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data directory %s is not a directory", a.dataDir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return fmt.Errorf("data directory permissions are %04o, want 0700", info.Mode().Perm())
	}
	if err := a.database.Probe(ctx); err != nil {
		return fmt.Errorf("database probe: %w", err)
	}
	return nil
}

func (a *App) templatePath() string {
	return filepath.Join(a.dataDir, templateName)
}

// PurgeExpiredTokens clears registration tokens whose validity has ended; the
// ticker started in main.go calls it every minute.
func (a *App) PurgeExpiredTokens() error {
	purged, err := a.database.PurgeExpiredTokens(a.now().Unix())
	if err != nil {
		return err
	}
	if purged > 0 {
		slog.Info("expired registration tokens purged", "count", purged)
	}
	return nil
}

func trimName(s string) string { return strings.TrimSpace(s) }

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
