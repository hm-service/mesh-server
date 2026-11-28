package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"mesh-server/core"
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

// credentialSecretBytes 是凭据私钥的长度：base64 的 32 字节 X25519 私钥
// （contract/device-api.md）。
const credentialSecretBytes = 32

// credentialIDMismatch 报告种子的回答不是这次请求的那一张：id 由控制面先定下并落成
// 意图行，回答里的 id 不同，就既不能用来完成意图行，也不能交给客户端。answered 可能
// 对应种子上一张真的凭据，补偿时要一并撤掉。
type credentialIDMismatch struct {
	requested string
	answered  string
}

func (e *credentialIDMismatch) Error() string {
	return fmt.Sprintf("签发结果里的凭据 id 是 %q，请求的是 %q", e.answered, e.requested)
}

// strayCredentialID 返回回答里那个与请求不同、形状可用的 id。撤销会把 id 原样放进
// CLI 的参数里：形状不对的值不配当参数，也一定不是种子建的凭据。
func strayCredentialID(err error) string {
	var mismatch *credentialIDMismatch
	if !errors.As(err, &mismatch) || !validCredentialID(mismatch.answered) {
		return ""
	}
	return mismatch.answered
}

// validCredentialID 只认 newCredentialID 生成的 uuid4 形状。
func validCredentialID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		switch i {
		case 8, 13, 18, 23:
			if id[i] != '-' {
				return false
			}
		default:
			c := id[i]
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return id[14] == '4' && strings.ContainsRune("89ab", rune(id[19]))
}

