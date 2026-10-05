package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hoodoo/owcli/internal/serve"
	"github.com/Hoodoo/owcli/internal/store"
	"github.com/Hoodoo/owcli/internal/version"
)

func newServeCommand() *cobra.Command {
	var (
		port       int
		addr       string
		noOpen     bool
		allowHosts []string
		userHeader string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Browse wikis in a local web viewer: graph, pages, search, Claims",
		Long: `Start a read-only viewer on 127.0.0.1 and open it in the browser. It shows the current repository's wiki or workspace by default
and can switch to any wiki or workspace owcli knows (owcli wikis): a graph of
pages and links, rendered pages with their Claims, and search. Pages are read
fresh on every request, so reload to see edits. Mermaid diagrams need network
access to a CDN; everything else works offline.

Behind a reverse proxy such as Google IAP, listen where the proxy can reach
the viewer, accept the public name, and trust the proxy's user header:

  owcli serve --no-open --addr 0.0.0.0:4321 --allow-host wiki.example.com \
    --user-header X-Goog-Authenticated-User-Email

With --user-header, requests without that header are refused. Only use it
when nothing but the proxy can reach the address.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := store.DefaultDirs()
			if err != nil {
				return err
			}
			opts := serve.Options{Dirs: dirs, Version: version.Producer(), Wikis: func() (any, error) { return listWikis(false) }}
			// Default to the current scope when there is one; otherwise the UI
			// starts at the wiki picker.
			if scope, err := dirs.ResolveSearchScope(".", ""); err == nil && scope.Status == store.ScopeReady {
				opts.DefaultWorkspace = scope.Workspace
				for _, w := range scope.Wikis {
					if w.ID == "" {
						w.ID, w.Name = store.WikiID(w.Layout.RepoRoot), scope.Current.Name
					}
					opts.Default = append(opts.Default, w)
				}
			}
			var ln net.Listener
			if addr != "" {
				if cmd.Flags().Changed("port") {
					return errors.New("give --addr or --port, not both")
				}
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return fmt.Errorf("--addr %q: %w", addr, err)
				}
				if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) && userHeader == "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s is not a loopback address; anyone who can reach it can read every wiki owcli knows\n", host)
				}
				if ln, err = net.Listen("tcp", addr); err != nil {
					return err
				}
			} else if ln, err = serve.Listen(port); err != nil {
				return err
			}
			opts.ListenHost, _, _ = net.SplitHostPort(ln.Addr().String())
			opts.AllowHosts, opts.UserHeader = allowHosts, userHeader
			url := "http://" + ln.Addr().String() + "/"
			fmt.Fprintf(cmd.OutOrStdout(), "owcli viewer at %s (Ctrl-C to stop)\n", url)
			if !noOpen {
				openBrowser(url)
			}
			srv := &http.Server{Handler: serve.New(opts).Handler(), ReadHeaderTimeout: 10 * time.Second}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdown)
			}()
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 4321, "port on 127.0.0.1; the next free one is used if it is taken")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser")
	cmd.Flags().StringVar(&addr, "addr", "", "listen on exactly this host:port instead of 127.0.0.1:--port (for a reverse proxy)")
	cmd.Flags().StringArrayVar(&allowHosts, "allow-host", nil, "also accept this name in the Host header, e.g. a proxy's public name (repeatable)")
	cmd.Flags().StringVar(&userHeader, "user-header", "", "trust this request header as the signed-in user and refuse requests without it")
	return cmd
}

// openBrowser opens url with the platform's opener, ignoring failures: the
// URL is printed anyway.
func openBrowser(url string) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	_ = exec.Command(opener, url).Start()
}
