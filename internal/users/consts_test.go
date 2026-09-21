package users

import "testing"

// TestBootstrapAdminRegularUserID_Stable 守住 P0 #1 不变量:
//   任何人不允许修改该值,否则破坏所有 admin personal 凭据 owner 归属。
//
// 与 docs/功能修改plan-v6.2.md §5.8 invariant 一致。
func TestBootstrapAdminRegularUserID_Stable(t *testing.T) {
	want := "00000000-0000-0000-0000-000000000001"
	if BootstrapAdminRegularUserID != want {
		t.Fatalf("BootstrapAdminRegularUserID 已变更: got %q want %q", BootstrapAdminRegularUserID, want)
	}
}