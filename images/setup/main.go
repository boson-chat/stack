// stack-setup: Boson's web-driven installer. install.sh starts it as the
// boson-setup container (host network, the Docker socket, the install dir
// mounted at the same path); it serves the wizard on https://<ip>:8443
// with a self-signed certificate and a one-time code.
//
//	stack-setup                 serve the wizard (default)
//	stack-setup code            print the current code (docker exec)
//	stack-setup fingerprint     print the wizard certificate's SHA-256
//	stack-setup probe ...       the ports check's probe (runs in a container)
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/checks"
	"github.com/boson-chat/stack/images/setup/internal/dockercli"
	"github.com/boson-chat/stack/images/setup/internal/flow"
	"github.com/boson-chat/stack/images/setup/internal/flow/addon"
	"github.com/boson-chat/stack/images/setup/internal/flow/fresh"
	"github.com/boson-chat/stack/images/setup/internal/gate"
	"github.com/boson-chat/stack/images/setup/internal/install"
	"github.com/boson-chat/stack/images/setup/internal/secret"
	"github.com/boson-chat/stack/images/setup/internal/server"
	"github.com/boson-chat/stack/images/setup/internal/stack"
	"github.com/boson-chat/stack/images/setup/internal/tlscert"
	"github.com/boson-chat/stack/images/setup/web"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var runDir = env("BOSON_SETUP_RUN_DIR", "/run/boson-setup")

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "stack-setup:", err)
			os.Exit(1)
		}
	case "code", "fingerprint":
		b, err := os.ReadFile(filepath.Join(runDir, cmd))
		if err != nil {
			fmt.Fprintln(os.Stderr, "stack-setup: not running yet?", err)
			os.Exit(1)
		}
		fmt.Println(strings.TrimSpace(string(b)))
	case "idle":
		// The add-on probe's helper container: waits for docker exec.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
	case "ircprobe":
		// The add-on path's IRC probe, run in a container on the stack's
		// network (addon.ContainerProbe): options on stdin, a report out.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := addon.RunWire(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "stack-setup ircprobe:", err)
			os.Exit(1)
		}
	case "probe":
		fs := flag.NewFlagSet("probe", flag.ExitOnError)
		host := fs.String("host", "", "address to test")
		nonce := fs.String("nonce", "", "UDP payload")
		tcp := fs.String("tcp", "", "TCP ports")
		udp := fs.String("udp", "", "UDP ports")
		_ = fs.Parse(os.Args[2:])
		res := checks.RunProbeClient(*host, *nonce, checks.ParseInts(*tcp), checks.ParseInts(*udp))
		_ = json.NewEncoder(os.Stdout).Encode(res)
	default:
		fmt.Fprintln(os.Stderr, "usage: stack-setup [serve|code|fingerprint|probe]")
		os.Exit(2)
	}
}

func dockerGID() string {
	var st syscall.Stat_t
	if syscall.Stat("/var/run/docker.sock", &st) == nil {
		return strconv.Itoa(int(st.Gid))
	}
	return "999"
}

var publishedRe = regexp.MustCompile(`:(\d+)->\d+/(tcp|udp)`)

