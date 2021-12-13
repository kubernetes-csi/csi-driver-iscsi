package iscsi

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/prashantv/gostub"
	"github.com/stretchr/testify/assert"
)

func TestExecWithTimeout(t *testing.T) {
	tests := map[string]struct {
		mockedStdout     string
		mockedExitStatus int
		wantTimeout      bool
	}{
		"Success": {
			mockedStdout:     "some output",
			mockedExitStatus: 0,
			wantTimeout:      false,
		},
		"WithError": {
			mockedStdout:     "some\noutput",
			mockedExitStatus: 1,
			wantTimeout:      false,
		},
		"WithTimeout": {
			mockedStdout:     "",
			mockedExitStatus: 0,
			wantTimeout:      true,
		},
		"WithTimeoutAndOutput": {
			mockedStdout:     "should not be returned",
			mockedExitStatus: 0,
			wantTimeout:      true,
		},
		"WithTimeoutAndError": {
			mockedStdout:     "",
			mockedExitStatus: 1,
			wantTimeout:      true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)

			timeout := time.Second
			if tt.wantTimeout {
				timeout = time.Millisecond * 50
			}

			defer gostub.Stub(&execCommandContext, func(ctx context.Context, command string, args ...string) *exec.Cmd {
				if tt.wantTimeout {
					time.Sleep(timeout + time.Millisecond*10)
				}
				return makeFakeExecCommandContext(tt.mockedExitStatus, tt.mockedStdout)(ctx, command, args...)
			}).Reset()

			out, err := ExecWithTimeout("dummy", []string{}, timeout)

			if tt.wantTimeout || tt.mockedExitStatus != 0 {
				assert.NotNil(err)
				if tt.wantTimeout {
					assert.Equal(context.DeadlineExceeded, err)
				}
			} else {
				assert.Nil(err)
			}

			if tt.wantTimeout {
				assert.Equal("", string(out))
			} else {
				assert.Equal(tt.mockedStdout, string(out))
			}
		})
	}
}
