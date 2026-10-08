// Package bridge sets up the laptop end of the mesh: the bridge daemon and the reverse tunnel that carries the
// devbox's 127.0.0.1:7722 to it. Both are user LaunchAgents; the laptop runs no sshd.
package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/rpc"
)

const (
	BridgeLabel = "com.saahil.brain-bridge"
	TunnelLabel = "com.saahil.brain-tunnel"
)

var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>{{range .Args}}
    <string>{{.}}</string>{{end}}
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>AbandonProcessGroup</key><true/>
  <key>ThrottleInterval</key><integer>{{.Throttle}}</integer>
  <key>StandardOutPath</key><string>{{.Log}}</string>
  <key>StandardErrorPath</key><string>{{.Log}}</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>{{.Home}}/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>HOME</key><string>{{.Home}}</string>
  </dict>
</dict>
</plist>
`))

type agentSpec struct {
	Label    string
	Args     []string
	Throttle int
	Log      string
	Home     string
}

func specs() []agentSpec {
	home := config.Home()
	logs := filepath.Join(home, "Library", "Logs")
	port := strconv.Itoa(config.BridgePort)
	return []agentSpec{
		{Label: BridgeLabel, Args: []string{filepath.Join(config.BinDir(), "brain"), "bridge", "serve"}, Throttle: 10,
			Log: filepath.Join(logs, "brain-bridge.log"), Home: home},
		{Label: TunnelLabel, Args: []string{"/usr/bin/ssh", "-N", "-o", "ExitOnForwardFailure=yes",
			"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3", "-o", "BatchMode=yes",
			"-o", "ControlMaster=no", "-o", "ControlPath=none",
			"-R", "127.0.0.1:" + port + ":127.0.0.1:" + port, config.DevboxHost}, Throttle: 30,
			Log: filepath.Join(logs, "brain-tunnel.log"), Home: home},
	}
}

// Install writes both LaunchAgents, (re)loads them and checks the bridge answers locally with the token.
func Install() error {
	if config.ThisMachine() != config.Laptop {
		return fmt.Errorf("bridge install runs on the laptop")
	}
	token, err := os.ReadFile(config.BridgeTokenPath())
	if err != nil {
		return fmt.Errorf("bridge token: %w (the bootstrap copies it from the devbox)", err)
	}
	agentsDir := filepath.Join(config.Home(), "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	for _, spec := range specs() {
		path := filepath.Join(agentsDir, spec.Label+".plist")
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := plistTemplate.Execute(f, spec); err != nil {
			f.Close()
			return err
		}
		f.Close()
		_ = exec.Command("launchctl", "bootout", domain+"/"+spec.Label).Run()
		time.Sleep(500 * time.Millisecond)
		if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap %s: %v %s", spec.Label, err, out)
		}
		fmt.Printf("loaded %s (%s)\n", spec.Label, path)
	}
	peer := rpc.HTTPPeer{URL: fmt.Sprintf("http://127.0.0.1:%d", config.BridgePort), Token: strings.TrimSpace(string(token))}
	var lastErr error
	for range 20 {
		time.Sleep(500 * time.Millisecond)
		resp, err := peer.Call(context.Background(), rpc.Request{Op: "status"})
		if err == nil && resp.OK {
			fmt.Printf("bridge answers on 127.0.0.1:%d: %s\n", config.BridgePort, resp.Result)
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("bridge did not answer locally: %v (see ~/Library/Logs/brain-bridge.log)", lastErr)
}

// SelfUpdate pulls the newest darwin binary the devbox published, checks its sha256, swaps it in place and restarts
// the bridge. The laptop fetches over its own ssh; the devbox never pushes code.
func SelfUpdate() error {
	if config.ThisMachine() != config.Laptop {
		return fmt.Errorf("self-update runs on the laptop; the devbox builds in place")
	}
	dist := filepath.Join(config.DevboxHome, ".local", "share", "brain", "dist")
	target := filepath.Join(config.BinDir(), "brain")
	tmp := filepath.Join(config.BinDir(), ".brain.new")
	sumFile := tmp + ".sha256"
	for src, dst := range map[string]string{dist + "/brain-darwin-arm64": tmp, dist + "/brain-darwin-arm64.sha256": sumFile} {
		if out, err := exec.Command("scp", "-q", "-o", "BatchMode=yes", "-o", "ControlPath=none",
			config.DevboxHost+":"+src, dst).CombinedOutput(); err != nil {
			return fmt.Errorf("scp %s: %v %s", src, err, out)
		}
	}
	defer os.Remove(sumFile)
	want, err := os.ReadFile(sumFile)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(tmp)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != strings.Fields(string(want))[0] {
		os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, strings.TrimSpace(string(want)))
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	out, _ := exec.Command(target, "version").Output()
	fmt.Printf("installed %s", out)
	return exec.Command("launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(os.Getuid())+"/"+BridgeLabel).Run()
}
