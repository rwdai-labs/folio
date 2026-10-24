// Folio serves the private maintenance runtime or builds a task context packet.
package main

import (
	// Embed the timezone database: the off-peak window is evaluated in
	// container local time, and a slim image may not ship zoneinfo.
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	contextrepo "folio/internal/context"
	"folio/internal/store"
	"folio/internal/web"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: folio serve | healthcheck | status | build | search | candidate | maintain | agent")
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		err = serve(args[1:])
	case "healthcheck":
		err = healthcheck(args[1:])
	default:
		return contextrepo.Run(args, os.Stdout, os.Stderr)
	}
	if err != nil {
		// Never expose HTTP/process error payloads or runtime credentials.
		fmt.Fprintln(os.Stderr, "folio: operation failed; check configuration and service status")
		return 1
	}
	return 0
}

func serve(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := flags.String("host", env("SERVICE_BIND_HOST", "0.0.0.0"), "listen address")
	port := flags.Int("port", envInt("SERVICE_PORT", 8080, 1, 65535), "listen port")
	dataDir := flags.String("data-dir", env("SERVICE_DATA_DIR", "/data"), "private persistent runtime")
	toolRoot := flags.String("tool-root", env("SERVICE_TOOL_ROOT", filepath.Dir(exe)), "installed tool directory")
	assetsDir := flags.String("assets-dir", "", "installed Web assets directory")
	passwordFile := flags.String("password-file", os.Getenv("ADMIN_PASSWORD_FILE"), "external administrator password file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *port < 1 || *port > 65535 {
		return errors.New("invalid server arguments")
	}
	if *assetsDir == "" {
		*assetsDir = filepath.Join(*toolRoot, "web")
	}
	backend, err := store.New(*dataDir, *toolRoot)
	if err != nil {
		return err
	}
	application, err := web.New(backend, web.Options{
		DataDir: *dataDir, PasswordFile: *passwordFile, AssetsDir: *assetsDir,
		PasswordNotice: func(password, path string) {
			announcePassword(os.Stderr, password, path)
		},
		TrustedHosts:      split(env("TRUSTED_HOSTS", "localhost,127.0.0.1")),
		TrustedOrigins:    split(os.Getenv("TRUSTED_ORIGINS")),
		CookieSecure:      envBool("COOKIE_SECURE"),
		SchedulerEnabled:  envBool("MAINTENANCE_SCHEDULER_ENABLED"),
		SchedulerInterval: time.Duration(envInt("MAINTENANCE_INTERVAL_SECONDS", 86400, 60, 604800)) * time.Second,
	})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	application.StartScheduler(ctx)
	application.StartDispatcher(ctx)
	server := &http.Server{
		Addr: *host + ":" + strconv.Itoa(*port), Handler: application.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// announcePassword reports the administrator password that this start created.
// The password file stays authoritative; the notice exists so the operator does
// not have to exec into the container on first deployment. It is never called
// for an existing or externally mounted password file, and later starts do not
// repeat it. Anything reading the service log afterwards must treat it as a
// credential source.
func announcePassword(w io.Writer, password, path string) {
	fmt.Fprintf(w, "folio: no administrator password found; generated one for this first start\n")
	fmt.Fprintf(w, "folio: administrator password: %s\n", password)
	fmt.Fprintf(w, "folio: stored at %s (mode 0600); printed only on this first start\n", path)
	fmt.Fprintf(w, "folio: keep container logs private because they now contain this credential\n")
}

func healthcheck(args []string) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := flags.String("url", "http://127.0.0.1:8080/", "local service URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("invalid healthcheck arguments")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(*url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("service is unavailable")
	}
	return nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func envBool(name string) bool {
	v := strings.ToLower(os.Getenv(name))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}
func envInt(name string, fallback, min, max int) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v < min || v > max {
		return fallback
	}
	return v
}
func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
