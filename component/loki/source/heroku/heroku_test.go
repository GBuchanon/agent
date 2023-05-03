package heroku

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/grafana/agent/component"
	"github.com/grafana/agent/component/common/loki"
	fnet "github.com/grafana/agent/component/common/net"
	flow_relabel "github.com/grafana/agent/component/common/relabel"
	"github.com/grafana/agent/component/loki/source/heroku/internal/herokutarget"
	"github.com/grafana/agent/pkg/util"
	"github.com/grafana/regexp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
)

func TestPush(t *testing.T) {
	opts := component.Options{
		Logger:        util.TestFlowLogger(t),
		Registerer:    prometheus.NewRegistry(),
		OnStateChange: func(e component.Exports) {},
	}

	ch1, ch2 := make(chan loki.Entry), make(chan loki.Entry)
	args := Arguments{
		Server: &fnet.ServerConfig{
			HTTP: &fnet.HTTPConfig{
				ListenAddress: address,
				ListenPort:    port,
			},
			// assign random grpc port
			GRPC: &fnet.GRPCConfig{ListenPort: 0},
		},
		UseIncomingTimestamp: false,
		Labels:               map[string]string{"foo": "bar"},
		ForwardTo:            []loki.LogsReceiver{ch1, ch2},
		RelabelRules:         rulesExport,
	}

	// Create and run the component.
	c, err := New(opts, args)
	require.NoError(t, err)

	go c.Run(context.Background())
	time.Sleep(200 * time.Millisecond)

	// Create a Heroku Drain Request and send it to the launched server.
	req, err := http.NewRequest(http.MethodPost, getEndpoint(c.target), strings.NewReader(testPayload))
	require.NoError(t, err)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, res.StatusCode)

	// Check the received log entries
	wantLabelSet := model.LabelSet{"foo": "bar", "host": "host", "app": "heroku", "proc": "router", "log_id": "-"}
	wantLogLine := "at=info method=GET path=\"/\" host=cryptic-cliffs-27764.herokuapp.com request_id=59da6323-2bc4-4143-8677-cc66ccfb115f fwd=\"181.167.87.140\" dyno=web.1 connect=0ms service=3ms status=200 bytes=6979 protocol=https\n"

	for i := 0; i < 2; i++ {
		select {
		case logEntry := <-ch1:
			require.WithinDuration(t, time.Now(), logEntry.Timestamp, 1*time.Second)
			require.Equal(t, wantLogLine, logEntry.Line)
			require.Equal(t, wantLabelSet, logEntry.Labels)
		case logEntry := <-ch2:
			require.WithinDuration(t, time.Now(), logEntry.Timestamp, 1*time.Second)
			require.Equal(t, wantLogLine, logEntry.Line)
			require.Equal(t, wantLabelSet, logEntry.Labels)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "failed waiting for log line")
		}
	}
}

const address = "localhost"
const port = 42421
const testPayload = `270 <158>1 2022-06-13T14:52:23.622778+00:00 host heroku router - at=info method=GET path="/" host=cryptic-cliffs-27764.herokuapp.com request_id=59da6323-2bc4-4143-8677-cc66ccfb115f fwd="181.167.87.140" dyno=web.1 connect=0ms service=3ms status=200 bytes=6979 protocol=https
`

var rulesExport = flow_relabel.Rules{
	{
		SourceLabels: []string{"__heroku_drain_host"},
		Regex:        newRegexp(),
		Action:       flow_relabel.Replace,
		Replacement:  "$1",
		TargetLabel:  "host",
	},
	{
		SourceLabels: []string{"__heroku_drain_app"},
		Regex:        newRegexp(),
		Action:       flow_relabel.Replace,
		Replacement:  "$1",
		TargetLabel:  "app",
	},
	{
		SourceLabels: []string{"__heroku_drain_proc"},
		Regex:        newRegexp(),
		Action:       flow_relabel.Replace,
		Replacement:  "$1",
		TargetLabel:  "proc",
	},
	{
		SourceLabels: []string{"__heroku_drain_log_id"},
		Regex:        newRegexp(),
		Action:       flow_relabel.Replace,
		Replacement:  "$1",
		TargetLabel:  "log_id",
	},
}

func newRegexp() flow_relabel.Regexp {
	re, err := regexp.Compile("^(?:(.*))$")
	if err != nil {
		panic(err)
	}
	return flow_relabel.Regexp{Regexp: re}
}

func getEndpoint(target *herokutarget.HerokuTarget) string {
	return fmt.Sprintf("http://%s:%d%s", address, port, target.DrainEndpoint())
}
