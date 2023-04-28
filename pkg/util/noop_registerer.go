package util

import "github.com/prometheus/client_golang/prometheus"

func NewNoopRegisterer() prometheus.Registerer { return &noopRegisterer{} }

type noopRegisterer struct{}

func (n noopRegisterer) Register(_ prometheus.Collector) error { return nil }

func (n noopRegisterer) MustRegister(_ ...prometheus.Collector) {}

func (n noopRegisterer) Unregister(_ prometheus.Collector) bool { return true }
