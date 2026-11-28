package text

// 语言清单里的 id。调用点只引用这里的常量，不写裸字符串。
//
// 类 text 是响应里的 message，类 error 是 Go error 的文案；名字去掉包名前缀，
// 因为 id 的第一段已经说明了包。
const (
	APIFormalNodeCredential          = "api.text.formal_node_credential"
	APICredentialChannelUnconfigured = "api.text.credential_channel_unconfigured"
	APIDeviceKeyMissing              = "api.text.device_key_missing"
	APIAppNotesRequiresURL           = "api.text.app_notes_requires_url"
	APIAutoUpdateRequiresURL         = "api.text.auto_update_requires_url"
	APISignatureRequiresURL          = "api.text.signature_requires_url"
	APILauncherSlotRequiresBoth      = "api.text.launcher_slot_requires_both"
	APIPlatformRequired              = "api.text.platform_required"
	APISlotOnlyLauncher              = "api.text.slot_only_launcher"

	AppPlatformInvalid           = "app.text.platform_invalid"
	AppNotesTooLong              = "app.text.notes_too_long"
	AppClientFileUnreadable      = "app.text.client_file_unreadable"
	AppConfigUnreadable          = "app.text.config_unreadable"
	AppPackageManifestInvalid    = "app.text.package_manifest_invalid"
	AppPackageTooLarge           = "app.text.package_too_large"
	AppPackageTooManyEntries     = "app.text.package_too_many_entries"
	AppPackageManifestMismatch   = "app.text.package_manifest_mismatch"
	AppPackageManifestUnsigned   = "app.text.package_manifest_unsigned"
	AppLauncherFileUnreadable    = "app.text.launcher_file_unreadable"
	AppSlotVersionRequired       = "app.text.slot_version_required"
	AppSlotVersionInvalid        = "app.text.slot_version_invalid"
	AppSlotURLRequired           = "app.text.slot_url_required"
	AppSlotURLTooLong            = "app.text.slot_url_too_long"
	AppSlotURLInvalid            = "app.text.slot_url_invalid"
	AppReleaseSignatureMissing   = "app.text.release_signature_missing"
	AppReleaseSignatureInvalid   = "app.text.release_signature_invalid"
	AppReleaseSignatureMalformed = "app.text.release_signature_malformed"

	AppCredentialParseFailed = "app.error.credential_parse_failed"
	AppCredentialIncomplete  = "app.error.credential_incomplete"
	AppCLIFailed             = "app.error.cli_failed"
	AppCLIFailedTruncated    = "app.error.cli_failed_truncated"
	AppCLIOutputTooLarge     = "app.error.cli_output_too_large"
	AppRedirectLimit         = "app.error.redirect_limit"
	AppDialAddressInvalid    = "app.error.dial_address_invalid"
	AppDialAddressBlocked    = "app.error.dial_address_blocked"
	AppFileTooLarge          = "app.error.file_too_large"
	AppDecompressFailed      = "app.error.decompress_failed"
	AppManifestMissing       = "app.error.manifest_missing"
	AppPackageReadFailed     = "app.error.package_read_failed"
	AppManifestReadFailed    = "app.error.manifest_read_failed"
	AppManifestTooLarge      = "app.error.manifest_too_large"
	AppManifestParseFailed   = "app.error.manifest_parse_failed"
	AppPackageFileMissing    = "app.error.package_file_missing"
	AppPackageFileUnexpected = "app.error.package_file_unexpected"
	AppPackageFileMismatch   = "app.error.package_file_mismatch"
	AppPackageFileDuplicate  = "app.error.package_file_duplicate"
	AppPackageFileNotRegular = "app.error.package_file_not_regular"

	HTTPXAddressInvalid = "httpx.error.address_invalid"
)
