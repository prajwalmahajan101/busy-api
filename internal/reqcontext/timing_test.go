package reqcontext_test

import (
	"context"
	"testing"
	"time"

	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
	"github.com/stretchr/testify/require"
)

func TestTimingAccumulates(t *testing.T) {
	ctx := reqcontext.WithTiming(context.Background())

	reqcontext.AddServiceTime(ctx, 5*time.Millisecond)
	reqcontext.AddServiceTime(ctx, 3*time.Millisecond) // accumulates, not overwrites
	reqcontext.AddRepoTime(ctx, 2*time.Millisecond)

	serviceMS, repoMS := reqcontext.TimingFromContext(ctx)
	require.Equal(t, int64(8), serviceMS)
	require.Equal(t, int64(2), repoMS)
}

func TestTrackHelpersRecordElapsed(t *testing.T) {
	ctx := reqcontext.WithTiming(context.Background())

	func() {
		defer reqcontext.TrackService(ctx)()
		time.Sleep(2 * time.Millisecond)
	}()

	stopRepo := reqcontext.TrackRepo(ctx)
	time.Sleep(2 * time.Millisecond)
	stopRepo()

	serviceMS, repoMS := reqcontext.TimingFromContext(ctx)
	require.GreaterOrEqual(t, serviceMS, int64(1))
	require.GreaterOrEqual(t, repoMS, int64(1))
}

func TestTimingNoAccumulatorIsNoop(t *testing.T) {
	// A ctx without WithTiming must not panic and must report zero.
	ctx := context.Background()
	require.NotPanics(t, func() {
		reqcontext.AddServiceTime(ctx, time.Second)
		reqcontext.AddRepoTime(ctx, time.Second)
	})

	serviceMS, repoMS := reqcontext.TimingFromContext(ctx)
	require.Zero(t, serviceMS)
	require.Zero(t, repoMS)
}
