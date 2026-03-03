package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/vhs/parser"
	"github.com/charmbracelet/vhs/token"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	overlayPaddingH    = 12
	overlayPaddingV    = 8
	overlayMarginPx    = 16
	overlayBgAlpha     = 166 // ~65% opacity
	overlayDurationSec = 1.0
	overlayFontSize    = 16.0
	overlayFontDPI     = 72.0
	overlayPreRollSec  = 0.2
	overlayPostRollSec = 0.3
)

var defaultOverlayFontFace font.Face

func init() {
	defaultOverlayFontFace = parseOverlayFont(gobold.TTF)
}

// parseOverlayFont parses raw TTF/OTF data into a font.Face for the overlay.
func parseOverlayFont(data []byte) font.Face {
	tt, err := opentype.Parse(data)
	if err != nil {
		return nil
	}
	face, err := opentype.NewFace(tt, &opentype.FaceOptions{
		Size:    overlayFontSize,
		DPI:     overlayFontDPI,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	return face
}

// overlayFontFaceForFamily returns a font.Face for the given font family file
// path, falling back to the default embedded Go Bold font.
func overlayFontFaceForFamily(family string) font.Face {
	if family == "" {
		return defaultOverlayFontFace
	}
	data, err := os.ReadFile(family)
	if err != nil {
		log.Printf("overlay: cannot read font %q, using default", family)
		return defaultOverlayFontFace
	}
	face := parseOverlayFont(data)
	if face == nil {
		log.Printf("overlay: cannot parse font %q, using default", family)
		return defaultOverlayFontFace
	}
	return face
}

// parseOverlayColor converts a hex color string like "#008080" to an NRGBA
// color with the given alpha. Returns a default on parse failure.
func parseOverlayColor(hex string, alpha uint8) color.NRGBA {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 6 {
		if val, err := strconv.ParseUint(hex, 16, 32); err == nil {
			return color.NRGBA{
				R: uint8(val >> 16),
				G: uint8(val >> 8),
				B: uint8(val),
				A: alpha,
			}
		}
	}
	// Fallback to teal.
	return color.NRGBA{0x00, 0x80, 0x80, alpha}
}

// overlaySegment tracks a contiguous range of frames that display a keypress
// overlay badge with the given label.
type overlaySegment struct {
	Label      string
	StartFrame int // 1-based, matching frame file numbering
	EndFrame   int // 1-based, inclusive
}

// formatKeypressLabel formats a modifier key command as a human-readable label
// such as "Ctrl+C" or "Ctrl+Shift+K".
func formatKeypressLabel(cmdType parser.CommandType, args string) string {
	parts := []string{token.ToCamel(string(cmdType))}
	for _, arg := range strings.Split(args, " ") {
		if len(arg) == 1 {
			parts = append(parts, strings.ToUpper(arg))
		} else {
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, "+")
}

// renderOverlayBadge creates a small badge image with a semi-transparent
// rounded background and white text label.
func renderOverlayBadge(label string, borderRadius int, bgHex string, fontFamily string) *image.RGBA {
	face := overlayFontFaceForFamily(fontFamily)
	if face == nil || label == "" {
		return nil
	}

	// Measure text dimensions.
	textWidth := font.MeasureString(face, label).Ceil()
	metrics := face.Metrics()
	textHeight := (metrics.Ascent + metrics.Descent).Ceil()

	// Badge dimensions.
	badgeW := textWidth + 2*overlayPaddingH
	badgeH := textHeight + 2*overlayPaddingV

	// Clamp border radius to half the smallest badge dimension.
	radius := borderRadius
	if max := badgeH / 2; radius > max {
		radius = max
	}
	if max := badgeW / 2; radius > max {
		radius = max
	}

	badge := image.NewRGBA(image.Rect(0, 0, badgeW, badgeH))

	// Draw rounded rect background using the existing roundedrect mask.
	bgColor := parseOverlayColor(bgHex, overlayBgAlpha)
	mask := &roundedrect{
		pa:     image.Point{0, 0},
		pb:     image.Point{badgeW, badgeH},
		radius: radius,
	}
	draw.DrawMask(badge, badge.Bounds(), &image.Uniform{bgColor},
		image.Point{}, mask, image.Point{}, draw.Over)

	// Draw text label.
	d := &font.Drawer{
		Dst:  badge,
		Src:  image.NewUniform(color.White),
		Face: face,
		Dot: fixed.Point26_6{
			X: fixed.I(overlayPaddingH),
			Y: fixed.I(overlayPaddingV) + metrics.Ascent,
		},
	}
	d.DrawString(label)

	return badge
}

// reencodeFrame reads a PNG from disk and writes it back through Go's encoder
// to ensure a consistent pixel format across the frame sequence.
func reencodeFrame(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, src)
}

// applyOverlayToFrame reads a text frame PNG from disk, composites the badge
// in the lower-right corner, and writes it back.
func applyOverlayToFrame(path string, badge *image.RGBA) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}

	bounds := src.Bounds()
	dst := image.NewNRGBA(bounds)
	draw.Draw(dst, bounds, src, bounds.Min, draw.Src)

	// Position badge in lower-right corner with margin.
	badgeW := badge.Bounds().Dx()
	badgeH := badge.Bounds().Dy()
	badgeX := bounds.Max.X - badgeW - overlayMarginPx
	badgeY := bounds.Max.Y - badgeH - overlayMarginPx
	dstRect := image.Rect(badgeX, badgeY, badgeX+badgeW, badgeY+badgeH)
	draw.Draw(dst, dstRect, badge, image.Point{}, draw.Over)

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, dst)
}

