package zap

import (
	"context"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	level "github.com/go-kit/kit/log/experimental_level"
	"github.com/sourcegraph/zap/internal/pkg/backoff"
)

// UpstreamClient is the subset of the Client interface that the
// server uses to communicate with the upstream remote server.
type UpstreamClient interface {
	RepoWatch(context.Context, RepoWatchParams) error
	RefInfo(context.Context, RefIdentifier) (*RefInfoResult, error)
	RefUpdate(context.Context, RefUpdateUpstreamParams) error
	SetRefUpdateCallback(func(context.Context, RefUpdateDownstreamParams) error)
	SetRefUpdateSymbolicCallback(f func(context.Context, RefUpdateSymbolicParams) error)
	DisconnectNotify() <-chan struct{}
	Close() error
}

// ConfigureRemoteClientFunc sets the func that this server calls to
// connect to upstream servers.
func (s *Server) ConfigureRemoteClientFunc(newClient func(ctx context.Context, endpoint string) (UpstreamClient, error)) {
	if s.remotes.newClient != nil {
		panic("(serverRemotes).newClient is already set")
	}
	s.remotes.newClient = newClient
}

type serverRemotes struct {
	parent *Server

	mu   sync.Mutex
	conn map[string]UpstreamClient // remote endpoint -> client

	newClient func(ctx context.Context, endpoint string) (UpstreamClient, error)
}

func (sr *serverRemotes) getClient(endpoint string) (UpstreamClient, bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	c, ok := sr.conn[endpoint]
	return c, ok
}

func (sr *serverRemotes) getOrCreateClient(ctx context.Context, log *log.Context, endpoint string) (UpstreamClient, error) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.newClient == nil {
		panic("(serverRemotes).newClient must be set with (*Server).ConfigureRemoteClientFunc")
	}
	cl, ok := sr.conn[endpoint]
	if !ok {
		ctx := sr.parent.bgCtx // use background context since this isn't tied to a specific request

		var err error
		cl, err = sr.newClient(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		level.Info(log).Log("connected-to-remote", endpoint)
		go func() {
			<-cl.DisconnectNotify()
			// If server is closed, do not attempt to connect. This
			// prevents an infinite loop when the server closes and
			// its connections are closed (which usually would cause
			// them all to try to reconnect).
			sr.parent.closedMu.Lock()
			closed := sr.parent.closed
			sr.parent.closedMu.Unlock()
			if closed {
				return
			}

			log := sr.parent.baseLogger().With("remote-client-monitor", endpoint)
			level.Warn(log).Log("disconnected", "")
			sr.mu.Lock()
			delete(sr.conn, endpoint)
			sr.mu.Unlock()
			if err := backoff.RetryNotifyWithContext(context.Background(), func(ctx context.Context) error {
				return sr.tryReconnect(ctx, log, endpoint)
			}, remoteBackOff(), func(err error, d time.Duration) {
				level.Debug(log).Log("retry-reconnect-after-error", err)
			}); err != nil {
				level.Error(log).Log("reconnect-failed-after-retries", err)
			}
		}()
		cl.SetRefUpdateCallback(func(ctx context.Context, params RefUpdateDownstreamParams) error {
			// Create a clean logger, because it will be used in
			// requests other than the initial one.
			log := sr.parent.baseLogger().With("callback-from-remote-endpoint", endpoint)
			if err := sr.parent.handleRefUpdateFromUpstream(ctx, log, params, endpoint); err != nil {
				level.Error(log).Log("params", params, "err", err)
				return err
			}
			return nil
		})
		cl.SetRefUpdateSymbolicCallback(func(context.Context, RefUpdateSymbolicParams) error {
			// Nothing to do here; symbolic refs are not shared between servers.
			return nil
		})
		if sr.conn == nil {
			sr.conn = map[string]UpstreamClient{}
		}
		sr.conn[endpoint] = cl
	}
	return cl, nil
}

func (sr *serverRemotes) closeAndRemoveClient(endpoint string) error {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	cl, ok := sr.conn[endpoint]
	if !ok {
		panic("no remote client for endpoint " + endpoint)
	}
	delete(sr.conn, endpoint)
	return cl.Close()
}

func (sr *serverRemotes) tryReconnect(ctx context.Context, log *log.Context, endpoint string) error {
	level.Debug(log).Log("try-reconnect", "")
	cl, err := sr.getOrCreateClient(ctx, log, endpoint)
	if err != nil {
		return err
	}
	level.Debug(log).Log("reconnect-ok", "")

	reestablishRepo := func(repoName string, repo *serverRepo) error {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		for remoteName, remote := range repo.config.Remotes {
			if remote.Endpoint == endpoint {
				level.Debug(log).Log("reestablish-watch-repo", repoName, "remote", remoteName)
				if err := cl.RepoWatch(ctx, RepoWatchParams{Repo: remote.Repo, Refspec: remote.Refspec}); err != nil {
					return err
				}
				for refName, refConfig := range repo.config.Refs {
					ref := repo.refdb.Lookup(refName)
					if refConfig.Overwrite && refConfig.Upstream == remoteName && ref != nil {
						o := ref.Object.(serverRef)
						if err := cl.RefUpdate(ctx, RefUpdateUpstreamParams{
							RefIdentifier: RefIdentifier{Repo: remote.Repo, Ref: refName},
							Force:         true,
							State: &RefState{
								RefBaseInfo: RefBaseInfo{GitBase: o.gitBase, GitBranch: o.gitBranch},
								History:     o.history(),
							},
						}); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}

	sr.parent.reposMu.Lock()
	defer sr.parent.reposMu.Unlock()
	for repoName, repo := range sr.parent.repos {
		if err := reestablishRepo(repoName, repo); err != nil {
			return err
		}
	}

	return nil
}

func remoteBackOff() backoff.BackOff {
	p := backoff.NewExponentialBackOff()
	p.InitialInterval = 500 * time.Millisecond
	p.Multiplier = 2
	p.MaxElapsedTime = 1 * time.Minute
	return p
}
