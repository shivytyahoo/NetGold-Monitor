package sqlengine

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---- password generation ----

func TestGenerateEnginePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		pw, err := GenerateEnginePassword()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(pw) != enginePasswordLength {
			t.Errorf("password length = %d, want %d", len(pw), enginePasswordLength)
		}
		if !hasAllClasses(pw) {
			t.Errorf("password must cover every character class: %q", pw)
		}
		for _, ch := range pw {
			if !strings.ContainsRune(pwCharset, ch) {
				t.Errorf("password contains a character outside the safe set: %q in %q", ch, pw)
			}
		}
		if seen[pw] {
			t.Error("two generated passwords collided — the random source is broken")
		}
		seen[pw] = true
	}
	// A deterministic reader yields a valid password too.
	cycling := make([]byte, 4096)
	for i := range cycling {
		cycling[i] = byte(i)
	}
	pw, err := generateEnginePassword(bytes.NewReader(cycling))
	if err != nil || len(pw) != enginePasswordLength || !hasAllClasses(pw) {
		t.Errorf("deterministic reader: pw=%q err=%v", pw, err)
	}
	// A degenerate reader (only 'a' can ever be drawn) must error
	// out instead of returning a weak password or looping forever.
	if _, err := generateEnginePassword(bytes.NewReader(make([]byte, 1<<20))); err == nil {
		t.Error("degenerate random source must produce an error")
	}
	// A failing reader surfaces its error.
	if _, err := generateEnginePassword(strings.NewReader("")); err == nil {
		t.Error("empty random source must produce an error")
	}
	// The real generator draws from crypto/rand.
	if _, err := generateEnginePassword(rand.Reader); err != nil {
		t.Errorf("crypto/rand generation: %v", err)
	}
}

// ---- provisioning T-SQL ----

func TestProvisionLoginSQL(t *testing.T) {
	sql := provisionLoginSQL("netgold_import", "a'b;c")
	for _, want := range []string{
		"CREATE LOGIN [netgold_import]",
		"ALTER LOGIN [netgold_import] WITH PASSWORD",
		"ALTER LOGIN [netgold_import] ENABLE",
		"ALTER SERVER ROLE [sysadmin] ADD MEMBER [netgold_import]",
		"CHECK_POLICY = OFF",
		"N'a''b;c'", // the password literal is T-SQL-escaped
		"sys.server_principals",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("provisioning SQL must contain %q, got:\n%s", want, sql)
		}
	}
}

// ---- the protected login file ----

func TestPackUnpackEngineLogin(t *testing.T) {
	data, err := packEngineLogin("netgold_import", []byte{1, 2, 3, 250})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	user, blob, err := unpackEngineLogin(data)
	if err != nil || user != "netgold_import" || !bytes.Equal(blob, []byte{1, 2, 3, 250}) {
		t.Errorf("round trip: user=%q blob=%v err=%v", user, blob, err)
	}
	for _, bad := range []string{
		"not json",
		`{"version":2,"user":"x","protected":"AQI="}`, // unknown version
		`{"version":1,"user":"","protected":"AQI="}`,  // missing user
		`{"version":1,"user":"x","protected":""}`,     // missing blob
		`{"version":1,"user":"x","protected":"!!"}`,   // bad base64
	} {
		if _, _, err := unpackEngineLogin([]byte(bad)); err == nil {
			t.Errorf("unpack must reject %q", bad)
		}
	}
}

// swapSecretHooks replaces only the DPAPI hooks with the fake
// prefix scheme (used by tests that do not need the job fakes).
func swapSecretHooks(t *testing.T) {
	t.Helper()
	oldP, oldU, oldT := platProtectSecret, platUnprotectSecret, platTightenLoginFile
	platProtectSecret = func(b []byte) ([]byte, error) { return append([]byte("FAKEPROT:"), b...), nil }
	platUnprotectSecret = func(b []byte) ([]byte, error) {
		s := string(b)
		if !strings.HasPrefix(s, "FAKEPROT:") {
			return nil, fmt.Errorf("not a fake-protected blob")
		}
		return []byte(strings.TrimPrefix(s, "FAKEPROT:")), nil
	}
	platTightenLoginFile = func(string) {}
	t.Cleanup(func() { platProtectSecret, platUnprotectSecret, platTightenLoginFile = oldP, oldU, oldT })
}

