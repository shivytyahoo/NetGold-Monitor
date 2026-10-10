//go:build windows

package sqlengine

// Windows implementation of the SQL Server Express engine path
// (see express.go for the design): service detection via the
// Service Control Manager, the SSPI trial connection, and the
// elevated setup helper child (netgold -engine-express), which
// installs the NETGOLD instance through Microsoft's installer
// bootstrapper, pins its static TCP port in the registry, restarts
// the instance's service, stores the generated 'sa' password
// protected, and verifies the sign-in end to end. Installing a
// service and setting 'sa' at setup time involves no SSPI
// handshake anywhere — that is the point of this path.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"netgold/internal/legacyimport"
)

func init() {
	platFindExpress = findExpressService
	platProbeSSPI = probeSSPI
	platProvisionExpress = runExpressElevated
	RunExpressChild = runExpressChild
}

// ---- service detection ----

// findExpressService reports whether the NETGOLD instance's
// service exists. Querying the SCM is allowed for standard users,
// so the (unelevated) parent can use this for its status page.
func findExpressService() bool {
	m, err := mgr.ConnectRemote("")
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(expressServiceName(ExpressInstanceName))
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// ---- the SSPI trial connection ----

// probeSSPI makes one short Windows Authentication connection to
// the LocalDB instance pipe: the probe that decides whether this
// sign-in session could provision the LocalDB engine's sign-in at
// all (v3.0.1's failure was exactly this handshake).
func probeSSPI(pipePath string) error {
	cfg := legacyimport.ConnConfig{PipeName: pipePath, Database: "master", WindowsAuth: true}
	db, err := sql.Open("sqlserver", cfg.DSN())
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}

// ---- the elevated helper child ----

// runExpressElevated (parent side) starts the elevated
// -engine-express copy of this program and waits for it.
func runExpressElevated(dataDir, installerPath string, progress func(string)) error {
	return runElevatedChild(
		[]string{"-engine-express", "-data", dataDir, "-express-installer", installerPath},
		fmt.Sprintf(`-engine-express -data "%s" -express-installer "%s"`, dataDir, installerPath),
		dataDir, ExpressProgressFile, ExpressResultFile, "SQL Server Express setup", progress)
}

// runExpressChild (child side) performs the Express provisioning.
// This process was started with administrator approval from the
// user's desktop session. Its verdict is written to the result
// file; the error is also returned so main can exit non-zero.
func runExpressChild(dataDir, installerPath string) error {
	if dataDir == "" || installerPath == "" {
		return fmt.Errorf("the Express setup was started without its data directory or installer path")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	pf, _ := os.Create(filepath.Join(dataDir, ExpressProgressFile))
	rep := &repairReporter{file: pf}
	if pf != nil {
		defer pf.Close()
	}
	err := doExpress(dataDir, installerPath, rep)
	result := "OK\nThe SQL Server Express engine was set up."
	if err != nil {
		result = "FAILED\n" + err.Error()
	}
	_ = os.WriteFile(filepath.Join(dataDir, ExpressResultFile), []byte(result), 0o644)
	return err
}

// doExpress executes the provisioning in the child. Idempotent: an
// existing NETGOLD instance whose stored sign-in still works is
// verified and re-configured, not reinstalled.
func doExpress(dataDir, installerPath string, rep *repairReporter) error {
	// Idempotent re-entry: the instance exists and the stored 'sa'
	// sign-in still works — just make sure the listener settings
	// and the service state are what imports expect.
	if findExpressService() {
		rep.step("A NetGold SQL Server Express instance already exists — verifying it…")
		user, pw, kind, ok := LoadStoredEngineLogin(dataDir)
		if !ok || kind != EngineKindExpress {
			return fmt.Errorf("a SQL Server Express instance named %s is installed, but the engine sign-in NetGold stored for it is missing, so NetGold cannot use it. Uninstall the instance (its name is %s) from Windows Settings > Apps, then run this setup again", ExpressInstanceName, ExpressInstanceName)
		}
		if err := applyExpressTCPConfig(); err != nil {
			return err
		}
		if err := setExpressServiceState(true); err != nil {
			return err
		}
		if err := platPingConn(expressConnConfig(user, pw)); err != nil {
			return fmt.Errorf("the existing SQL Server Express instance %s is installed and running, but the sign-in NetGold stored for it no longer works: %v", ExpressInstanceName, err)
		}
		rep.step("The existing SQL Server Express engine is ready.")
		return nil
	}

	// Fresh install. The 'sa' password is generated here, used once
	// as a setup parameter, and stored protected — it is never
	// written anywhere else.
	pw, err := GenerateEnginePassword()
	if err != nil {
		return err
	}
	commonAppData := os.Getenv("ProgramData")
	if commonAppData == "" {
		commonAppData = `C:\ProgramData`
	}
	instDir := expressInstanceDir(commonAppData)
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		return fmt.Errorf("could not create the engine folder %s: %w", instDir, err)
	}
	rep.step("Installing SQL Server Express — Microsoft's installer downloads the engine files itself, so this can take several minutes…")
	code, err := runExpressInstaller(installerPath, pw, instDir)
	if err != nil {
		return err
	}
	if fatal, msg := expressExitGuidance(code); fatal {
		return fmt.Errorf("%s", msg)
	} else if msg != "" {
		rep.step("%s", msg)
	}
	if !findExpressService() {
		return fmt.Errorf("the installer finished (exit code %d), but the %s service was not found afterwards — the installation did not complete", code, expressServiceName(ExpressInstanceName))
	}

	rep.step("Setting the engine's fixed network port…")
	if err := applyExpressTCPConfig(); err != nil {
		return err
	}
	rep.step("Restarting the engine service so the port takes effect…")
	if err := restartExpressService(); err != nil {
		return err
	}
	if err := SaveExpressLogin(dataDir, pw); err != nil {
		return err
	}

	// Verify the sign-in end to end, with patience: a freshly
	// installed engine can take a while to accept connections.
	rep.step("Verifying the engine sign-in…")
	cfg := expressConnConfig(ExpressLoginUser, pw)
	deadline := time.Now().Add(90 * time.Second)
	for {
		if err := platPingConn(cfg); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the SQL Server Express engine is installed, but NetGold could not sign in to it at 127.0.0.1:%d — restart the computer once, then run the import again", ExpressPort)
		}
		time.Sleep(3 * time.Second)
	}
	rep.step("SQL Server Express engine is ready.")
	return nil
}

