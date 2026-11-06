package screencast

import (
	"testing"

	"bnema.dev/purego-pipewire/pipewire"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestCurrentBuffersRecoveryPreservesReleaseOwner(t *testing.T) {
	a := portsmocks.NewMockCastBufferAllocator(t)
	b := portsmocks.NewMockCastBufferAllocator(t)
	first, firstFile := tempCast(t, domain.CastXRGB8888, 64, 32, 1, 8192)
	second, secondFile := tempCast(t, domain.CastXRGB8888, 64, 32, 1, 8192)
	formats := []domain.DMABufFormat{{Format: domain.CastXRGB8888, Modifier: 7}}
	a.EXPECT().CastFormats().Return(formats).Once()
	b.EXPECT().CastFormats().Return(formats).Once()
	a.EXPECT().AllocCast(domain.CastXRGB8888, 64, 32, uint64(7)).Return(first, nil).Once()
	b.EXPECT().AllocCast(domain.CastXRGB8888, 64, 32, uint64(7)).Return(second, nil).Once()
	a.EXPECT().ReleaseCast(first).Once()
	b.EXPECT().ReleaseCast(second).Once()
	var current ports.CastBufferAllocator = a
	buffers := CurrentBuffers(func() ports.CastBufferAllocator { return current })
	alloc := castAllocator{dev: buffers}
	require.Equal(t, formats, buffers.CastFormats())
	var out pipewire.VideoPlanes
	require.NoError(t, alloc.Alloc(0, pipewire.VideoFormatBGRx, 64, 32, 7, &out))
	current = b
	require.Equal(t, formats, buffers.CastFormats())
	// Renegotiation replaces the old allocation after recovery. Its release
	// must still reach A, while the replacement must be allocated on B.
	require.NoError(t, alloc.Alloc(0, pipewire.VideoFormatBGRx, 64, 32, 7, &out))
	require.True(t, closed(firstFile))
	alloc.freeAll()
	require.True(t, closed(secondFile))
}
