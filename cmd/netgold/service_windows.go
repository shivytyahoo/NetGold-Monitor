//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName        = "NetGoldMonitor"
	serviceDisplayName = "NetGold Monitor"
	serviceDescription = "NetGold Monitor — free, open-source network monitoring (ping, SNMP, NetFlow, syslog). Developed by Shivam Bhalla."
)

// runningAsWindowsService reports whether the process was started by
// the Windows Service Control Manager.
func runningAsWindowsService() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// netgoldService adapts the application to the SCM dispatcher.
type netgoldService struct{ cfg config }

func (s *netgoldService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(s.cfg, stop) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			changes <- svc.Status{State: svc.Stopped}
			if err != nil {
				return false, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				close(stop)
			}
		}
	}
}

// runWindowsService hands control to the SCM dispatcher.
func runWindowsService(cfg config) error {
	return svc.Run(serviceName, &netgoldService{cfg: cfg})
}

// serviceArgs builds the command line the service will be started with.
// The data directory is made absolute: a service's working directory is
// not the install folder, so a relative -data would end up in System32.
func serviceArgs(cfg config) ([]string, error) {
	abs, err := filepath.Abs(cfg.dataDir)
	if err != nil {
		return nil, err
	}
	return []string{
		"-addr", cfg.addr,
		"-data", abs,
		"-interval", cfg.interval.String(),
		"-netflow-port", fmt.Sprint(cfg.netflowPort),
		"-sflow-port", fmt.Sprint(cfg.sflowPort),
		"-syslog-port", fmt.Sprint(cfg.syslogPort),
	}, nil
}

// handleServiceCommand implements -service install|uninstall|start|stop|status
// through the service control manager. These commands need an elevated
// (Administrator) shell for install/uninstall/start/stop.
func handleServiceCommand(cmd string, cfg config) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service control manager: %w", err)
	}
	defer m.Disconnect()

	switch cmd {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		args, err := serviceArgs(cfg)
		if err != nil {
			return err
		}
		s, err := m.CreateService(serviceName, exe, mgr.Config{
			DisplayName: serviceDisplayName,
			Description: serviceDescription,
			StartType:   mgr.StartAutomatic,
		}, args...)
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		defer s.Close()
		// Restart on failure: after 5 s, then 30 s, then 60 s; the
		// failure counter resets after a day of healthy running.
		actions := []mgr.RecoveryAction{
			{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		}
		if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
			fmt.Printf("warning: could not set recovery options: %v\n", err)
		}
		fmt.Printf("Service %s installed (automatic start). Run: netgold -service start\n", serviceName)
		return nil

	case "uninstall":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("open service: %w", err)
		}
		defer s.Close()
		_, _ = s.Control(svc.Stop) // best effort: stop before removal
		if err := s.Delete(); err != nil {
			return fmt.Errorf("delete service: %w", err)
		}
		fmt.Printf("Service %s uninstalled.\n", serviceName)
		return nil

	case "start", "stop", "status":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("open service (is it installed?): %w", err)
		}
		defer s.Close()
		switch cmd {
		case "start":
			if err := s.Start(); err != nil {
				return fmt.Errorf("start service: %w", err)
			}
			fmt.Println("Service start requested.")
		case "stop":
			if _, err := s.Control(svc.Stop); err != nil {
				return fmt.Errorf("stop service: %w", err)
			}
			fmt.Println("Service stop requested.")
		case "status":
			st, err := s.Query()
			if err != nil {
				return err
			}
			fmt.Printf("%s (%s): %s\n", serviceDisplayName, serviceName, stateName(st.State))
		}
		return nil
	}
	return fmt.Errorf("unknown service command %q (use install, uninstall, start, stop or status)", cmd)
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "Start pending"
	case svc.StopPending:
		return "Stop pending"
	case svc.Running:
		return "Running"
	case svc.ContinuePending:
		return "Continue pending"
	case svc.PausePending:
		return "Pause pending"
	case svc.Paused:
		return "Paused"
	}
	return fmt.Sprintf("state %d", int(s))
}
