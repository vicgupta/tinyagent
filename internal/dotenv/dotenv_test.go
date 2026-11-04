package dotenv

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMissingFileIsFine(t *testing.T) {
	names, err := Load(t.TempDir())
	if err != nil || names != nil {
		t.Fatalf("Load(empty dir) = %v, %v; want nil, nil", names, err)
	}
}

func TestLoadRealEnvWins(t *testing.T) {
	dir := t.TempDir()
	body := "# comment\nexport DOTENV_T_A=from-file\nDOTENV_T_B=\"quoted \\\"x\\\"\"\nDOTENV_T_C='lit $X' \nDOTENV_T_D=plain # trailing\n\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOTENV_T_A", "from-env")
	for _, k := range []string{"DOTENV_T_B", "DOTENV_T_C", "DOTENV_T_D"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	names, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"DOTENV_T_B", "DOTENV_T_C", "DOTENV_T_D"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for k, want := range map[string]string{"DOTENV_T_A": "from-env", "DOTENV_T_B": `quoted "x"`, "DOTENV_T_C": "lit $X", "DOTENV_T_D": "plain"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadEmptyEnvStillWins(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("DOTENV_T_E=x\n"), 0o600)
	t.Setenv("DOTENV_T_E", "")
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("DOTENV_T_E"); got != "" {
		t.Fatalf("set-but-empty env was overridden: %q", got)
	}
}

func TestLoadBadLineNoValueInError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("not a pair secret123\n"), 0o600)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("want error")
	}
	if s := err.Error(); s != ".env:1: expected KEY=VALUE" {
		t.Fatalf("err = %q", s)
	}
}

func TestLoadLowercaseModelAlias(t *testing.T) {
	env := map[string]string{}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	set := func(k, v string) error { env[k] = v; return nil }
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte("model=codex/gpt-6-luna\n"), 0o600)
	names, err := load(p, lookup, set)
	if err != nil || !reflect.DeepEqual(names, []string{"AGENT_MODEL"}) || env["AGENT_MODEL"] != "codex/gpt-6-luna" {
		t.Fatalf("names=%v err=%v env=%v", names, err, env)
	}
	if _, ok := env["model"]; ok {
		t.Error("alias also set lowercase model")
	}
	// AGENT_MODEL in the file wins over the alias, in either order.
	env = map[string]string{}
	os.WriteFile(p, []byte("AGENT_MODEL=a/b\nmodel=c/d\n"), 0o600)
	load(p, lookup, set)
	if env["AGENT_MODEL"] != "a/b" {
		t.Errorf("AGENT_MODEL = %q, want a/b", env["AGENT_MODEL"])
	}
	// The alias is case-insensitive.
	env = map[string]string{}
	os.WriteFile(p, []byte("Model=e/f\n"), 0o600)
	load(p, lookup, set)
	if env["AGENT_MODEL"] != "e/f" {
		t.Errorf("Model= alias: AGENT_MODEL = %q", env["AGENT_MODEL"])
	}
	os.WriteFile(p, []byte("AGENT_MODEL=a/b\nmodel=c/d\n"), 0o600)
	// The real environment wins over the alias.
	env = map[string]string{"AGENT_MODEL": ""}
	load(p, lookup, set)
	if env["AGENT_MODEL"] != "" {
		t.Errorf("real empty AGENT_MODEL overridden: %q", env["AGENT_MODEL"])
	}
}
