package config

import (
	"os"
	"testing"
	"time"
)

// TestLoadV62Defaults 验证 v6.2 阶段 14 新 env vars 默认值符合文档。
func TestLoadV62Defaults(t *testing.T) {
	// 清空所有 PIPEWRIGHT_* env vars(避免外部污染)。
	for _, k := range []string{
		"PIPEWRIGHT_DATA_DIR", "PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE",
		"PIPEWRIGHT_ENFORCE_BUILD_ENV", "PIPEWRIGHT_UI_ONLY",
		"PIPEWRIGHT_AUTO_CHECK_ON_START", "PIPEWRIGHT_CHECK_CONCURRENCY",
		"PIPEWRIGHT_CHECK_TIMEOUT_SECONDS", "PIPEWRIGHT_PULL_TIMEOUT_MULTIPLIER",
		"PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE",
	} {
		_ = os.Unsetenv(k)
	}
	c := Load()
	if c.DataDir != DefaultDataDir {
		t.Fatalf("DataDir default = %q, want %q", c.DataDir, DefaultDataDir)
	}
	if c.ConfigUploadMax != DefaultConfigUploadMax {
		t.Fatalf("ConfigUploadMax = %d, want %d", c.ConfigUploadMax, DefaultConfigUploadMax)
	}
	if !c.EnforceBuildEnv {
		t.Fatal("EnforceBuildEnv default 应为 true")
	}
	if !c.UIOnly {
		t.Fatal("UIOnly default 应为 true")
	}
	if !c.AutoCheckOnStart {
		t.Fatal("AutoCheckOnStart default 应为 true")
	}
	if c.CheckConcurrency != DefaultCheckConcurrency {
		t.Fatalf("CheckConcurrency = %d, want %d", c.CheckConcurrency, DefaultCheckConcurrency)
	}
	if c.CheckTimeout != time.Duration(DefaultCheckTimeoutSec)*time.Second {
		t.Fatalf("CheckTimeout = %v, want %ds", c.CheckTimeout, DefaultCheckTimeoutSec)
	}
	if c.PullTimeoutMultiply != DefaultPullTimeoutMult {
		t.Fatalf("PullTimeoutMultiply = %d, want %d", c.PullTimeoutMultiply, DefaultPullTimeoutMult)
	}
	if c.AllowUncheckedEnable {
		t.Fatal("AllowUncheckedEnable default 应为 false(紧急逃生,默认关闭)")
	}
}

// TestLoadV62Overrides 验证 env vars 覆盖生效。
func TestLoadV62Overrides(t *testing.T) {
	t.Setenv("PIPEWRIGHT_DATA_DIR", "/custom/data")
	t.Setenv("PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE", "5242880")
	t.Setenv("PIPEWRIGHT_ENFORCE_BUILD_ENV", "false")
	t.Setenv("PIPEWRIGHT_UI_ONLY", "false")
	t.Setenv("PIPEWRIGHT_AUTO_CHECK_ON_START", "false")
	t.Setenv("PIPEWRIGHT_CHECK_CONCURRENCY", "20")
	t.Setenv("PIPEWRIGHT_CHECK_TIMEOUT_SECONDS", "120")
	t.Setenv("PIPEWRIGHT_PULL_TIMEOUT_MULTIPLIER", "6")
	t.Setenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE", "true")

	c := Load()
	if c.DataDir != "/custom/data" {
		t.Fatalf("DataDir = %q", c.DataDir)
	}
	if c.ConfigUploadMax != 5242880 {
		t.Fatalf("ConfigUploadMax = %d", c.ConfigUploadMax)
	}
	if c.EnforceBuildEnv {
		t.Fatal("EnforceBuildEnv 应为 false")
	}
	if c.UIOnly {
		t.Fatal("UIOnly 应为 false")
	}
	if c.AutoCheckOnStart {
		t.Fatal("AutoCheckOnStart 应为 false")
	}
	if c.CheckConcurrency != 20 {
		t.Fatalf("CheckConcurrency = %d", c.CheckConcurrency)
	}
	if c.CheckTimeout != 120*time.Second {
		t.Fatalf("CheckTimeout = %v", c.CheckTimeout)
	}
	if c.PullTimeoutMultiply != 6 {
		t.Fatalf("PullTimeoutMultiply = %d", c.PullTimeoutMultiply)
	}
	if !c.AllowUncheckedEnable {
		t.Fatal("AllowUncheckedEnable 应为 true")
	}
}

// TestGetenvBool_AcceptsCommonTruthy 验证 getenvBool 接受各种 truthy 形式。
func TestGetenvBool_AcceptsCommonTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on", "TRUE", "Yes"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("__TEST_BOOL", v)
			if !getenvBool("__TEST_BOOL", false) {
				t.Fatalf("getenvBool(%q) = false, want true", v)
			}
		})
	}
}

// TestGetenvBool_FalsyValues 验证 getenvBool 接受各种 falsy 形式。
func TestGetenvBool_FalsyValues(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off", "FALSE"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("__TEST_BOOL", v)
			if getenvBool("__TEST_BOOL", true) {
				t.Fatalf("getenvBool(%q) = true, want false", v)
			}
		})
	}
}

// TestGetenvBool_InvalidFallsBack 验证无效值回退到 default。
func TestGetenvBool_InvalidFallsBack(t *testing.T) {
	t.Setenv("__TEST_BOOL", "garbage")
	if !getenvBool("__TEST_BOOL", true) {
		t.Fatal("garbage 应回退到 default=true")
	}
	if getenvBool("__TEST_BOOL", false) {
		t.Fatal("garbage 应回退到 default=false")
	}
}

// TestGetenvInt_InvalidFallsBack 验证无效 int 回退 default。
func TestGetenvInt_InvalidFallsBack(t *testing.T) {
	t.Setenv("__TEST_INT", "abc")
	if getenvInt("__TEST_INT", 42) != 42 {
		t.Fatal("invalid int 应回退 default=42")
	}
}
