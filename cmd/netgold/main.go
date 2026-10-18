// NetGold Monitor — a lightweight, open-source network monitor.
// One binary: polling engine + SQLite storage + embedded web dashboard.
//
// Usage: netgold [-addr :8080] [-data <dir>] [-interval 60s] [-demo]
// Windows service control: netgold -service install|uninstall|start|stop|status
//
// Since v2.2.2 a newly started copy replaces an older running copy
// (single instance with takeover), and on Windows the program
// installs itself to %LOCALAPPDATA%\NetGold and runs from there, so
// there is only ever one NetGold on the computer. The data directory
// defaults to a NetGold folder in the user profile; a data folder
// left beside an older copy is carried over once, automatically.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"netgold/internal/flow"
	"netgold/internal/notify"
	"netgold/internal/poller"
	"netgold/internal/sqlengine"
	"netgold/internal/store"
	"netgold/internal/syslogsrv"
	"netgold/internal/web"
)

type config struct {
	addr           string
	dataDir        string
	interval       time.Duration
	demo           bool
	netflowPort    int
	sflowPort      int
	syslogPort     int
	noTakeover     bool
	canonicalChild bool
}

// hasOpFlag reports whether the argument list carries a command-line
// operation flag (-service, -version, -help/-h, -engine-repair,
// -engine-provision, -engine-express): an
// invocation that runs a command and exits, rather than starting the
// program. Pure function; recognises both "-flag value" and
// "-flag=value" spellings, with one or two leading dashes.
func hasOpFlag(args []string) bool {
	for _, a := range args {
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		switch name {
		case "service", "version", "help", "h", "engine-repair", "engine-provision", "engine-express":
			return true
		}
	}
	return false
}

