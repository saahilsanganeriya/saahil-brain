package rpc

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"saahil.local/brain/internal/config"
)

const maxRequestBytes = 1 << 20

// Server is the laptop bridge: HTTP on loopback, every request token-checked and logged without its text.
type Server struct {
	Handler *Handler
	Token   string
	LogPath string
	logMu   sync.Mutex
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var req Request
	status, resp := s.serve(r, &req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
	s.log(r, req, status, resp, time.Since(started))
}

func (s *Server) serve(r *http.Request, req *Request) (int, Response) {
	if r.Method != http.MethodPost || r.URL.Path != "/rpc" {
		return http.StatusNotFound, failf("POST /rpc only")
	}
	got, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !hasBearer || s.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
		return http.StatusUnauthorized, failf("missing or wrong token")
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes)).Decode(req); err != nil {
		return http.StatusBadRequest, fail(err)
	}
	resp := s.Handler.Handle(r.Context(), *req)
	if !resp.OK {
		return http.StatusUnprocessableEntity, resp
	}
	return http.StatusOK, resp
}

type logLine struct {
	Time   time.Time `json:"time"`
	Remote string    `json:"remote"`
	Op     string    `json:"op"`
	Step   string    `json:"step,omitempty"`
	To     string    `json:"to,omitempty"`
	From   string    `json:"from,omitempty"`
	ID     string    `json:"id,omitempty"`
	Status int       `json:"status"`
	Error  string    `json:"error,omitempty"`
	Ms     int64     `json:"ms"`
}

func (s *Server) log(r *http.Request, req Request, status int, resp Response, took time.Duration) {
	if s.LogPath == "" {
		return
	}
	line, _ := json.Marshal(logLine{Time: time.Now().UTC(), Remote: r.RemoteAddr, Op: req.Op, Step: req.Step, To: req.To,
		From: req.From, ID: req.ID, Status: status, Error: resp.Error, Ms: took.Milliseconds()})
	s.logMu.Lock()
	defer s.logMu.Unlock()
	f, err := os.OpenFile(s.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ListenAndServe binds loopback only.
func (s *Server) ListenAndServe(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute}
	return srv.Serve(ln)
}

// Peer answers requests on the other machine.
type Peer interface {
	Call(ctx context.Context, req Request) (Response, error)
	Name() string
}

// ErrUnreachable means the other machine did not answer (asleep, tunnel down); callers queue or report, never fail hard.
var ErrUnreachable = errors.New("peer unreachable")

// HTTPPeer reaches the laptop bridge from the devbox through the reverse tunnel.
type HTTPPeer struct {
	URL   string
	Token string
}

func (p HTTPPeer) Name() string { return string(config.Laptop) }

func (p HTTPPeer) Call(ctx context.Context, req Request) (Response, error) {
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/rpc", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+p.Token)
	hreq.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer res.Body.Close()
	var resp Response
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("%w: bad reply (HTTP %d): %v", ErrUnreachable, res.StatusCode, err)
	}
	return resp, nil
}

// SSHPeer reaches the devbox from the laptop with the laptop's own ssh (key + alias already set up).
type SSHPeer struct {
	Host   string
	Binary string
}

func (p SSHPeer) Name() string { return string(config.Devbox) }

func (p SSHPeer) Call(ctx context.Context, req Request) (Response, error) {
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "ControlPath=none",
		p.Host, p.Binary, "rpc")
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Response{}, fmt.Errorf("%w: ssh %s: %v %s", ErrUnreachable, p.Host, err, strings.TrimSpace(stderr.String()))
	}
	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		return Response{}, fmt.Errorf("%w: bad reply: %v", ErrUnreachable, err)
	}
	return resp, nil
}

// PeerFor returns the transport to the other machine.
func PeerFor(m config.Machine) (Peer, error) {
	switch m {
	case config.Laptop:
		token, err := os.ReadFile(config.BridgeTokenPath())
		if err != nil {
			return nil, fmt.Errorf("no bridge token at %s: %w", config.BridgeTokenPath(), err)
		}
		return HTTPPeer{URL: fmt.Sprintf("http://127.0.0.1:%d", config.BridgePort), Token: strings.TrimSpace(string(token))}, nil
	case config.Devbox:
		return SSHPeer{Host: config.DevboxHost, Binary: filepath.Join(config.DevboxHome, ".local", "bin", "brain")}, nil
	}
	return nil, fmt.Errorf("unknown machine %q", m)
}

// Decode unpacks a successful response's result.
func Decode[T any](resp Response) (T, error) {
	var v T
	if !resp.OK {
		return v, errors.New(resp.Error)
	}
	err := json.Unmarshal(resp.Result, &v)
	return v, err
}