// runExpressInstaller runs the verified bootstrapper quietly (this
// process is elevated, so it runs directly) and returns its exit
// code as an unsigned value — setup's failure codes are large
// unsigned numbers that Go reports as negative exit codes.
func runExpressInstaller(installerPath, saPassword, instDir string) (uint32, error) {
	cmd := exec.Command(installerPath, expressSSEIArgs(ExpressInstanceName, saPassword, instDir)...)
	cmd.SysProcAttr = hideWindowAttr()
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return uint32(ee.ExitCode()), nil
	}
	return 0xFFFFFFFF, fmt.Errorf("could not start the SQL Server Express installer: %w", err)
}

// ---- registry: instance ID + TCP configuration ----

// expressInstanceID reads the instance ID setup recorded for the
// NETGOLD instance (HKLM\SOFTWARE\Microsoft\Microsoft SQL Server\
// Instance Names\SQL, value "NETGOLD", e.g. "MSSQL17.NETGOLD").
func expressInstanceID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Microsoft SQL Server\Instance Names\SQL`,
		registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("could not read the installed SQL Server instances: %w", err)
	}
	defer k.Close()
	id, _, err := k.GetStringValue(ExpressInstanceName)
	if err != nil || id == "" {
		return "", fmt.Errorf("the %s instance is installed as a service, but its registry entry is missing — the installation looks incomplete", ExpressInstanceName)
	}
	return id, nil
}

// applyExpressTCPConfig writes the planned listener values (static
// port, dynamic ports off) under the instance's SuperSocketNetLib
// key. Writing the same values again is harmless, so this runs on
// both the install and the verify paths.
func applyExpressTCPConfig() error {
	id, err := expressInstanceID()
	if err != nil {
		return err
	}
	keyPath := superSocketNetLibKey(id)
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, keyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("could not open the engine's network settings (%s): %w", keyPath, err)
	}
	defer k.Close()
	for _, v := range expressTCPPlan(ExpressPort) {
		if err := k.SetStringValue(v.Name, v.Value); err != nil {
			return fmt.Errorf("could not set the engine's %s setting: %w", v.Name, err)
		}
	}
	return nil
}

// ---- service control (SCM) ----

// setExpressServiceState starts (wantRunning=true) or stops the
// NETGOLD instance's service and waits for the state to settle.
func setExpressServiceState(wantRunning bool) error {
	m, err := mgr.ConnectRemote("")
	if err != nil {
		return fmt.Errorf("could not reach the Windows service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(expressServiceName(ExpressInstanceName))
	if err != nil {
		return fmt.Errorf("could not open the %s service: %w", expressServiceName(ExpressInstanceName), err)
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("could not read the %s service state: %w", expressServiceName(ExpressInstanceName), err)
	}
	if wantRunning {
		if st.State == svc.Running {
			return nil
		}
		if err := s.Start(); err != nil {
			return fmt.Errorf("could not start the %s service: %w", expressServiceName(ExpressInstanceName), err)
		}
		return waitServiceState(s, svc.Running, 120*time.Second)
	}
	if st.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("could not stop the %s service: %w", expressServiceName(ExpressInstanceName), err)
	}
	return waitServiceState(s, svc.Stopped, 60*time.Second)
}

// restartExpressService stops then starts the instance's service
// (a listener change only takes effect across a restart).
func restartExpressService() error {
	if err := setExpressServiceState(false); err != nil {
		return err
	}
	return setExpressServiceState(true)
}

// waitServiceState polls the service until it reaches the wanted
// state or the timeout passes.
func waitServiceState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err == nil && st.State == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the %s service did not reach the expected state in time", expressServiceName(ExpressInstanceName))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
