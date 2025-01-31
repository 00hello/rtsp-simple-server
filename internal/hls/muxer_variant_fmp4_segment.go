package hls

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/aler9/gortsplib"

	"github.com/aler9/rtsp-simple-server/internal/hls/fmp4"
)

type partsReader struct {
	parts   []*muxerVariantFMP4Part
	curPart int
	curPos  int
}

func (mbr *partsReader) Read(p []byte) (int, error) {
	n := 0
	lenp := len(p)

	for {
		if mbr.curPart >= len(mbr.parts) {
			return n, io.EOF
		}

		copied := copy(p[n:], mbr.parts[mbr.curPart].content[mbr.curPos:])
		mbr.curPos += copied
		n += copied

		if mbr.curPos == len(mbr.parts[mbr.curPart].content) {
			mbr.curPart++
			mbr.curPos = 0
		}

		if n == lenp {
			return n, nil
		}
	}
}

type muxerVariantFMP4Segment struct {
	lowLatency      bool
	id              uint64
	startTime       time.Time
	startDTS        time.Duration
	segmentMaxSize  uint64
	videoTrack      *gortsplib.TrackH264
	audioTrack      *gortsplib.TrackMPEG4Audio
	genPartID       func() uint64
	onPartFinalized func(*muxerVariantFMP4Part)

	name             string
	size             uint64
	parts            []*muxerVariantFMP4Part
	currentPart      *muxerVariantFMP4Part
	renderedDuration time.Duration
}

func newMuxerVariantFMP4Segment(
	lowLatency bool,
	id uint64,
	startTime time.Time,
	startDTS time.Duration,
	segmentMaxSize uint64,
	videoTrack *gortsplib.TrackH264,
	audioTrack *gortsplib.TrackMPEG4Audio,
	genPartID func() uint64,
	onPartFinalized func(*muxerVariantFMP4Part),
) *muxerVariantFMP4Segment {
	s := &muxerVariantFMP4Segment{
		lowLatency:      lowLatency,
		id:              id,
		startTime:       startTime,
		startDTS:        startDTS,
		segmentMaxSize:  segmentMaxSize,
		videoTrack:      videoTrack,
		audioTrack:      audioTrack,
		genPartID:       genPartID,
		onPartFinalized: onPartFinalized,
		name:            "seg" + strconv.FormatUint(id, 10),
	}

	s.currentPart = newMuxerVariantFMP4Part(
		s.videoTrack,
		s.audioTrack,
		s.genPartID(),
	)

	return s
}

func (s *muxerVariantFMP4Segment) reader() io.Reader {
	return &partsReader{parts: s.parts}
}

func (s *muxerVariantFMP4Segment) getRenderedDuration() time.Duration {
	return s.renderedDuration
}

func (s *muxerVariantFMP4Segment) finalize(
	nextVideoSample *fmp4.VideoSample,
	nextAudioSample *fmp4.AudioSample,
) error {
	err := s.currentPart.finalize()
	if err != nil {
		return err
	}

	if s.currentPart.content != nil {
		s.onPartFinalized(s.currentPart)
		s.parts = append(s.parts, s.currentPart)
	}

	s.currentPart = nil

	if s.videoTrack != nil {
		s.renderedDuration = nextVideoSample.DTS - s.startDTS
	} else {
		s.renderedDuration = 0
		for _, pa := range s.parts {
			s.renderedDuration += pa.renderedDuration
		}
	}

	return nil
}

func (s *muxerVariantFMP4Segment) writeH264(sample *fmp4.VideoSample, adjustedPartDuration time.Duration) error {
	size := uint64(0)
	for _, nalu := range sample.NALUs {
		size += uint64(len(nalu))
	}
	if (s.size + size) > s.segmentMaxSize {
		return fmt.Errorf("reached maximum segment size")
	}
	s.size += size

	s.currentPart.writeH264(sample)

	// switch part
	if s.lowLatency &&
		s.currentPart.duration() >= adjustedPartDuration {
		err := s.currentPart.finalize()
		if err != nil {
			return err
		}

		s.parts = append(s.parts, s.currentPart)
		s.onPartFinalized(s.currentPart)

		s.currentPart = newMuxerVariantFMP4Part(
			s.videoTrack,
			s.audioTrack,
			s.genPartID(),
		)
	}

	return nil
}

func (s *muxerVariantFMP4Segment) writeAAC(sample *fmp4.AudioSample, adjustedPartDuration time.Duration) error {
	size := uint64(len(sample.AU))
	if (s.size + size) > s.segmentMaxSize {
		return fmt.Errorf("reached maximum segment size")
	}
	s.size += size

	s.currentPart.writeAAC(sample)

	// switch part
	if s.lowLatency && s.videoTrack == nil &&
		s.currentPart.duration() >= adjustedPartDuration {
		err := s.currentPart.finalize()
		if err != nil {
			return err
		}

		s.parts = append(s.parts, s.currentPart)
		s.onPartFinalized(s.currentPart)

		s.currentPart = newMuxerVariantFMP4Part(
			s.videoTrack,
			s.audioTrack,
			s.genPartID(),
		)
	}

	return nil
}
