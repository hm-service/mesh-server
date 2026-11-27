package text

// 语言清单里的 id。调用点只引用这里的常量，不写裸字符串。
//
// 类 text 是响应里的 message，类 error 是 Go error 的文案；名字去掉包名前缀，
// 因为 id 的第一段已经说明了包。
const (
	APIAutoUpdatePerDevice           = "api.text.auto_update_per_device"
	APIFormalNodeCredential          = "api.text.formal_node_credential"
	APICredentialChannelUnconfigured = "api.text.credential_channel_unconfigured"
	APIInvalidRequestBody            = "api.text.invalid_request_body"
	APIAppVersionNotInput            = "api.text.app_version_not_input"
	APIAppNotesRequiresURL           = "api.text.app_notes_requires_url"
	APIAutoUpdateRequiresURL         = "api.text.auto_update_requires_url"
	APILauncherSlotRequiresBoth      = "api.text.launcher_slot_requires_both"
	APIPlatformRequired              = "api.text.platform_required"
	APISlotOnlyLauncher              = "api.text.slot_only_launcher"

	AppPlatformInvalid        = "app.text.platform_invalid"
	AppNotesTooLong           = "app.text.notes_too_long"
	AppClientFileUnreadable   = "app.text.client_file_unreadable"
	AppPackageVersionInvalid  = "app.text.package_version_invalid"
	AppLauncherFileUnreadable = "app.text.launcher_file_unreadable"
	AppSlotVersionRequired    = "app.text.slot_version_required"
	AppSlotVersionTooLong     = "app.text.slot_version_too_long"
	AppSlotURLRequired        = "app.text.slot_url_required"
	AppSlotURLTooLong         = "app.text.slot_url_too_long"
	AppSlotURLInvalid         = "app.text.slot_url_invalid"

	AppCredentialParseFailed  = "app.error.credential_parse_failed"
	AppCredentialIncomplete   = "app.error.credential_incomplete"
	AppCLIFailed              = "app.error.cli_failed"
	AppRedirectLimit          = "app.error.redirect_limit"
	AppDialAddressInvalid     = "app.error.dial_address_invalid"
	AppDialAddressBlocked     = "app.error.dial_address_blocked"
	AppFileTooLarge           = "app.error.file_too_large"
	AppDecompressFailed       = "app.error.decompress_failed"
	AppManifestMissing        = "app.error.manifest_missing"
	AppPackageReadFailed      = "app.error.package_read_failed"
	AppManifestReadFailed     = "app.error.manifest_read_failed"
	AppManifestTooLarge       = "app.error.manifest_too_large"
	AppManifestParseFailed    = "app.error.manifest_parse_failed"
	AppManifestVersionMissing = "app.error.manifest_version_missing"

	HTTPXAddressInvalid = "httpx.error.address_invalid"
)
