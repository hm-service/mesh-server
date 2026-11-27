# text

控制面的文案清单：中文在 `lang/chinese.lang`，代码里只引用 `ids.go` 的常量，不写裸字符串。

```go
httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", text.Get(text.APIPlatformRequired))
fmt.Errorf("%s：%w", text.Format(text.AppDecompressFailed), err)
```

## 文件

| 文件 | 用途 |
| --- | --- |
| `lang/chinese.lang` | 运行时唯一读取的一份 |
| `lang/english.lang` | 完整英文翻译，目前只由测试校验键集与占位符 |
| `ids.go` | 清单里每个 id 的常量 |
| `text.go` | `Get` 与 `Format`：包初始化时解析一次，解析失败直接 panic |

## 清单格式

- 一行 `key = value`：按第一个 `=` 切分，两侧各自 trim，`#` 开头的行与空行忽略；UTF-8 无 BOM，LF 换行。
- key 是 `<包>.<类>.<标识>`：字符集 `[a-z0-9_.]`，三段点分。类取 `text`（响应里的 message）或 `error`（Go error 的文案），`log` 留给日志。
- value 里只有 `{0}`、`{1}`… 这些占位符，从 0 开始连续编号，不允许别的花括号；`Format` 用 `fmt.Sprint` 替换，要引号就在文案里写 `「{0}」`。
- 两份清单的键集与条目数必须相同；value 不为空，同一个 key 不出现两次。`parse` 与测试都会拦下来。
- 中文标点用全角（`：`、`，`、`（）`、`「」`），`--flag`、路径、占位符与英文单词原样保留。

## 运行时

- 只读中文：不做语言判断、不读环境变量、不看系统区域设置。
- `Get(id)` 查不到就原样返回 id；`Format(id, args...)` 实参不足时那个占位符原样留下（`debug` 构建里往 stderr 报一行），不 panic。
- 内嵌清单解析失败是构建缺陷，包初始化时当场 panic（进程起不来，而不是每条文案退化成 id）。

## 加一条

1. 在 `ids.go` 加常量，值就是 id。
2. 两份清单各加同一行，占位符编号一致。
3. 调用点写 `text.Get(...)` 或 `text.Format(...)`；错误用 `fmt.Errorf("%s：%w", text..., err)` 保留 `%w` 的包裹语义。
4. `cd server && go test ./text/`：键集、占位符、常量与条目的一一对应都在这里卡着。
