package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDotEnvNoOverride(t *testing.T) {
	t.Setenv("CONFIG_TEST_EXISTING", "exported")
	p := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(p, []byte("CONFIG_TEST_EXISTING=file\nCONFIG_TEST_NEW='literal$secret'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("CONFIG_TEST_NEW")
	if err := LoadDotEnv(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CONFIG_TEST_EXISTING") != "exported" || os.Getenv("CONFIG_TEST_NEW") != "literal$secret" {
		t.Fatal("env semantics")
	}
}
func TestConfigDefaultsAndValidation(t *testing.T) {
	t.Setenv("APP_ID", "test")
	t.Setenv("APP_SECRET", "test-secret")
	c, e := Load()
	if e != nil || c.Generation.TopK != 50 || c.URL != "https://api-1b5.rwkvos.com/v1/batch/completions" {
		t.Fatal("defaults", e)
	}
	for _, v := range []string{"https://user:secret@example.com/v1", "https://example.com/?secret=value", "http://example.com/v1"} {
		t.Setenv("RWKV_API_URL", v)
		if _, e := Load(); e == nil {
			t.Fatal("invalid URL accepted")
		}
	}
	t.Setenv("RWKV_API_URL", "")
	t.Setenv("RWKV_TEMPERATURE", "NaN")
	if _, e := Load(); e == nil {
		t.Fatal("NaN accepted")
	}
}
