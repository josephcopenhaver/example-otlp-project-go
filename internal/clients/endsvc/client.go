package endsvc

import (
	"context"
	"time"

	"github.com/josephcopenhaver/xit/xnet"
	"github.com/josephcopenhaver/xit/xnet/xhttp"
	"github.com/josephcopenhaver/xit/xslices"
)

type Client struct {
	*xhttp.Client
}

func baseOptions(authHeader string) ([]xhttp.ClientOption, error) {

	op := xhttp.ClientOpts()
	return []xhttp.ClientOption{
		op.ServiceName("end-svc-client"),
		op.PerCallTimeout(5 * time.Second),
	}, nil
}

func New(options ...xhttp.ClientOption) (*Client, error) {
	baseOpts, err := baseOptions("custom-auth-header-value")
	if err != nil {
		return nil, err
	}

	sc, err := xhttp.NewClient(xslices.Concat(baseOpts, options)...)
	if err != nil {
		return nil, err
	}

	return &Client{sc}, nil
}

type GoodbyeResult struct {
	xnet.Response[xhttp.Response] `json:"-"`

	Status string `json:"status"`
}

func (c *Client) Goodbye(ctx context.Context) (GoodbyeResult, error) {
	var resp GoodbyeResult

	op := xhttp.ReqOpts()
	_, err := c.Do(ctx,
		op.Path("goodbye"),
		op.UnmarshalJSONRespTo(&resp),
	)

	return resp, err
}