func TestSaveLoadEngineLogin(t *testing.T) {
	swapSecretHooks(t)
	dir := t.TempDir()
	const pw = "S3cure!Passw0rd_xyz"
	if _, _, ok := LoadEngineLogin(dir); ok {
		t.Fatal("no login file yet — load must report not-ok")
	}
	if err := SaveEngineLogin(dir, EngineLoginUser, pw); err != nil {
		t.Fatalf("save: %v", err)
	}
	user, got, ok := LoadEngineLogin(dir)
	if !ok || user != EngineLoginUser || got != pw {
		t.Errorf("load round trip: user=%q pw=%q ok=%v", user, got, ok)
	}
	raw, err := os.ReadFile(filepath.Join(dir, EngineLoginFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pw) {
		t.Error("the stored file must never contain the plaintext password")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, EngineLoginFile))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("login file permissions = %o, want 600", fi.Mode().Perm())
		}
	}
	// A damaged file loads as not-ok (the caller re-provisions).
	if err := os.WriteFile(filepath.Join(dir, EngineLoginFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := LoadEngineLogin(dir); ok {
		t.Error("damaged login file must load as not-ok")
	}
	// An empty data dir never resolves to a file.
	if _, _, ok := LoadEngineLogin(""); ok {
		t.Error("empty data dir must load as not-ok")
	}
	if err := SaveEngineLogin("", EngineLoginUser, pw); err == nil {
		t.Error("saving without a data dir must fail")
	}
}

func TestIcaclsGrantArgs(t *testing.T) {
	args := icaclsGrantArgs(`C:\Data\engine-login.json`, "S-1-5-21-1234")
	joined := strings.Join(args, " ")
	for _, want := range []string{`C:\Data\engine-login.json`, "/inheritance:r",
		"*S-1-5-21-1234:F", "*S-1-5-18:F", "*S-1-5-32-544:F"} {
		if !strings.Contains(joined, want) {
			t.Errorf("icacls args must contain %q, got %v", want, args)
		}
	}
}

// ---- job integration: imports use the provisioned login ----

func TestJobImportsWithProvisionedLogin(t *testing.T) {
	f := &jobFakes{installed: true}
	swapPlatform(t, f)
	dir := t.TempDir()
	s := &Setup{DataDir: dir}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("want done, got step=%q error=%q", st.Step, st.Error)
	}
	if !f.provisionRan {
		t.Error("a fresh engine must get its dedicated login provisioned")
	}
	cfg := f.importCfg
	if cfg.User != EngineLoginUser || cfg.Password != f.provisionedPW || cfg.Password == "" {
		t.Errorf("import must use the provisioned login, got user=%q pw-match=%v", cfg.User, cfg.Password == f.provisionedPW)
	}
	if cfg.WindowsAuth {
		t.Error("import through our own engine must NOT use Windows Authentication (that is the SSPI dependency being removed)")
	}
	raw, err := os.ReadFile(filepath.Join(dir, EngineLoginFile))
	if err != nil {
		t.Fatalf("login file must be stored: %v", err)
	}
	if strings.Contains(string(raw), f.provisionedPW) {
		t.Error("the stored login file must not contain the plaintext password")
	}
	if !strings.Contains(st.Note, "dedicated SQL login") {
		t.Errorf("done note must explain the dedicated login: %q", st.Note)
	}
}

func TestJobReusesStoredLogin(t *testing.T) {
	f := &jobFakes{installed: true}
	swapPlatform(t, f)
	dir := t.TempDir()
	if err := SaveEngineLogin(dir, EngineLoginUser, "Stored!Passw0rd_1"); err != nil {
		t.Fatal(err)
	}
	s := &Setup{DataDir: dir}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("want done, got step=%q error=%q", st.Step, st.Error)
	}
	if f.provisionRan || f.provisionChildRan {
		t.Error("a stored login that still signs in must be reused, not re-provisioned")
	}
	if f.importCfg.Password != "Stored!Passw0rd_1" {
		t.Errorf("import must use the stored password, got a different one")
	}
}