func main() {
	var cfg config
	var serviceCmd string
	var showVersion, engineRepair, engineProvision, engineExpress bool
	var repairMSI, expressInstaller string
	// A Windows build of NetGold is a GUI-subsystem program (v3.0):
	// double-clicking it opens no console window at all. When the
	// invocation is a command (an operation flag is present), attach
	// to the parent console first so the command can still print its
	// result where the user can read it.
	opMode := hasOpFlag(os.Args[1:])
	if opMode {
		platformAttachConsole()
	}
	flag.BoolVar(&showVersion, "version", false, "print the program version and exit")
	flag.BoolVar(&engineRepair, "engine-repair", false, "internal: run the elevated SQL engine repair (started by NetGold itself with administrator approval)")
	flag.BoolVar(&engineProvision, "engine-provision", false, "internal: provision the SQL engine's dedicated sign-in (started by NetGold itself with administrator approval)")
	flag.BoolVar(&engineExpress, "engine-express", false, "internal: set up the SQL Server Express engine (started by NetGold itself with administrator approval)")
	flag.StringVar(&repairMSI, "repair-msi", "", "internal: installer file for -engine-repair")
	flag.StringVar(&expressInstaller, "express-installer", "", "internal: installer bootstrapper file for -engine-express")
	flag.StringVar(&cfg.addr, "addr", ":8080", "listen address")
	flag.StringVar(&cfg.dataDir, "data", "", "data directory (SQLite file lives here; default: a NetGold folder in your user profile)")
	flag.DurationVar(&cfg.interval, "interval", 60*time.Second, "poll interval")
	flag.BoolVar(&cfg.demo, "demo", false, "seed a synthetic demo device so graphs render immediately")
	flag.IntVar(&cfg.netflowPort, "netflow-port", 2055, "UDP port for the NetFlow/IPFIX collector (0 = off)")
	flag.IntVar(&cfg.sflowPort, "sflow-port", 6343, "UDP port for the sFlow collector (0 = off)")
	flag.IntVar(&cfg.syslogPort, "syslog-port", 5514, "UDP+TCP port for the syslog receiver (0 = off; 514 needs Administrator/root or service mode)")
	flag.StringVar(&serviceCmd, "service", "", "Windows service control: install|uninstall|start|stop|status")
	flag.BoolVar(&cfg.noTakeover, "no-takeover", false, "do not replace a running NetGold Monitor of a different version; just report it and exit")
	flag.BoolVar(&cfg.canonicalChild, "canonical-child", false, "internal: this process was relaunched from the canonical install location")
	flag.Parse()

	if showVersion {
		fmt.Printf("NetGold Monitor v%s — Developed by Shiv\n", web.Version)
		return
	}

	// Windowless GUI mode is a Windows interactive start with no
	// operation flag and no service control: no console exists.
	windowless := !opMode && platformGUIMode() && !runningAsWindowsService()

	// The very first output of every run: which version this copy
	// is, in plain sight, so a console window can never be mistaken
	// for a different (older) download. A canonical child skips it —
	// its parent already printed the banner on the same console.
	// (In windowless mode the banner is written to netgold.log
	// instead, once the data directory is known.)
	if !cfg.canonicalChild && !windowless {
		printBanner(cfg.addr)
	}
	setConsoleTitle()

	// Resolve the data directory. Without an explicit -data, the
	// data lives in the canonical per-user location (v2.2.2), and a
	// data folder left beside an older program copy is carried over
	// once. An explicit -data is honoured exactly as given.
	dataGiven := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "data" {
			dataGiven = true
		}
	})
	if !dataGiven {
		cfg.dataDir = defaultDataDir()
		migrateLegacyData(cfg.dataDir)
	}

	// The elevated engine-repair child (v3.0): NetGold relaunches
	// itself with -engine-repair after the user approves the Windows
	// administrator prompt; the repair itself lives in sqlengine and
	// reports through files in the data directory.
	if engineRepair {
		if err := sqlengine.RunRepairChild(cfg.dataDir, repairMSI); err != nil {
			log.Fatalf("engine repair: %v", err)
		}
		return
	}

	// The engine sign-in helper child (v3.0.1): like the repair
	// child, NetGold relaunches itself with -engine-provision after
	// the user approves the Windows administrator prompt — used when
	// this session cannot sign in to the engine itself (the SSPI
	// credential failure). It reports through the data directory.
	if engineProvision {
		if err := sqlengine.RunProvisionChild(cfg.dataDir); err != nil {
			log.Fatalf("engine provision: %v", err)
		}
		return
	}

	// The SQL Server Express setup child (v3.0.2): like the repair
	// child, NetGold relaunches itself with -engine-express after
	// the user approves the Windows administrator prompt; it
	// installs the NETGOLD Express instance (a service — no SSPI
	// involved) and reports through the data directory.
	if engineExpress {
		if err := sqlengine.RunExpressChild(cfg.dataDir, expressInstaller); err != nil {
			log.Fatalf("engine express setup: %v", err)
		}
		return
	}

	// Windowless GUI mode (Windows interactive start, v3.0): no
	// console exists, so from here on all program output — banner,
	// takeover notes, run log — goes to netgold.log in the data
	// directory instead of vanishing.
	guiMode := windowless
	if guiMode {
		setupGUILogging(cfg.dataDir)
		if !cfg.canonicalChild {
			printBanner(cfg.addr)
		}
	}

	if serviceCmd != "" {
		if err := handleServiceCommand(serviceCmd, cfg); err != nil {
			log.Fatalf("service %s: %v", serviceCmd, err)
		}
		return
	}

	// When the Windows SCM starts us, run under the service dispatcher.
	if runningAsWindowsService() {
		if err := runWindowsService(cfg); err != nil {
			log.Fatalf("service: %v", err)
		}
		return
	}

	// On Windows, install to the canonical location and continue
	// from there (the parent exits once the installed copy starts).
	if canonicalizeIfNeeded(cfg) {
		return
	}

	// One NetGold at a time: replace an older running copy, or
	// report the situation and exit (see instance.go).
	serve, exitCode := ensureSingleInstance(cfg)
	if !serve {
		if guiMode && exitCode == 0 {
			// Benign no-op in GUI mode (the same version is already
			// running, or the Windows service holds the port): just
			// open the running program in the browser.
			openBrowser(webURL(cfg.addr))
		}
		os.Exit(exitCode)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	var doneOnce sync.Once
	closeDone := func() { doneOnce.Do(func() { close(done) }) }
	go func() {
		<-stop
		closeDone()
	}()
	if guiMode {
		// The application runs in the background; the system tray
		// owns the main thread. The browser opens once the server is
		// listening, and choosing Exit in the tray menu closes done
		// for a graceful shutdown.
		appErr := make(chan error, 1)
		go func() { appErr <- run(cfg, done) }()
		go autoOpenBrowser(cfg.addr)
		startTray(webURL(cfg.addr), closeDone)
		if err := <-appErr; err != nil {
			log.Printf("NetGold Monitor stopped with an error: %v", err)
		}
		return
	}
	if err := run(cfg, done); err != nil {
		log.Fatal(err)
	}
}

// guiLogFile keeps the netgold.log handle referenced for the life of
// the process (an unreferenced os.File would be closed by the GC).
var guiLogFile *os.File

// setupGUILogging redirects the program's output to netgold.log in
// the data directory (appended across runs): in windowless GUI mode
// there is no console, so this file is where the banner, takeover
// decisions and the run log live. Best effort — if the file cannot
// be opened the program still runs, just silently.
func setupGUILogging(dataDir string) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "netgold.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	guiLogFile = f
	log.SetOutput(f)
	os.Stdout = f
	os.Stderr = f
}

