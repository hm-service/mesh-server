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
	"mesh-server/text"
)

// credentialIssuer 调种子的 management RPC 签发与撤销凭据。
// 私钥只在签发结果里出现一次：不落库、不写日志。
type credentialIssuer struct {
	cliPath string
	rpcAddr string
}

// credentialCLITimeout 是单次 cli 调用的上限。签发要等种子生成密钥，留够余量。
const credentialCLITimeout = 15 * time.Second

// credentialCLIOutputLimit 是单次 cli 每条输出流各自的上限：答案只有几百字节，
// 超出就是二进制不对，没有理由把它整个读进内存，也没有理由只限 stdout——stderr
// 会跟着错误信息进日志。两条流各留一份，一次调用的内存上限因此是它的两倍。
const credentialCLIOutputLimit = 64 << 10

// credentialCLIPipeWait 是主进程结束（或被杀）之后等待管道关闭的上限。继承了
// 这两条管道的子进程可以让 Wait 一直不返回；没有这个上限，单次签发的总时限就
// 只约束了进程本身，不约束管道与清理。
const credentialCLIPipeWait = 2 * time.Second

// boundedBuffer 只留前 limit 字节，其余丢弃并记下截断；返回值仍报完整长度，
// 子进程不会因为短写而报错。截断必须让调用方看见：一份被剪短的输出与一段真
// 的短回答是两件事。
type boundedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	switch {
	case room <= 0:
		if len(p) > 0 {
			b.truncated = true
		}
	case len(p) > room:
		b.buf.Write(p[:room])
		b.truncated = true
	default:
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// generate 让种子签发 id 这张凭据。
// credential_id 用 uuid4：种子对同一个 id 是幂等的，续期必须换 id 才会换密钥。
// id 由调用方先定下来并落成意图行，种子那边多出来的凭据才总有本地记录可撤。
func (c *credentialIssuer) generate(ctx context.Context, id string, ttl time.Duration) (IssuedCredential, error) {
	out, err := c.run(ctx, "-o", "json", "credential", "generate",
		"--ttl", strconv.Itoa(int(ttl.Seconds())), "--credential-id", id)
	if err != nil {
		return IssuedCredential{}, err
	}
	var raw struct {
		ID     string `json:"credential_id"`
		Secret string `json:"credential_secret"`
		Expiry string `json:"expiry_unix"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return IssuedCredential{}, fmt.Errorf("%s：%w", text.Get(text.AppCredentialParseFailed), err)
	}
	expiry, err := strconv.ParseInt(strings.TrimSpace(raw.Expiry), 10, 64)
	if err != nil || raw.Secret == "" {
		return IssuedCredential{}, errors.New(text.Get(text.AppCredentialIncomplete))
	}
	return IssuedCredential{ID: raw.ID, Secret: raw.Secret, Expiry: expiry}, nil
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
// 免得命令行上的密钥被写进日志。两条输出流各自有界（各 credentialCLIOutputLimit
// 字节），截断状态随错误一起报出来；进程与管道都有时限，失败不会把一次签发拖成
// 没有上限的等待。
func (c *credentialIssuer) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, credentialCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cliPath, append([]string{"-p", c.rpcAddr}, args...)...)
	stdout := &boundedBuffer{limit: credentialCLIOutputLimit}
	stderr := &boundedBuffer{limit: credentialCLIOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// 主进程退出或被杀之后，管道可能还被它拉起的子进程持有：WaitDelay 到点就
	// 关掉管道，Wait 不会跟着那个子进程的寿命走。
	cmd.WaitDelay = credentialCLIPipeWait
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(string(stderr.Bytes()))
		if msg == "" {
			msg = err.Error()
		}
		if stderr.truncated {
			return nil, errors.New(text.Format(text.AppCLIFailedTruncated, msg))
		}
		return nil, errors.New(text.Format(text.AppCLIFailed, msg))
	}
	if stdout.truncated {
		// 剪短的 stdout 解析不出答案，也不是「答案不完整」：说清楚它超了预算。
		return nil, errors.New(text.Format(text.AppCLIOutputTooLarge, credentialCLIOutputLimit))
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

// ErrFormalNode 表示这台设备是正式节点：它用 network_secret 认证，凭据对它
// 没有意义。签了也永远用不上，只在种子的可信列表里留一张没人持有的凭据。
var ErrFormalNode = errors.New("device is a formal node")

// CredentialConfigured 报告这台控制面是否配了签发通道。
func (a *App) CredentialConfigured() bool { return a.credential != nil }

// IssuedCredential 是一次签发的结果。私钥只在这里出现一次：不落库、不写日志。
type IssuedCredential struct {
	ID     string
	Secret string
	Expiry int64
}

// CredentialNone 是客户端上报「本地没有凭据」的值（contract/device-api.md）。
const CredentialNone = "none"

// IssueCredential 给设备签发一张凭据，记下 id 与到期时间，返回这一次的完整
// 结果。响应只能用返回值里的 id：回头再查一次库在并发签发时会把一张凭据的
// 私钥配到另一张的 id 上。
func (a *App) IssueCredential(ctx context.Context, publicID string) (IssuedCredential, error) {
	if a.credential == nil {
		return IssuedCredential{}, ErrCredentialChannelUnconfigured
	}
	unlock := a.locks.lock(publicID)
	defer unlock()
	device, ok, err := a.DeviceByID(publicID)
	if err != nil {
		return IssuedCredential{}, err
	}
	if !ok {
		return IssuedCredential{}, ErrNotFound
	}
	return a.issueLocked(ctx, device)
}

// issueLocked 签发一张凭据，调用方必须持有该设备的锁。
//
// 顺序是「先记意图、再让种子签、最后补上到期时间」：种子那边多出来的凭据总有
// 本地记录，删除、换绑、确认都能按 id 撤掉；种子成功而落库失败也不再留下一张
// 谁都不知道的凭据。
func (a *App) issueLocked(ctx context.Context, device db.Device) (IssuedCredential, error) {
	// 正式节点用 network_secret 认证，凭据对它没有意义。配置读不出来时按错误停下：
	// 当成凭据节点会签出一张它用不上的凭据，还把它记成凭据身份。判据与控制台
	// 显示共用 ConfigFactsOf。
	facts, err := ConfigFactsOf(device.ConfigTOML)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("%s: %w", text.Get(text.AppConfigUnreadable), err)
	}
	if facts.Identity == IdentityFormal {
		return IssuedCredential{}, ErrFormalNode
	}
	id, err := newCredentialID()
	if err != nil {
		return IssuedCredential{}, err
	}
	if err := a.database.BeginCredential(id, device.PublicID, a.now().Unix()); err != nil {
		return IssuedCredential{}, err
	}
	issued, err := a.credential.generate(ctx, id, CredentialTTL)
	if err != nil {
		// 种子可能已经建了这张（超时，或者连接断在应答回来之前）。补一次撤销把它
		// 收干净：撤销对种子不认识的 id 也返回成功，所以这一步是幂等的。撤不掉就
		// 把意图行留着——删除设备或下次收敛时会再按 id 撤一次。
		if revokeErr := a.revokeIDs(ctx, id); revokeErr == nil {
			if deleteErr := a.database.DeleteCredential(id); deleteErr != nil {
				return IssuedCredential{}, fmt.Errorf("%w (cleanup: %v)", err, deleteErr)
			}
		}
		return IssuedCredential{}, err
	}
	if err := a.database.FinishCredential(id, issued.Expiry); err != nil {
		// 意图行写不进去：私钥不能交出去。那张凭据在种子上还在，但本地有记录，
		// 删除设备时会按 id 撤掉。
		return IssuedCredential{}, err
	}
	return issued, nil
}

// RevokeCredential 撤销设备的全部凭据（含只记了意图的行），并清掉记录。设备
// 名下没有凭据时什么都不做；有记录却没有签发通道时明确失败，不能回答成功
// （记录留在库里，种子侧那张凭据仍然有效）。
func (a *App) RevokeCredential(ctx context.Context, publicID string) error {
	unlock := a.locks.lock(publicID)
	defer unlock()
	return a.revokeDeviceLocked(ctx, publicID)
}

// revokeDeviceLocked 撤销一台设备名下的全部记录，调用方必须持有设备锁。
func (a *App) revokeDeviceLocked(ctx context.Context, publicID string) error {
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
	ids := make([]string, 0, len(creds))
	for _, c := range creds {
		ids = append(ids, c.CredentialID)
	}
	return a.revokeAll(ctx, ids)
}

// revokeExceptLocked 撤销除 keep 以外的全部记录，调用方必须持有设备锁。
func (a *App) revokeExceptLocked(ctx context.Context, publicID, keep string) error {
	stale, err := a.database.ListCredentialsExcept(publicID, keep)
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	if a.credential == nil {
		return ErrCredentialChannelUnconfigured
	}
	return a.revokeAll(ctx, stale)
}

// revokeAll 撤销一批凭据并删掉记录。凭据张数没有上限，逐张等种子不能让整批
// 跟着设备走，所以整批共用一个总时限。管理端删除设备与心跳里的确认走同一条
// 路径：两处各自算时限，口径迟早分叉，而心跳那一处还占着并发闸门的槽位。
//
// 撤销是幂等的：`easytier-cli credential revoke` 把「没有这张」当正常结果（种子
// 重启过、凭据自然过期、上一次撤销已经生效），所以重试与补撤都安全。
func (a *App) revokeAll(ctx context.Context, ids []string) error {
	ctx, cancel := context.WithTimeout(ctx, credentialRevokeTimeout)
	defer cancel()
	for _, id := range ids {
		if err := a.credential.revoke(ctx, id); err != nil {
			return err
		}
		if err := a.database.DeleteCredential(id); err != nil {
			return err
		}
	}
	return nil
}

// revokeIDs 只撤销、不动本地记录，用于签发失败后的补偿。
func (a *App) revokeIDs(ctx context.Context, ids ...string) error {
	ctx, cancel := context.WithTimeout(ctx, credentialRevokeTimeout)
	defer cancel()
	for _, id := range ids {
		if err := a.credential.revoke(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ConfirmCredential 处理设备上报的凭据 id。只有它上报的正是最新签发的那张，
// 才说明设备已经换上新凭据并重启过 core，这时才可以吊销其余的。
func (a *App) ConfirmCredential(ctx context.Context, publicID, credentialID string) error {
	if a.credential == nil || credentialID == "" || credentialID == CredentialNone {
		return nil
	}
	unlock := a.locks.lock(publicID)
	defer unlock()
	return a.confirmLocked(ctx, publicID, credentialID)
}

// confirmLocked 处理「报上来的就是最新那张」，调用方必须持有设备锁。
func (a *App) confirmLocked(ctx context.Context, publicID, credentialID string) error {
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
	return a.revokeExceptLocked(ctx, publicID, credentialID)
}

// credentialStepLocked 是心跳对一台设备凭据做的全部决定与动作：确认切换、按需
// 签发。调用方必须持有该设备的锁，device 就是锁内读到的当前行——绑定已在入口处
// 复核，这里不再重复判断身份。
//
// reportedID 是设备 core 里正在用的那张，签发条件按它分档：
//
//   - 与最新签发的一致：切换已经完成，只有进入续期窗口才续签。
//   - 报着旧 id：core 还没重启完，或者管理端刚签发了一张而设备还不知道。冷却外
//     补一张（管理端那条通道靠这一步落到设备上），冷却内不签。
//   - none：客户端声明本地没有凭据，冷却外签一张。
//   - 空串：正式节点（或还不认识这个字段的旧客户端），既不确认也不签发。
//
// 冷却复用 CredentialNoneCooldown：报 none 与报旧 id 都可能每轮心跳都命中，
// 没有它就会一直签。
func (a *App) credentialStepLocked(ctx context.Context, device db.Device, reportedID string) (*IssuedCredential, error) {
	if a.credential == nil {
		return nil, nil
	}
	if reportedID == "" {
		return nil, nil
	}
	if reportedID != CredentialNone {
		if err := a.confirmLocked(ctx, device.PublicID, reportedID); err != nil {
			return nil, err
		}
	}
	now := a.now().Unix()
	latest, err := a.database.LatestCredential(device.PublicID)
	switch {
	case errors.Is(err, db.ErrCredentialNotFound):
		// 这台设备从来就没有过凭据：签一张。
	case err != nil:
		return nil, err
	case reportedID == CredentialNone:
		if now-latest.CreatedAt < int64(CredentialNoneCooldown.Seconds()) {
			return nil, nil
		}
	case reportedID == latest.CredentialID:
		// 设备已经用上最新那张：只在续期窗口内才签下一张。
		if latest.ExpiresAt-now >= int64(CredentialRenewBefore.Seconds()) {
			return nil, nil
		}
	case now-latest.CreatedAt < int64(CredentialNoneCooldown.Seconds()):
		return nil, nil
	}
	issued, err := a.issueLocked(ctx, device)
	if err != nil {
		return nil, err
	}
	return &issued, nil
}

// LatestCredential 返回设备最新签发的那张凭据。
func (a *App) LatestCredential(publicID string) (db.Credential, error) {
	return a.database.LatestCredential(publicID)
}