func TestJobProvisionsViaChildOnSSPIFailure(t *testing.T) {
	f := &jobFakes{installed: true,
		provisionErr: fmt.Errorf("connecting to the local SQL engine: NextBytes InitializeSecurityContext failed 8009030e")}
	swapPlatform(t, f)
	dir := t.TempDir()
	s := &Setup{DataDir: dir}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("SSPI failure in this process must escalate to the helper and still finish, got step=%q error=%q", st.Step, st.Error)
	}
	if !f.provisionChildRan {
		t.Error("the helper child must be used after an SSPI-class provisioning failure")
	}
	if f.importCfg.User != EngineLoginUser || f.importCfg.Password != f.provisionedPW {
		t.Errorf("import must use the child-provisioned login, got user=%q", f.importCfg.User)
	}
}

func TestJobProvisionFailureIsDiagnosed(t *testing.T) {
	f := &jobFakes{installed: true,
		provisionErr: fmt.Errorf("creating the engine sign-in on the local engine: simulated T-SQL failure")}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepFailed || st.FailedStep != StepCreatingInstance {
		t.Fatalf("want failure at creating_instance, got step=%q failed=%q", st.Step, st.FailedStep)
	}
	if !strings.Contains(st.Error, "dedicated sign-in") || !strings.Contains(st.Error, "simulated T-SQL failure") {
		t.Errorf("error must name the provisioning failure and its cause: %q", st.Error)
	}
	if f.provisionChildRan {
		t.Error("a non-SSPI provisioning failure must not escalate to the helper child")
	}
}

// ---- stored sign-in engine kind (v3.0.2) ----

func TestStoredEngineKindRoundTrip(t *testing.T) {
	swapSecretHooks(t)
	dir := t.TempDir()

	// Express sign-in: kind recorded, user is 'sa', password exact.
	if err := SaveExpressLogin(dir, "Expr3ss!Passw0rd_9"); err != nil {
		t.Fatalf("save express login: %v", err)
	}
	user, pw, kind, ok := LoadStoredEngineLogin(dir)
	if !ok || user != ExpressLoginUser || pw != "Expr3ss!Passw0rd_9" || kind != EngineKindExpress {
		t.Errorf("express round trip: user=%q pw set=%v kind=%q ok=%v", user, pw != "", kind, ok)
	}
	// The plaintext password must not appear in the stored file.
	raw, err := os.ReadFile(filepath.Join(dir, EngineLoginFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Expr3ss!Passw0rd_9") {
		t.Error("the stored file must never contain the plaintext password")
	}

	// LocalDB sign-in (v3.0.1 behaviour): kind is localdb.
	if err := SaveEngineLogin(dir, EngineLoginUser, "Local!Passw0rd_9"); err != nil {
		t.Fatalf("save localdb login: %v", err)
	}
	user, pw, kind, ok = LoadStoredEngineLogin(dir)
	if !ok || user != EngineLoginUser || pw != "Local!Passw0rd_9" || kind != EngineKindLocalDB {
		t.Errorf("localdb round trip: user=%q kind=%q ok=%v", user, kind, ok)
	}

	// A pre-v3.0.2 file (no engine field) reads back as LocalDB.
	legacy, err := packEngineLogin(EngineLoginUser, []byte("FAKEPROT:legacy-pw"))
	if err != nil {
		t.Fatal(err)
	}
	u2, k2, blob, err := unpackEngineLoginKind(legacy)
	if err != nil || u2 != EngineLoginUser || k2 != EngineKindLocalDB || string(blob) != "FAKEPROT:legacy-pw" {
		t.Errorf("legacy envelope: user=%q kind=%q err=%v", u2, k2, err)
	}
}
