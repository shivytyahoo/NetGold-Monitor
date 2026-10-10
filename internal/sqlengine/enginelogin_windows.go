//go:build windows

package sqlengine

// Windows implementation of the engine's dedicated SQL login (see
// enginelogin.go for the design): DPAPI protection for the stored
// password, in-process provisioning over the instance pipe, an
// icacls pass to tighten the stored file's ACL, and the elevated
// helper child (-engine-provision) used when the calling process's
// own sign-in session cannot authenticate (the SSPI 8009030e case).
//
// The child mirrors the engine repair's contract: progress lines
// and an OK/FAILED result through files in the data directory, one
// UAC consent, hidden window.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"netgold/internal/legacyimport"
)

func init() {
	platProtectSecret = dpapiProtect
	platUnprotectSecret = dpapiUnprotect
	platProvisionLoginHere = provisionLoginHere
	platRunProvisionChild = runProvisionElevated
	platTightenLoginFile = tightenLoginFileACL
	RunProvisionChild = runProvisionChild
}

// ---- DPAPI (crypt32) ----

var (
	crypt32DLL             = windows.NewLazyDLL("crypt32.dll")
	procCryptProtectData   = crypt32DLL.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32DLL.NewProc("CryptUnprotectData")
	kernel32DLL            = windows.NewLazyDLL("kernel32.dll")
	procLocalFree          = kernel32DLL.NewProc("LocalFree")
)

// cryptProtectUForbidden keeps DPAPI from ever showing UI.
const cryptProtectUForbidden = 0x01

// dataBlob mirrors the Win32 DATA_BLOB structure.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func blobOf(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, int(b.cbData)))
	return out
}

func (b dataBlob) free() {
	if b.pbData != nil {
		procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	}
}

// dpapiProtect encrypts data with DPAPI in the CURRENT USER's scope
// (the default): only processes running as this Windows user can
// decrypt it. That is what protects the stored engine password —
// copying the file to another user or machine yields nothing.
func dpapiProtect(data []byte) ([]byte, error) {
	in := blobOf(data)
	var out dataBlob
	r1, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		cryptProtectUForbidden, uintptr(unsafe.Pointer(&out)))
	if r1 == 0 {
		return nil, fmt.Errorf("Windows could not protect the value (DPAPI): %v", callErr)
	}
	defer out.free()
	return out.bytes(), nil
}

// dpapiUnprotect decrypts what dpapiProtect produced, in the same
// user's session.
func dpapiUnprotect(data []byte) ([]byte, error) {
	in := blobOf(data)
	var out dataBlob
	r1, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		cryptProtectUForbidden, uintptr(unsafe.Pointer(&out)))
	if r1 == 0 {
		return nil, fmt.Errorf("Windows could not unprotect the value (DPAPI): %v", callErr)
	}
	defer out.free()
	return out.bytes(), nil
}

// ---- provisioning in this process ----

// provisionLoginHere creates/updates the engine login on the
// instance at pipePath. The connection signs in with Windows
// Authentication from this process — the same user context that
// created the instance with SqlLocalDB.exe, whose account is the
// instance's administrator. When this session has no usable
// credentials, the returned error is the SSPI class and the caller
// escalates to the helper child.
func provisionLoginHere(pipePath, user, password string) error {
	cfg := legacyimport.ConnConfig{PipeName: pipePath, Database: "master", WindowsAuth: true}
	src, err := legacyimport.Connect(cfg)
	if err != nil {
		return err
	}
	defer src.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := src.DB.ExecContext(ctx, provisionLoginSQL(user, password)); err != nil {
		return fmt.Errorf("creating the engine sign-in on the local engine: %v", err)
	}
	return nil
}

// ---- stored-file ACL ----

// tightenLoginFileACL restricts the login file to the current user
// (plus SYSTEM and Administrators) via icacls. Best effort: the
// file already sits in the user's own data folder and its content
// is DPAPI-protected, so a failure here is not fatal.
func tightenLoginFileACL(path string) {
	tok, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil || u.User.Sid == nil {
		return
	}
	cmd := exec.Command("icacls.exe", icaclsGrantArgs(path, u.User.Sid.String())...)
	cmd.SysProcAttr = hideWindowAttr()
	_ = cmd.Run()
}

// ---- the helper child ----

// runProvisionElevated (parent side) starts the elevated
// -engine-provision copy of this program and waits for it.
func runProvisionElevated(dataDir string, progress func(string)) error {
	return runElevatedChild(
		[]string{"-engine-provision", "-data", dataDir},
		fmt.Sprintf(`-engine-provision -data "%s"`, dataDir),
		dataDir, ProvisionProgressFile, ProvisionResultFile, "sign-in helper", progress)
}

// runProvisionChild (child side) performs the provisioning. This
// process was started with administrator approval from the user's
// desktop session. Its verdict is written to the result file; the
// error is also returned so main can exit non-zero.
func runProvisionChild(dataDir string) error {
	if dataDir == "" {
		return fmt.Errorf("the sign-in helper was started without its data directory")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	pf, _ := os.Create(filepath.Join(dataDir, ProvisionProgressFile))
	rep := &repairReporter{file: pf}
	if pf != nil {
		defer pf.Close()
	}
	err := doProvision(dataDir, rep)
	result := "OK\nThe engine sign-in was prepared."
	if err != nil {
		result = "FAILED\n" + err.Error()
	}
	_ = os.WriteFile(filepath.Join(dataDir, ProvisionResultFile), []byte(result), 0o644)
	return err
}

// doProvision executes the provisioning in the child: make sure the
// instance runs, reuse the stored login when it still signs in,
// otherwise create a fresh login, store it protected, and verify
// the SQL sign-in end to end.
func doProvision(dataDir string, rep *repairReporter) error {
	exe, ok := findLocalDB()
	if !ok {
		return fmt.Errorf("LocalDB is not installed on this computer, so there is no engine to prepare a sign-in for")
	}
	rep.step("Starting the NetGold engine instance…")
	pipe, err := ensureInstance(exe)
	if err != nil {
		return err
	}
	pipePath := PipePathFromInfo(pipe)

	if user, pw, ok := LoadEngineLogin(dataDir); ok {
		cfg := legacyimport.ConnConfig{PipeName: pipePath, Database: "master", User: user, Password: pw}
		if platPingConn(cfg) == nil {
			rep.step("The stored engine sign-in still works — nothing to change.")
			return nil
		}
		rep.step("The stored engine sign-in no longer works — creating a fresh one…")
	} else {
		rep.step("Creating the engine's dedicated sign-in…")
	}
	pw, err := GenerateEnginePassword()
	if err != nil {
		return err
	}
	if err := provisionLoginHere(pipePath, EngineLoginUser, pw); err != nil {
		return err
	}
	if err := SaveEngineLogin(dataDir, EngineLoginUser, pw); err != nil {
		return err
	}
	cfg := legacyimport.ConnConfig{PipeName: pipePath, Database: "master", User: EngineLoginUser, Password: pw}
	if err := platPingConn(cfg); err != nil {
		return fmt.Errorf("the new engine sign-in could not be verified: %v", err)
	}
	rep.step("Engine sign-in ready.")
	return nil
}