// autoOpenBrowser waits until the web server answers, then opens
// the dashboard in the default browser (GUI mode's "double-click
// and it appears" behaviour). Gives up quietly after ~20 s.
func autoOpenBrowser(addr string) {
	url := webURL(addr)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/api/version")
		if err == nil {
			resp.Body.Close()
			// Any HTTP response means the server is up. Since v3.4
			// the API answers 401 until the user signs in, so the
			// status code itself must not gate opening the browser;
			// the browser lands on the login page.
			openBrowser(url)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// printBanner prints the startup banner: the program name, its
// exact version, the developer credit and the web address. The
// version comes from web.Version, the same constant the web pages
// and /api/version report, so what the console says and what the
// program is can never disagree.
func printBanner(addr string) {
	fmt.Println("============================================")
	fmt.Printf("  NetGold Monitor v%s\n", web.Version)
	fmt.Println("  Developed by Shiv")
	fmt.Printf("  Web interface: %s\n", webURL(addr))
	fmt.Println("============================================")
}

// webURL renders a listen address (":8080", "127.0.0.1:8080", …)
// as the address a person types into a browser.
func webURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		return "http://" + host + ":" + port
	}
	return "http://" + addr
}

// run starts the whole application and blocks until stop is closed.
func run(cfg config, stop <-chan struct{}) error {
	if err := os.MkdirAll(cfg.dataDir, 0o755); err != nil {
		return err
	}
	// Record ourselves so a future copy can find and replace this
	// one (the pidfile is the fallback finder on platforms without
	// a process-table scan, and a debugging aid everywhere).
	writePIDFile(cfg.dataDir)
	defer removePIDFile(cfg.dataDir)
	st, err := store.Open(filepath.Join(cfg.dataDir, "netgold.db"))
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer st.DB.Close()

	if cfg.demo {
		if _, err := st.AddDevice(store.Device{Name: "Demo Router", IP: "127.0.0.1", Type: "demo"}); err != nil {
			log.Printf("demo seed: %v", err)
		} else {
			log.Println("demo device seeded (synthetic data)")
		}
	}

	// Automatic topologies (v3.6): generated once on the first start
	// of this version for existing installations (imports generate
	// them too). A topology the user deleted stays deleted — the
	// builder honours the deletion tombstones.
	//
	// v3.6.1: creation is paused while the Topology section is
	// hidden from the interface — nothing should be generated for
	// a section the user cannot see. The builder itself is
	// unchanged; set this to true when the section returns.
	const autoTopologiesEnabled = false
	if autoTopologiesEnabled {
		if rep, err := st.EnsureAutoTopologies(); err != nil {
			log.Printf("automatic topologies: %v", err)
		} else if rep.HubBuilt || rep.CombinedBuilt {
			log.Printf("automatic topologies created: %s (%d nodes, %d links), %s (%d nodes, %d links)",
				rep.HubName, rep.HubNodes, rep.HubLinks,
				rep.CombinedName, rep.CombinedNodes, rep.CombinedLinks)
		}
	}

	// Poll interval bounds follow the legacy system: default 60 s, minimum
	// 10 s, maximum 86,400 s (24 h).
	interval := cfg.interval
	if interval < 10*time.Second {
		log.Printf("poll interval %s is below the 10s minimum — using 10s", interval)
		interval = 10 * time.Second
	}
	if interval > 24*time.Hour {
		log.Printf("poll interval %s is above the 24h maximum — using 24h", interval)
		interval = 24 * time.Hour
	}

	engine := poller.New(st, interval)
	engine.Notifier = &notify.Notifier{Store: st}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go engine.Run(ctx)
	go engine.Maintenance(ctx)

	// v2.1 collectors: NetFlow/sFlow traffic + syslog.
	flows := flow.NewCollector(st, cfg.netflowPort, cfg.sflowPort)
	if err := flows.Start(ctx); err != nil {
		return err
	}
	syslogSrv := &syslogsrv.Server{Store: st, Port: cfg.syslogPort}
	if err := syslogSrv.Start(ctx); err != nil {
		return err
	}
	for _, l := range flows.Listeners() {
		log.Printf("collector listening: %s", l)
	}
	if cfg.syslogPort > 0 {
		log.Printf("syslog receiver listening on UDP+TCP %d", cfg.syslogPort)
	}

	srv := &http.Server{Addr: cfg.addr, Handler: (&web.Server{Store: st, Engine: engine, Flows: flows, Syslog: syslogSrv, RunningAsService: runningAsWindowsService()}).Handler()}
	serveErr := make(chan error, 1)
	go func() {
		fmt.Printf("NetGold Monitor running → http://localhost%s  (data: %s)\n", cfg.addr, cfg.dataDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	select {
	case <-stop:
		log.Println("shutting down…")
	case err := <-serveErr:
		cancel()
		return fmt.Errorf("http: %w", err)
	}
	cancel()
	shutCtx, c2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer c2()
	_ = srv.Shutdown(shutCtx)
	return nil
}