func serve() error {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	dir := env("BOSON_INSTALL_DIR", "/opt/boson-stack")
	port, _ := strconv.Atoi(env("BOSON_SETUP_PORT", "8443"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	g := gate.New()
	instance := secret.Hex(16)
	ips := checks.LocalIPs()
	host, _ := os.Hostname()
	cert, fp, err := tlscert.New(append(ips, net.ParseIP("127.0.0.1")), []string{host, "localhost"})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return err
	}
	// The code is a secret: never logged, only readable through
	// `docker exec boson-setup stack-setup code` (install.sh does that).
	if err := os.WriteFile(filepath.Join(runDir, "code"), []byte(g.Code()+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runDir, "fingerprint"), []byte(fp+"\n"), 0o600); err != nil {
		return err
	}

	red := &dockercli.Redactor{}
	cli := &dockercli.CLI{Dir: dir, Project: "boson", Redact: red}
	runner, err := install.NewRunner(&install.Runner{
		Dir:      dir,
		StackSrc: env("BOSON_SETUP_STACK_SRC", "/usr/share/boson-stack"),
		Compose:  &install.DockerCompose{CLI: cli},
		Store:    install.NewStore(dir),
		Hub:      install.NewHub(),
		Edge:     env("BOSON_SETUP_EDGE", "127.0.0.1"),
	})
	if err != nil {
		return err
	}

	probeImage := env("BOSON_SETUP_IMAGE", "")
	ports := &checks.Ports{
		HostIP: checks.PrimaryIP,
		Nonce:  func() string { return secret.Hex(12) },
		BosonPorts: func(ctx context.Context) map[string]bool {
			out, err := cli.Output(ctx, "ps", "--filter", "label=com.docker.compose.project=boson", "--format", "{{.Ports}}")
			m := map[string]bool{}
			if err != nil {
				return m
			}
			for _, x := range publishedRe.FindAllStringSubmatch(string(out), -1) {
				m[x[2]+"/"+x[1]] = true
			}
			return m
		},
	}
	if probeImage != "" {
		ports.RunProbe = func(ctx context.Context, host, nonce string, tcp, udp []int) (checks.ProbeResult, error) {
			join := func(p []int) string {
				var s []string
				for _, x := range p {
					s = append(s, strconv.Itoa(x))
				}
				return strings.Join(s, ",")
			}
			out, err := cli.Output(ctx, "run", "--rm", "--network", "bridge", "--label", "org.boson.setup=probe",
				"--entrypoint", "/usr/local/bin/stack-setup", probeImage, "probe",
				"--host", host, "--nonce", nonce, "--tcp", join(tcp), "--udp", join(udp))
			if err != nil {
				return checks.ProbeResult{}, err
			}
			return checks.ParseProbeOutput(out)
		}
	}

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	stop := make(chan struct{}, 1)
	finish := func() {
		g.Close()
		log.Info("setup finished: switching the wizard off")
		time.Sleep(2 * time.Second)
		select {
		case stop <- struct{}{}:
		default:
		}
	}
	runner.Logf = func(step, line string) { log.Info("install", "step", step, "line", red.Redact(line)) }
	runner.OnDone = func(install.State) {
		// The Done page needs the session a while longer; if nobody closes
		// the wizard, it switches itself off.
		go func() {
			time.Sleep(30 * time.Minute)
			if st := runner.State(); st.Status == install.StatusDone {
				runner.SetFinished()
				finish()
			}
		}()
	}

	deps := fresh.Deps{
		InstallDir: dir,
		Runner:     runner,
		DNS: &checks.DNS{
			Resolver: checks.NewResolver(env("BOSON_SETUP_DNS_SERVER", "")),
			LocalIPs: checks.LocalIPs,
			Probe:    checks.InstanceProbe(port, instance),
		},
		Ports:     ports,
		DockerGID: dockerGID,
		Redactor:  red,
		Finish:    finish,
	}
	fr, err := fresh.New(deps)
	if err != nil {
		return err
	}
	ad, err := addon.New(deps)
	if err != nil {
		return err
	}
	if probeImage != "" {
		subnet := "10.203.53.0/24"
		if ex, err := stack.LoadExisting(dir); err == nil && ex.Env["STACK_SUBNET"] != "" {
			subnet = ex.Env["STACK_SUBNET"]
		}
		ad.Probe = addon.ContainerProbe(cli, probeImage, "boson", subnet)
		pctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if err := addon.StartProbeHelper(pctx, cli, probeImage, "boson", subnet); err != nil {
			log.Warn("add-on probe container", "err", err)
		}
		cancel()
		runner.BeforeUp = func(ctx context.Context) { addon.RemoveProbeHelper(ctx, cli, "boson") }
	}
	reg := &flow.Registry{}
	reg.Add(fr)
	reg.Add(ad)

	srv.Handler = (&server.Server{Gate: g, Flows: reg, Static: web.FS, Instance: instance, Log: log}).Handler()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
		case <-stop:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if probeImage != "" {
			addon.RemoveProbeHelper(ctx, cli, "boson")
		}
	}()
	log.Info("Boson setup is listening", "port", port, "install_dir", dir, "certificate_sha256", fp)
	log.Info("get the one-time code with: docker exec boson-setup stack-setup code")
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
