// Command gitscope indexes a git repository's history and serves an
// interactive local web UI to explore it.
//
//	gitscope [flags]          index and open the UI (default)
//	gitscope index [flags]    only update the index
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"gitscope/internal/gitlog"
	"gitscope/internal/server"
	"gitscope/internal/store"
	"gitscope/web"
)

func main() {
	repo := flag.String("repo", ".", "path inside the repository to analyze")
	port := flag.Int("port", 7777, "port to listen on (127.0.0.1 only); falls back to a free port")
	noOpen := flag.Bool("no-open", false, "do not open the browser")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "uso: gitscope [index] [flags]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := run(flag.Arg(0), *repo, *port, !*noOpen); err != nil {
		fmt.Fprintln(os.Stderr, "gitscope:", err)
		os.Exit(1)
	}
}

func run(cmd, repo string, port int, open bool) error {
	if cmd != "" && cmd != "index" && cmd != "serve" {
		flag.Usage()
		return fmt.Errorf("comando desconhecido %q", cmd)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root, err := gitlog.TopLevel(ctx, repo)
	if err != nil {
		return fmt.Errorf("%s não é um repositório git", repo)
	}
	gitDir, err := gitlog.GitDir(ctx, root)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(gitDir, "gitscope", "index.db"), root)
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("indexando %s...\n", root)
	res, err := st.Sync(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%d commits (%d novos, %d removidos) em %s\n",
		res.Total, res.Added, res.Removed, res.Duration.Round(time.Millisecond))
	if cmd == "index" {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
	}
	url := fmt.Sprintf("http://localhost:%d", ln.Addr().(*net.TCPAddr).Port)
	fmt.Printf("gitscope em %s  (Ctrl+C para sair)\n", url)
	if open {
		openBrowser(url)
	}

	srv := &http.Server{Handler: server.New(st, root, web.FS), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
