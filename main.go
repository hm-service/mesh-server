// Command mesh-server runs the mesh control plane described by contract/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
	"mesh-server/httpx"
)

const (
	// defaultDataDir is the container mount point of the state directory.
	defaultDataDir = "/data"
	// defaultPort applies when PORT is not set. HOST always takes part in the
	// listening address, so HOST=127.0.0.1 alone still binds loopback; DATA_DIR
	// holds no configuration.
	defaultPort = 8088
	// memoryLimit keeps the process inside the documented budget unless
	// GOMEMLIMIT says otherwise.
	memoryLimit = 128 << 20
	// shutdownTimeout bounds how long in-flight requests may finish.
	shutdownTimeout = 5 * time.Second
	// readHeaderTimeout, readTimeout and idleTimeout bound how long one
	// connection may hold a request slot; a device body is at most 1MiB.
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 15 * time.Second
	idleTimeout       = 60 * time.Second
	// purgeInterval is how often expired registration tokens are cleared.
	purgeInterval = time.Minute
)

func main() {
	os.Exit(run())
}

func run() int {
	dataDirFlag := flag.String("data", "", "data directory (defaults to $DATA_DIR, then "+defaultDataDir+")")
	flag.Parse()

	loc, err := loadTimezone(os.Getenv("TZ"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return 1
	}
	time.Local = loc

	conf, err := readEnvironment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return 1
	}
	dataDir := conf.dataDir
	if *dataDirFlag != "" {
		dataDir = *dataDirFlag
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: create data directory: %v\n", err)
		return 1
	}

	logFile, err := core.OpenLog(filepath.Join(dataDir, "server.log"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: open log file: %v\n", err)
		return 1
	}
	logger := newLogger(io.MultiWriter(os.Stdout, logFile), loc)
	slog.SetDefault(logger)

	application, err := app.Open(dataDir, app.Options{
		Now:           time.Now,
		FetchAllow:    conf.fetchAllow,
		CredentialCLI: conf.credentialCLI,
		CredentialRPC: conf.credentialRPC,
	})
	if err != nil {
		logger.Error("startup failed", "error", err)
		_ = logFile.Close()
		return 1
	}

	port := conf.port
	if port == 0 {
		port = defaultPort
	}
	listen := net.JoinHostPort(conf.host, strconv.Itoa(port))
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		logger.Error("listen failed", "addr", listen, "error", err)
		_ = application.Close()
		_ = logFile.Close()
		return 1
	}

	handler := api.New(api.Options{
		App:                  application,
		Static:               frontendFS(),
		Logger:               logger,
		ReadyzToken:          conf.readyzToken,
		TokenHash:            conf.tokenHash,
		SessionKey:           conf.sessionKey,
		TrustedProxies:       conf.trustedProxies,
		MaxAuthConcurrency:   conf.maxAuth,
		MaxAPIConcurrency:    conf.maxAPI,
		MaxStaticConcurrency: conf.maxStatic,
	}).Handler()
	server := &http.Server{
		Addr:    listen,
		Handler: handler,
		// A request that stops mid-body must not hold a concurrency slot: the
		// read deadline bounds every unauthenticated upload as well.
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("server started",
		"addr", listen,
		"data_dir", dataDir,
		"start_at", time.Now().Format(time.RFC3339),
	)
	logger.Info("admin console", "url", browseURL(listen)+"/admin")

	purgeCtx, stopPurge := context.WithCancel(context.Background())
	// 清理 goroutine 必须先停：它每一轮都写数据库，停了之后再关库与日志，
	// 否则退出窗口里那一次 tick 会打到已关闭的连接上。defer 只是兜底。
	defer stopPurge()
	go purgeTokens(purgeCtx, application, logger)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			stopPurge()
			_ = application.Close()
			_ = logFile.Close()
			return 1
		}
		stopPurge()
		_ = application.Close()
		_ = logFile.Close()
		return 0
	case sig := <-signals:
		logger.Info("shutdown signal received", "signal", sig.String())
		signal.Stop(signals)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("shutdown failed", "error", err)
		return 254
	}
	logger.Info("server shutdown", "stop_at", time.Now().Format(time.RFC3339))
	stopPurge()
	if err := application.Close(); err != nil {
		logger.Error("close database failed", "error", err)
		return 255
	}
	if err := logFile.Close(); err != nil {
		return 255
	}
	return 0
}

// environment is the validated deployment configuration; DATA_DIR, HOST, PORT
// and the MAX_* limits are read here and nowhere else.
type environment struct {
	dataDir        string
	host           string
	port           int
	trustedProxies []netip.Prefix
	fetchAllow     []netip.Prefix
	readyzToken    string
	tokenHash      *core.TokenHash
	sessionKey     []byte
	maxAuth        int
	maxAPI         int
	maxStatic      int
	// credentialCLI/credentialRPC 指向 easytier-cli 与种子的 management RPC；
	// CLI 为空表示这台控制面不签发凭据。
	credentialCLI string
	credentialRPC string
}

