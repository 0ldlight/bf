//go:build unix

package process

import (
	"os"
	"testing"
)

// 特殊权限位不能被 Perm() 掩码隐藏。
// Special mode bits must not disappear behind the Perm() mask.
func TestProcessPrivateModeSpecialBits(t *testing.T) {
	for _, bit := range []os.FileMode{os.ModeSetuid, os.ModeSetgid, os.ModeSticky} {
		if validPrivateMode(0o600 | bit) {
			t.Errorf("special mode accepted: %v", bit)
		}
	}
	if !validPrivateMode(0o400) || !validPrivateMode(0o600) {
		t.Error("valid mode rejected")
	}
	if validPrivateMode(0o644) {
		t.Error("world-readable mode accepted")
	}
}
