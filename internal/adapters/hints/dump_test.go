package hints

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// An existing dump directory is made private, and a symlink at a file name is
// replaced instead of followed.
func TestWriteDumpPrivate(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "hints")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	victim := filepath.Join(base, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("keep"), 0o644))
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	prefix := filepath.Join(dir, when.Format("20060102-150405.000")+"-o")
	require.NoError(t, os.Symlink(victim, prefix+".json"))
	pix := make([]byte, 4*4*4)
	j := &dumpJob{when: when, req: domain.HintScanRequest{Output: "o", Width: 4, Height: 4, Scale: 1}, pix: pix, stride: 16, m: &mapping{}}
	require.NoError(t, writeDump(dir, j))

	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	require.Equal(t, "keep", string(got))
	li, err := os.Lstat(prefix + ".json")
	require.NoError(t, err)
	require.True(t, li.Mode().IsRegular())
	require.Equal(t, os.FileMode(0o600), li.Mode().Perm())
}

func TestDumpSourcesAndCrops(t *testing.T) {
	dir := t.TempDir()
	j := &dumpJob{when: time.Now(), req: domain.HintScanRequest{Output: "test", Width: 80, Height: 80, Scale: 2}, stride: 320, pix: make([]byte, 80*80*4)}
	for i, source := range []domain.HintSource{domain.HintSourceATSPI, domain.HintSourceOCR, domain.HintSourceDetector} {
		j.targets = append(j.targets, domain.HintTarget{Source: source, Kind: domain.HintLink, Window: 1, Score: 1, Rect: domain.Rect{X: 3 + i*10, Y: 3, W: 5, H: 5}})
	}
	j.stages = []scanStage{{Source: "ocr", Windows: []domain.WindowID{1}, Targets: 1, MS: 2, Covered: []domain.WindowID{1}, Crops: []scanCrop{{Window: 1, Source: "ocr", Rect: image.Rect(1, 1, 78, 78), Popups: []image.Rectangle{image.Rect(50, 50, 60, 60)}, Covered: true}}}}
	require.NoError(t, writeDump(dir, j))
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	var meta dumpMeta
	require.NoError(t, json.Unmarshal(data, &meta))
	require.Equal(t, j.stages, meta.Stages)
	require.Equal(t, j.stages[0].Crops, meta.Crops)
	require.Equal(t, []string{"ocr"}, meta.Sources)
	f, err := os.Open(files[0][:len(files[0])-5] + ".annotated.png")
	require.NoError(t, err)
	defer f.Close()
	img, err := png.Decode(f)
	require.NoError(t, err)
	for i, target := range j.targets {
		require.Equal(t, target.Source.String(), meta.Targets[i].Source)
		require.Equal(t, target.Window, meta.Targets[i].Window)
		require.Equal(t, sourceColor(target.Source), color.RGBAModel.Convert(img.At((3+i*10)*2, 6)))
	}
	require.NotEqual(t, sourceColor(domain.HintSourceOCR), boxColor)
}

func TestDumpStageRecordsPartialCoverageAndError(t *testing.T) {
	s := newMocksource(t)
	s.EXPECT().needsFrame(mock.Anything).Return(false)
	s.EXPECT().kind().Return(domain.HintSourceATSPI)
	wins := []domain.HintWindow{{ID: 1, Rect: domain.Rect{W: 20, H: 20}, Popups: []domain.Rect{{X: 20, W: 10, H: 10}}}, {ID: 2, Rect: domain.Rect{X: 40, W: 20, H: 20}}}
	req := domain.HintScanRequest{Width: 100, Height: 80, Scale: 2, Windows: wins}
	s.EXPECT().scan(mock.Anything, req, wins, (*frame)(nil)).Return(scanResult{Targets: []domain.HintTarget{{Window: 1, Source: domain.HintSourceATSPI}}, Covered: []domain.WindowID{1}}, errors.New("partial")).Once()
	w := worker{sources: []source{s}, dumpDir: t.TempDir()}
	captures := make(chan domain.CaptureRequest, 1)
	captured := make(chan domain.CaptureDone, 1)
	captured <- domain.CaptureDone{ID: ports.CaptureIDInternal | 1}
	res := w.serve(context.Background(), req, nil, captures, captured, nil, time.Second)
	require.NoError(t, res.Err)
	require.NotNil(t, w.pending)
	defer w.pending.m.unmap()
	require.Len(t, w.pending.stages, 1)
	stage := w.pending.stages[0]
	require.Equal(t, []domain.WindowID{1, 2}, stage.Windows)
	require.Equal(t, []domain.WindowID{1}, stage.Covered)
	require.Equal(t, "partial", stage.Err)
	require.True(t, stage.Crops[0].Covered)
	require.False(t, stage.Crops[1].Covered)
	require.Equal(t, image.Rect(0, 0, 60, 40), stage.Crops[0].Rect)
	require.Equal(t, []image.Rectangle{image.Rect(40, 0, 60, 20)}, stage.Crops[0].Popups)
}

func TestHintDumpDurations(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	j := &dumpJob{when: when, req: domain.HintScanRequest{Output: "o", Width: 1, Height: 1, Scale: 1}, pix: make([]byte, 4), stride: 4, captureD: 1250 * time.Microsecond, stages: []scanStage{{Source: "atspi", MS: 2.5}, {Source: "detector", MS: 4.25}}}
	require.NoError(t, writeDump(dir, j))
	data, err := os.ReadFile(filepath.Join(dir, when.Format("20060102-150405.000")+"-o.json"))
	require.NoError(t, err)
	var meta dumpMeta
	require.NoError(t, json.Unmarshal(data, &meta))
	require.Equal(t, 1.25, meta.CaptureMS)
	require.Equal(t, 8.0, meta.TotalMS)
	require.Equal(t, j.stages, meta.Stages)
}