// generate 让种子签发 id 这张凭据，并核对回答确实属于这次请求。
// credential_id 用 uuid4：种子对同一个 id 是幂等的，续期必须换 id 才会换密钥。
// id 由调用方先定下来并落成意图行，种子那边多出来的凭据才总有本地记录可撤。
//
// 种子的回答是一个整体：id、私钥与到期时间必须与这次请求同源。任何一项对不上都说明
// 种子侧、控制面记录与将要交给客户端的凭据会不同源（CLI 版本变化、异常输出、参数被
// 忽略），那不是可以继续的结果：按失败返回，由调用方走补偿。
func (c *credentialIssuer) generate(ctx context.Context, id string, ttl time.Duration, now time.Time) (IssuedCredential, error) {
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
	if raw.ID != id {
		return IssuedCredential{}, &credentialIDMismatch{requested: id, answered: raw.ID}
	}
	// 私钥必须是 base64 的 32 字节 X25519 私钥：形状不对的字符串交给客户端只会让
	// 它的 core 起不来，而那时控制面已经把这张凭据记成已签发了。
	secret, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw.Secret))
	if err != nil || len(secret) != credentialSecretBytes {
		return IssuedCredential{}, errors.New(text.Get(text.AppCredentialIncomplete))
	}
	expiry, err := strconv.ParseInt(strings.TrimSpace(raw.Expiry), 10, 64)
	if err != nil {
		return IssuedCredential{}, errors.New(text.Get(text.AppCredentialIncomplete))
	}
	// 有效期是请求的一部分：种子被要求签 ttl，回答只能落在这个期限附近。两端时钟
	// 允许 ClockSkew 的偏差；更短、更长或已经过期都不是这次请求的结果。
	want := now.Add(ttl)
	if expiry < want.Add(-core.ClockSkew).Unix() || expiry > want.Add(core.ClockSkew).Unix() {
		return IssuedCredential{}, fmt.Errorf("签发结果的有效期 %d 不在请求的 %d 附近（±%s）", expiry, want.Unix(), core.ClockSkew)
	}
	// 交出去的是校验过的那一份：重新编码成规范 base64，避免把等价但形状不同的文本
	// 连同断言一起漏过去。
	return IssuedCredential{ID: id, Secret: base64.StdEncoding.EncodeToString(secret), Expiry: expiry}, nil
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
	// Sealed 是同一条私钥封给设备的那一份（base64 的信封，见 seal.go）。它和
	// Secret 同进同出：客户端的优先级是「有封装的必须用封装的」。
	Sealed string
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
	unlock, err := a.locks.lock(ctx, publicID)
	if err != nil {
		return IssuedCredential{}, err
	}
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
	now := a.now()
	if err := a.database.BeginCredential(id, device.PublicID, now.Unix()); err != nil {
		return IssuedCredential{}, err
	}
	issued, err := a.credential.generate(ctx, id, CredentialTTL, now)
	if err != nil {
		// 种子可能已经建了这张（超时，或者连接断在应答回来之前）。ID 与请求不一致
		// 时那张建在回答的 id 下，一并撤掉，否则补偿够不着它。撤销对种子不认识的
		// id 也返回成功，所以这一步是幂等的。撤不掉就把意图行留着——删除设备或下次
		// 收敛时会再按 id 撤一次。
		ids := []string{id}
		if stray := strayCredentialID(err); stray != "" {
			ids = append(ids, stray)
		}
		if revokeErr := a.revokeIDs(ctx, ids...); revokeErr == nil {
			if deleteErr := a.database.DeleteCredential(id); deleteErr != nil {
				return IssuedCredential{}, fmt.Errorf("%w (cleanup: %v)", err, deleteErr)
			}
		}
		return IssuedCredential{}, err
	}
	// 封装在落定之前做：封不出来就不下发。设备公钥转不成 X25519（记录坏了、低阶点）
	// 时把这张凭据撤掉并删掉意图行，下一轮心跳重新签——留一张签好却交不出去的
	// 凭据只会让设备停在「最新那张」上，等到过期都拿不到。
	secret, err := base64.StdEncoding.DecodeString(issued.Secret)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("%w: %v", ErrSealRecipient, err)
	}
	sealed, err := sealToDevice(device.Pubkey, secret)
	if err != nil {
		if revokeErr := a.revokeIDs(ctx, id); revokeErr == nil {
			if deleteErr := a.database.DeleteCredential(id); deleteErr != nil {
				return IssuedCredential{}, fmt.Errorf("%w (cleanup: %v)", err, deleteErr)
			}
		}
		return IssuedCredential{}, err
	}
	issued.Sealed = sealed
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
	unlock, err := a.locks.lock(ctx, publicID)
	if err != nil {
		return err
	}
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
	unlock, err := a.locks.lock(ctx, publicID)
	if err != nil {
		return err
	}
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
//   - 报着库里还有的旧 id：core 还没重启完，或者管理端刚签发了一张而设备还不知道。
//     冷却外补一张（管理端那条通道靠这一步落到设备上），冷却内不签。
//   - 报着库里已经没有的 id：那张已经被撤销或删除，设备拿着一张种子不认的凭据
//     重连，永远等不到「切过去」那一刻。立即补签，不适用冷却。
//   - none：客户端声明本地没有凭据，冷却外签一张。
//   - 空串：正式节点（或还不认识这个字段的旧客户端），既不确认也不签发。
//
// 冷却复用 CredentialNoneCooldown：报 none 与报旧 id 都可能每轮心跳都命中，
// 没有它就会一直签。报着一张已删除的 id 同样每轮都命中，但客户端把新凭据落盘
// 之后就改报新的那张（core 还没起来时报空串），不需要冷却兜住；冷却反而会把
// 「凭据被删掉」拖成五分钟的掉线。代价是客户端一直写不进 credential.json 时
// 每轮都会签一张，这与「报 none 每轮都命中」是同一个坏客户端，只是不受冷却限制。
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
	noLatest := errors.Is(err, db.ErrCredentialNotFound)
	if err != nil && !noLatest {
		return nil, err
	}
	// 设备报的那张还在不在库里。只有在它既不是 none 也不是最新时才需要问：其余
	// 两种情形的新旧关系已经决定了下文。
	deleted := false
	if !noLatest && reportedID != CredentialNone && reportedID != latest.CredentialID {
		if _, err := a.database.CredentialByID(device.PublicID, reportedID); err != nil {
			if !errors.Is(err, db.ErrCredentialNotFound) {
				return nil, err
			}
			deleted = true
		}
	}
	switch {
	case noLatest, deleted:
		// 这台设备没有凭据，或在用的那张已经被撤销/删除：签一张。
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
		// 报着库里还有的旧 id：core 正在切换，冷却内等它。
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
