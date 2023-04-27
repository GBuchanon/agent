package api

import (
	"context"
	"fmt"
	"github.com/go-kit/log/level"
	"github.com/grafana/agent/component"
	"github.com/grafana/agent/component/prometheus"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/storage/remote"
	"github.com/weaveworks/common/logging"
	"github.com/weaveworks/common/server"
	"net/http"
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

func New(opts component.Options, args Arguments) (component.Component, error) {
	return &Component{
		opts:   opts,
		args:   args,
		fanout: prometheus.NewFanout(args.ForwardTo, opts.ID, opts.Registerer),
	}, nil
}

type Arguments struct {
	HTTPAddress string               `river:"http_address,attr"`
	HTTPPort    int                  `river:"http_port,attr"`
	ForwardTo   []storage.Appendable `river:"forward_to,attr"`
}

type Component struct {
	opts   component.Options
	args   Arguments
	fanout *prometheus.Fanout
}

func (c *Component) Run(ctx context.Context) error {
	s, err := server.New(server.Config{
		MetricsNamespace:        "prometheus_source_api",
		HTTPListenAddress:       c.args.HTTPAddress,
		HTTPListenPort:          c.args.HTTPPort,
		Log:                     logging.GoKit(c.opts.Logger),
		Registerer:              c.opts.Registerer,
		RegisterInstrumentation: false,
	})
	defer func() {
		s.Shutdown()
		s.Stop()
	}()

	s.HTTP.Path("/api/v1/metrics/write").Methods("POST").HandlerFunc(c.handlePushRequest)

	go func() {
		// TODO: recovery mechanism when server dies
		err := s.Run()
		if err != nil {
			level.Error(c.opts.Logger).Log("msg", "server exit with error", "err", err)
		}
	}()

	if err != nil {
		return fmt.Errorf("failed to create server: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			level.Info(c.opts.Logger).Log("msg", "context done - exiting")
			return nil
		}
	}
}

func (c *Component) Update(args component.Arguments) error {
	//TODO implement me
	return nil
}

func (c *Component) handlePushRequest(w http.ResponseWriter, r *http.Request) {
	// TODO: don't create new one all the time
	handler := remote.NewWriteHandler(c.opts.Logger, c.fanout)
	handler.ServeHTTP(w, r)
}