func readEnvironment() (environment, error) {
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	host := strings.TrimSpace(os.Getenv("HOST"))
	if host == "" {
		host = "0.0.0.0"
	}
	port := 0
	if raw := strings.TrimSpace(os.Getenv("PORT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 65535 {
			return environment{}, fmt.Errorf("PORT must be an integer between 1 and 65535, got %q", raw)
		}
		port = parsed
	}
	proxies, err := httpx.ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return environment{}, err
	}
	// RELEASE_FETCH_ALLOW 让发布下载能读内网文件服务（例如 mesh 上的包目录）；
	// 留空就是「只允许公网地址」。
	fetchAllow, err := httpx.ParsePrefixes(os.Getenv("RELEASE_FETCH_ALLOW"))
	if err != nil {
		return environment{}, err
	}
	// 凭据签发通道：CLI 由镜像提供，RPC 地址是宿主上转发到种子回环的入口。
	credentialCLI := strings.TrimSpace(os.Getenv("ET_CLI"))
	credentialRPC := strings.TrimSpace(os.Getenv("ET_RPC_ADDR"))
	if credentialCLI != "" && credentialRPC == "" {
		return environment{}, fmt.Errorf("ET_RPC_ADDR is required when ET_CLI is set")
	}
	readyzToken := os.Getenv("READYZ_TOKEN")
	// Secrets leave the environment table as soon as they are in memory.
	_ = os.Unsetenv("READYZ_TOKEN")
	if readyzToken != "" && !validSecret(readyzToken) {
		return environment{}, fmt.Errorf("READYZ_TOKEN must match ^[A-Za-z0-9_]{16,128}$")
	}
	// AUTH_TOKEN is the only admin credential; it is required and hashed before
	// anything else starts, so the raw value never outlives startup.
	authToken := os.Getenv("AUTH_TOKEN")
	_ = os.Unsetenv("AUTH_TOKEN")
	if !validSecret(authToken) {
		return environment{}, fmt.Errorf("AUTH_TOKEN must match ^[A-Za-z0-9_]{16,128}$")
	}
	tokenHash, err := core.NewTokenHash(authToken)
	if err != nil {
		return environment{}, fmt.Errorf("hash AUTH_TOKEN: %w", err)
	}
	// An absent signing key is generated below; rotating it revokes every
	// credential ever issued.
	sessionKey := strings.TrimSpace(os.Getenv("SESSION_SIGNING_KEY"))
	_ = os.Unsetenv("SESSION_SIGNING_KEY")
	if sessionKey != "" && !validSessionKey(sessionKey) {
		return environment{}, fmt.Errorf("SESSION_SIGNING_KEY must match ^[A-Za-z0-9_]{32,128}$")
	}
	maxAuth, err := positiveIntEnv("MAX_AUTH_CONCURRENCY", 32)
	if err != nil {
		return environment{}, err
	}
	maxAPI, err := positiveIntEnv("MAX_API_CONCURRENCY", 128)
	if err != nil {
		return environment{}, err
	}
	maxStatic, err := positiveIntEnv("MAX_STATIC_CONCURRENCY", 64)
	if err != nil {
		return environment{}, err
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(memoryLimit)
	}
	return environment{
		dataDir:        dataDir,
		host:           host,
		port:           port,
		trustedProxies: proxies,
		fetchAllow:     fetchAllow,
		readyzToken:    readyzToken,
		tokenHash:      tokenHash,
		sessionKey:     []byte(sessionKey),
		maxAuth:        maxAuth,
		maxAPI:         maxAPI,
		maxStatic:      maxStatic,
		credentialCLI:  credentialCLI,
		credentialRPC:  credentialRPC,
	}, nil
}

func positiveIntEnv(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, raw)
	}
	return value, nil
}

func validSecret(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// validSessionKey accepts the longer alphabet-only key used for signing.
func validSessionKey(value string) bool {
	return len(value) >= 32 && validSecret(value)
}

func loadTimezone(name string) (*time.Location, error) {
	if name == "" {
		name = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("TZ=%q is not a valid timezone: %w", name, err)
	}
	return loc, nil
}

func newLogger(w io.Writer, loc *time.Location) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key != slog.TimeKey {
				return attr
			}
			if t, ok := attr.Value.Any().(time.Time); ok {
				return slog.String("timestamp", t.In(loc).Format(time.RFC3339))
			}
			return attr
		},
	})
	return slog.New(handler)
}

func purgeTokens(ctx context.Context, application *app.App, logger *slog.Logger) {
	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := application.PurgeExpiredTokens(); err != nil {
				logger.Error("purge expired registration tokens", "error", err)
			}
		}
	}
}

// browseURL turns a listen address into a URL a human can open.
func browseURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