// applyOverlaySegments re-encodes all text frames for pixel format consistency
// and composites overlay badges onto the appropriate frames.
func applyOverlaySegments(segments []overlaySegment, borderRadius int, bgHex, fontFamily, inputDir string, startFrame, endFrame, framerate int) {
	// Compute pre/post roll in frames.
	preRoll := int(float64(framerate) * overlayPreRollSec)
	postRoll := int(float64(framerate) * overlayPostRollSec)

	// Expand segments with pre/post roll, clamping to valid range.
	// Later segments replace earlier ones if they overlap.
	expanded := make([]overlaySegment, len(segments))
	copy(expanded, segments)
	for i := range expanded {
		expanded[i].StartFrame -= preRoll
		if expanded[i].StartFrame < startFrame {
			expanded[i].StartFrame = startFrame
		}
		expanded[i].EndFrame += postRoll
		if expanded[i].EndFrame > endFrame {
			expanded[i].EndFrame = endFrame
		}
	}

	// Build a map from frame number to badge for quick lookup.
	badgeCache := make(map[string]*image.RGBA)
	frameBadge := make(map[int]*image.RGBA)

	for _, seg := range expanded {
		badge, ok := badgeCache[seg.Label]
		if !ok {
			badge = renderOverlayBadge(seg.Label, borderRadius, bgHex, fontFamily)
			badgeCache[seg.Label] = badge
		}
		if badge == nil {
			continue
		}
		for frame := seg.StartFrame; frame <= seg.EndFrame; frame++ {
			frameBadge[frame] = badge
		}
	}

	// Re-encode every text frame. Frames with a badge get the overlay
	// composited; all others are simply re-encoded through Go's PNG
	// encoder so that ffmpeg sees a consistent pixel format.
	for frame := startFrame; frame <= endFrame; frame++ {
		path := filepath.Join(inputDir, fmt.Sprintf(textFrameFormat, frame))
		if badge, ok := frameBadge[frame]; ok {
			if err := applyOverlayToFrame(path, badge); err != nil {
				log.Printf("overlay: %v", err)
			}
		} else {
			if err := reencodeFrame(path); err != nil {
				log.Printf("overlay reencode: %v", err)
			}
		}
	}
}
