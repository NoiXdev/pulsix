package notifytest

import (
	"fmt"

	"github.com/noixdev/pulsix/internal/notify"
)

// Body is the body of the one JSON request a chat preset renders (backlog
// 4.7's presets), an error when it rendered anything else.
func Body(reqs []notify.Request, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if len(reqs) != 1 || reqs[0].Path != "" || reqs[0].ContentType != "" || len(reqs[0].Header) > 0 {
		return nil, fmt.Errorf("want one plain JSON request, got %+v", reqs)
	}
	return reqs[0].Body, nil
}
