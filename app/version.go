package app

import "mesh-server/signing"

// ValidAppVersion reports whether s is a build label the control plane can
// order. Both the published version and the one a device reports come from the
// same build, so anything else is a protocol violation. The rule lives in
// package signing: the release tool and the control plane must not disagree
// about it.
func ValidAppVersion(s string) bool {
	return signing.ValidVersion(s)
}

// CompareVersions orders two build labels by (year, month, day, sequence), as
// signing.Compare does. ok is false when a label is outside that domain.
func CompareVersions(a, b string) (order int, ok bool) {
	return signing.Compare(a, b)
}

// EffectiveAutoUpdate 是三态覆盖的结果：设备自己定了就用设备的值，否则用发布包
// 的。nil 表示跟随（contract/admin-api.md「设备级自动更新」）。客户端只看它下发
// 的那个布尔值，不知道覆盖发生在哪一层。
func EffectiveAutoUpdate(device *bool, release bool) bool {
	if device != nil {
		return *device
	}
	return release
}

// NeedsUpdate reports whether a device running reported must be pointed at
// published. Only a reported version that is provably older counts: a device
// at or above the published build (a rolled-back release, a hand-installed
// package) is left alone, so auto_update cannot loop on the same old package.
// A device that has never reported a version counts as behind: it is an older
// client that predates the field, and it reports its version right after
// installing, so this cannot loop. A reported label that cannot be ordered is
// not evidence of an older build and is left alone (contract/updates.md).
func NeedsUpdate(reported, published string) bool {
	if published == "" {
		return false
	}
	if reported == "" {
		return true
	}
	order, ok := CompareVersions(reported, published)
	return ok && order < 0
}
