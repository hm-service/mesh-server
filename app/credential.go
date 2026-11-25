package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"mesh-server/db"
)

// credentialIssuer 调种子的 management RPC 签发与撤销凭据。
// 私钥只在签发结果里出现一次：不落库、不写日志。
type credentialIssuer struct {
	cliPath string
	rpcAddr string
}

// issuedCredential 是种子返回的一张凭据。
type issuedCredential struct {
	ID     string
	Secret string
	Expiry int64
}

// credentialCLITimeout 是单次 cli 调用的上限。签发要等种子生成密钥，留够余量。
const credentialCLITimeout = 15 * time.Second

// credentialCLIOutputLimit 是单次 cli 输出的上限：答案只有几百字节，超出就是
// 二进制不对，没有理由把它整个读进内存。
const credentialCLIOutputLimit = 64 << 10

// boundedBuffer 只留前 limit 字节，其余丢弃；返回值仍报完整长度，子进程不会
// 因为短写而报错。
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// generate 让种子签发一张新凭据。
// credential_id 用 uuid4：种子对同一个 id 是幂等的，续期必须换 id 才会换密钥。
func (c *credentialIssuer) generate(ctx context.Context, ttl time.Duration) (issuedCredential, error) {
	id, err := newCredentialID()
	if err != nil {
		return issuedCredential{}, err
	}
	out, err := c.run(ctx, "-o", "json", "credential", "generate",
		"--ttl", strconv.Itoa(int(ttl.Seconds())), "--credential-id", id)
	if err != nil {
		return issuedCredential{}, err
	}
	var raw struct {
		ID     string `json:"credential_id"`
		Secret string `json:"credential_secret"`
		Expiry string `json:"expiry_unix"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return issuedCredential{}, fmt.Errorf("解析签发结果失败: %w", err)
	}
	expiry, err := strconv.ParseInt(strings.TrimSpace(raw.Expiry), 10, 64)
	if err != nil || raw.Secret == "" {
		return issuedCredential{}, fmt.Errorf("签发结果缺少密钥或到期时间")
	}
	return issuedCredential{ID: raw.ID, Secret: raw.Secret, Expiry: expiry}, nil
}

// revoke 让种子撤掉一张凭据，全网会在几秒内断开使用它的节点。
func (c *credentialIssuer) revoke(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	_, err := c.run(ctx, "credential", "revoke", id)
	return err
}

// run 执行一次 cli 调用并返回 stdout。错误里带 stderr，但不带参数，
// 免得命令行上的密钥被写进日志。
func (c *credentialIssuer) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, credentialCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cliPath, append([]string{"-p", c.rpcAddr}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout := &boundedBuffer{limit: credentialCLIOutputLimit}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("easytier-cli 失败: %s", msg)
	}
	return stdout.Bytes(), nil
}

// newCredentialID 生成 uuid4。
func newCredentialID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// 凭据有效期与续期窗口：剩余不足 RenewBefore 时签发下一张，旧的自然过期。
const (
	CredentialTTL         = 30 * 24 * time.Hour
	CredentialRenewBefore = 7 * 24 * time.Hour
)

// CredentialNoneCooldown 是报 none 时的签发冷却：同一设备距最近一条凭据的
// 创建时间不足这么久就不再签发。正常恢复（重装、本地凭据丢失）第一轮就拿到
// 凭据；冷却只挡异常——并发心跳、credential.json 一直写不进去时，30 秒一轮
// 会无限续签。5 分钟把最坏情况压到几百张/天，恢复后最多等 5 分钟。
const CredentialNoneCooldown = 5 * time.Minute

// credentialRevokeTimeout 是一次撤销全部凭据的总上限：凭据张数没有上限，
// 逐张串行等种子的时间不能跟着设备走。
const credentialRevokeTimeout = 30 * time.Second

// ErrCredentialChannelUnconfigured 表示这台控制面没有配置签发通道（ET_CLI
// 为空），既签不出也撤不掉凭据。
var ErrCredentialChannelUnconfigured = errors.New("credential channel is not configured")

// CredentialConfigured 报告这台控制面是否配了签发通道。
func (a *App) CredentialConfigured() bool { return a.credential != nil }

// IssuedCredential 是一次签发的结果。私钥只在这里出现一次：不落库、不写日志。
type IssuedCredential struct {
	ID     string
	Secret string
	Expiry int64
}

// IssueCredential 给设备签发一张凭据，记下 id 与到期时间，返回这一次的完整
// 结果。响应只能用返回值里的 id：回头再查一次库在并发签发时会把一张凭据的
// 私钥配到另一张的 id 上。
func (a *App) IssueCredential(ctx context.Context, publicID string) (IssuedCredential, error) {
	if a.credential == nil {
		return IssuedCredential{}, ErrCredentialChannelUnconfigured
	}
	if _, ok, err := a.DeviceByID(publicID); err != nil {
		return IssuedCredential{}, err
	} else if !ok {
		return IssuedCredential{}, ErrNotFound
	}
	issued, err := a.credential.generate(ctx, CredentialTTL)
	if err != nil {
		return IssuedCredential{}, err
	}
	if err := a.database.CreateCredential(issued.ID, publicID, issued.Expiry, a.now().Unix()); err != nil {
		return IssuedCredential{}, err
	}
	return IssuedCredential{ID: issued.ID, Secret: issued.Secret, Expiry: issued.Expiry}, nil
}

// RevokeCredential 撤销设备的全部凭据，并清掉记录。设备名下没有凭据时什么
// 都不做；有记录却没有签发通道时明确失败，不能回答成功（记录留在库里，种子
// 侧那张凭据仍然有效）。
func (a *App) RevokeCredential(ctx context.Context, publicID string) error {
	creds, err := a.database.ListCredentials(publicID)
	if err != nil {
		return err
	}
	if len(creds) == 0 {
		return nil
	}
	if a.credential == nil {
		return ErrCredentialChannelUnconfigured
	}
	ctx, cancel := context.WithTimeout(ctx, credentialRevokeTimeout)
	defer cancel()
	for _, c := range creds {
		if err := a.credential.revoke(ctx, c.CredentialID); err != nil {
			return err
		}
		if err := a.database.DeleteCredential(c.CredentialID); err != nil {
			return err
		}
	}
	return nil
}

// ConfirmCredential 处理设备上报的凭据 id。只有它上报的正是最新签发的那张，
// 才说明设备已经换上新凭据并重启过 core，这时才可以吊销其余的。
func (a *App) ConfirmCredential(ctx context.Context, publicID, credentialID string) error {
	if a.credential == nil || credentialID == "" {
		return nil
	}
	latest, err := a.database.LatestCredential(publicID)
	if errors.Is(err, db.ErrCredentialNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if latest.CredentialID != credentialID {
		return nil
	}
	stale, err := a.database.ListCredentialsExcept(publicID, credentialID)
	if err != nil {
		return err
	}
	for _, id := range stale {
		if err := a.credential.revoke(ctx, id); err != nil {
			return err
		}
		if err := a.database.DeleteCredential(id); err != nil {
			return err
		}
	}
	return nil
}

// LatestCredential 返回设备最新签发的那张凭据。
func (a *App) LatestCredential(publicID string) (db.Credential, error) {
	return a.database.LatestCredential(publicID)
}
