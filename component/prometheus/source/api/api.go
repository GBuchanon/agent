package api

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/go-kit/log/level"
	"github.com/grafana/agent/component"
	agentprom "github.com/grafana/agent/component/prometheus"
	"github.com/grafana/agent/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/storage/remote"
	"github.com/weaveworks/common/logging"
	"github.com/weaveworks/common/server"
)

func init() {
	component.Register(component.Registration{
		Name: "prometheus.source.api",
		Args: Arguments{},
		Build: func(opts component.Options, args component.Arguments) (component.Component, error) {
			return New(opts, args.(Arguments))
		},
	})
}

type Arguments struct {
	HTTPAddress string               `river:"http_address,attr"`
	HTTPPort    int                  `river:"http_port,attr"`
	ForwardTo   []storage.Appendable `river:"forward_to,attr"`
}

type Component struct {
	opts               component.Options
	handler            http.Handler
	serverErr          chan error
	fanout             *agentprom.Fanout
	uncheckedCollector *util.UncheckedCollector

	updateMut sync.RWMutex
	args      Arguments
	server    *server.Server
}

func New(opts component.Options, args Arguments) (component.Component, error) {
	fanout := agentprom.NewFanout(args.ForwardTo, opts.ID, opts.Registerer)
	uncheckedCollector := util.NewUncheckedCollector(nil)
	c := &Component{
		opts:               opts,
		handler:            remote.NewWriteHandler(opts.Logger, fanout),
		serverErr:          make(chan error),
		fanout:             fanout,
		args:               args,
		uncheckedCollector: uncheckedCollector,
	}
	opts.Registerer.MustRegister(uncheckedCollector)

	// we do not need to hold the lock here, since `c` is not yet exposed
	err := c.createNewServer(args)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (c *Component) Run(ctx context.Context) error {
	go c.runServer()
	defer func() {
		c.updateMut.Lock()
		defer c.updateMut.Unlock()
		c.shutdownServer()
	}()

	for {
		select {
		case err := <-c.serverErr:
			return fmt.Errorf("server terminated with error: %v", err)
		case <-ctx.Done():
			level.Info(c.opts.Logger).Log("msg", "terminating due to context done")
			return nil
		}
	}
}

func (c *Component) Update(args component.Arguments) error {
	newArgs := args.(Arguments)
	c.fanout.UpdateChildren(newArgs.ForwardTo)

	c.updateMut.Lock()
	defer c.updateMut.Unlock()

	if !c.serverNeedsUpdate(newArgs) {
		c.args = newArgs
		return nil
	}
	c.shutdownServer()

	err := c.createNewServer(newArgs)
	if err != nil {
		return fmt.Errorf("failed to create new server on update: %v", err)
	}
	go c.runServer()

	c.args = newArgs
	return nil
}

func (c *Component) runServer() {
	c.updateMut.RLock()
	s := c.server
	c.updateMut.RUnlock()

	if s == nil { // already shut down
		return
	}

	err := s.Run()
	level.Warn(c.opts.Logger).Log("msg", "server Run exited", "error", err)
	if err != nil {
		select {
		case c.serverErr <- err:
		default:
		}
	}
}

// createNewServer will create a new server.Server and assign it to the server field.
// It is not goroutine-safe and a updateMut write lock should be held when it's called.
func (c *Component) createNewServer(args Arguments) error {
	config := c.serverConfigForArgs(args)

	// [server.Server] registers new metrics every time it is created. To
	// avoid issues with re-registering metrics with the same name, we create a
	// new registry for the server every time we create one, and pass it to an
	// unchecked collector to bypass uniqueness checking.
	serverRegistry := prometheus.NewRegistry()
	c.uncheckedCollector.SetCollector(serverRegistry)
	config.Registerer = serverRegistry

	s, err := server.New(config)
	if err != nil {
		return fmt.Errorf("failed to create server: %v", err)
	}

	s.HTTP.Path("/api/v1/metrics/write").Methods("POST").HandlerFunc(c.handler.ServeHTTP)
	c.server = s
	return nil
}

// shutdownServer will shut down the currently used server.
// It is not goroutine-safe and a updateMut write lock should be held when it's called.
func (c *Component) shutdownServer() {
	if c.server != nil {
		c.server.Shutdown()
		c.server.Stop()
		c.server = nil
	}
}

func (c *Component) serverConfigForArgs(args Arguments) server.Config {
	return server.Config{
		MetricsNamespace:        "prometheus_source_api",
		HTTPListenAddress:       args.HTTPAddress,
		HTTPListenPort:          args.HTTPPort,
		Log:                     logging.GoKit(c.opts.Logger),
		RegisterInstrumentation: false,
	}
}

func (c *Component) serverNeedsUpdate(args Arguments) bool {
	oldConfig := c.serverConfigForArgs(c.args)
	newConfig := c.serverConfigForArgs(args)
	return !reflect.DeepEqual(newConfig, oldConfig)
}
