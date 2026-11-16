package manager

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func Run(args []string, stdout, stderr io.Writer) (exit int) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintln(stderr, fmtError(p))
			exit = 1
		}
	}()
	cfg := LoadConfig()
	if cfg.BindPort < 1 || cfg.BindPort > 65535 {
		fmt.Fprintln(stderr, "SEM_BIND_PORT must be between 1 and 65535")
		return 1
	}
	action := "serve"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	if action == "--help" || action == "-h" || action == "help" {
		fmt.Fprintln(stdout, "SoftEther Manager\n\nUsage: softether-manager [serve|healthcheck|address|status|users|password|connect|webpath|domain]\n\npassword --username NAME --password-stdin\nconnect --host HOST --port PORT --password-stdin [--auth header|basic|both]\nwebpath [PREFIX]\ndomain [show|set NAME|clear|only on|off]\n\nConfiguration: SEM_DATA_DIR, SEM_BIND_HOST, SEM_BIND_PORT, SEM_WEB_PATH, SEM_SE_HOST, SEM_SE_PORT")
		return 0
	}
	if action == "version" || action == "--version" {
		fmt.Fprintln(stdout, cfg.Version)
		return 0
	}
	a, err := New(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer a.Close()
	output := func(v any) { check(json.NewEncoder(stdout).Encode(v)) }
	switch action {
	case "serve":
		if len(args) > 0 {
			fmt.Fprintln(stderr, "serve takes no arguments; use SEM_* environment variables")
			return 2
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		a.Start(ctx)
		server := &http.Server{Addr: net.JoinHostPort(cfg.BindHost, strconv.Itoa(cfg.BindPort)), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		}()
		fmt.Fprintf(stderr, "SoftEther Manager %s listening on %s\n", cfg.Version, server.Addr)
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "healthcheck":
		path := strings.Trim(str(a.setting("web_path")), "/")
		if path != "" {
			path = "/" + path
		}
		res, err := (&http.Client{Timeout: 4 * time.Second}).Get("http://127.0.0.1:" + strconv.Itoa(cfg.BindPort) + path + "/api/v1/system/health")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return 1
		}
		return 0
	case "address", "status":
		domain := str(a.setting("domain"))
		cert, _ := a.tls.certificate(domain)
		out := M{"host": cfg.BindHost, "port": cfg.BindPort, "web_path": a.setting("web_path"), "domain": domain, "https_port": a.setting("https_port"), "domain_only": a.setting("domain_only"), "https_ready": boolean(cert["present"]) && !boolean(cert["stale"])}
		if action == "status" {
			out["connection"] = a.connection()
			out["version"] = cfg.Version
		}
		output(out)
		return 0
	case "users":
		for _, row := range query(a.db, `SELECT Username,CreatedDate FROM PanelUser WHERE IsDeleted=0 ORDER BY UserID`) {
			fmt.Fprintf(stdout, "%s\tsince %s\n", row["Username"], row["CreatedDate"])
		}
		return 0
	case "password":
		flags := flag.NewFlagSet(action, flag.ContinueOnError)
		flags.SetOutput(stderr)
		username := flags.String("username", "", "Panel administrator username")
		password := flags.String("password", "", "New password")
		fromStdin := flags.Bool("password-stdin", false, "Read the password from standard input")
		if err := flags.Parse(args); err != nil {
			return 2
		}
		a.SetPassword(*username, cliPassword(*password, *fromStdin))
		fmt.Fprintf(stdout, "Account %s updated.\n", *username)
		return 0
	case "connect":
		flags := flag.NewFlagSet(action, flag.ContinueOnError)
		flags.SetOutput(stderr)
		host := flags.String("host", "127.0.0.1", "SoftEther host")
		port := flags.Int("port", 5555, "SoftEther management port")
		password := flags.String("password", "", "SoftEther administrator password")
		auth := flags.String("auth", "header", "header, basic or both")
		initialize := flags.Bool("initialize-password", false, "Set the password on a fresh SoftEther server with an empty administrator password")
		fromStdin := flags.Bool("password-stdin", false, "Read the password from standard input")
		if err := flags.Parse(args); err != nil {
			return 2
		}
		*password = cliPassword(*password, *fromStdin)
		connectionInput(M{"host": *host, "port": *port, "password": *password, "auth": *auth})
		if *initialize {
			if *password == "" {
				fail(422, "The initial administrator password cannot be empty.")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client := a.newRPCClient(*host, *port, "", *auth)
			defer client.Close()
			_, err := client.Call(ctx, "SetServerPassword", M{"PlainTextPassword_str": *password})
			check(err)
		}
		a.saveConnection(*host, *port, password, *auth)
		fmt.Fprintf(stdout, "Connection set to %s:%d.\n", *host, *port)
		return 0
	case "webpath":
		path := ""
		if len(args) > 0 {
			path = args[0]
		}
		a.set("web_path", cleanWebPath(path))
		fmt.Fprintln(stdout, "Web path saved. Restart the service to apply it.")
		return 0
	case "domain":
		sub, value := "show", ""
		if len(args) > 0 {
			sub = args[0]
		}
		if len(args) > 1 {
			value = args[1]
		}
		switch sub {
		case "show":
			output(M{"domain": a.setting("domain"), "https_port": a.setting("https_port"), "domain_only": a.setting("domain_only")})
		case "set":
			domain := normalizeDomain(value)
			if domain == "" {
				fail(422, "A domain name is required.")
			}
			a.set("domain", domain)
			a.set("domain_only", false)
		case "clear":
			a.set("domain", "")
			a.set("domain_only", false)
		case "only":
			if value != "on" && value != "off" {
				fail(422, "Usage: domain only on|off")
			}
			if value == "on" && str(a.setting("domain")) == "" {
				fail(422, "Set a domain first.")
			}
			a.set("domain_only", value == "on")
		default:
			fail(422, "Usage: domain [show|set NAME|clear|only on|off]")
		}
		return 0
	default:
		fmt.Fprintln(stderr, "Unknown command:", action)
		return 2
	}
}
