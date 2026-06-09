package videophash

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/corona10/goimagehash"
	"github.com/disintegration/imaging"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

const (
	screenshotSize = 160
	columns        = 5
	rows           = 5
)

func Generate(encoder *ffmpeg.FFMpeg, videoFile *models.VideoFile, useHW bool) (*uint64, error) {
	sprite, err := generateSprite(encoder, videoFile, useHW)
	if err != nil {
		return nil, err
	}

	hash, err := goimagehash.PerceptionHash(sprite)
	if err != nil {
		return nil, fmt.Errorf("computing phash from sprite: %w", err)
	}
	hashValue := hash.GetHash()
	return &hashValue, nil
}

func generateSpriteScreenshot(encoder *ffmpeg.FFMpeg, input string, t float64, slowSeek bool) (image.Image, error) {
	options := transcoder.ScreenshotOptions{
		Width:      screenshotSize,
		OutputPath: "-",
		OutputType: transcoder.ScreenshotOutputTypeBMP,
		SlowSeek:   slowSeek,
	}

	args := transcoder.ScreenshotTime(input, t, options)
	data, err := encoder.GenerateOutput(context.Background(), args, nil)
	if err != nil {
		return nil, err
	}

	reader := bytes.NewReader(data)

	img, _, err := image.Decode(reader)
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	return img, nil
}

func combineImages(images []image.Image) image.Image {
	width := images[0].Bounds().Size().X
	height := images[0].Bounds().Size().Y
	canvasWidth := width * columns
	canvasHeight := height * rows
	montage := imaging.New(canvasWidth, canvasHeight, color.NRGBA{})
	for index := 0; index < len(images); index++ {
		x := width * (index % columns)
		y := height * int(math.Floor(float64(index)/float64(rows)))
		img := images[index]
		montage = imaging.Paste(montage, img, image.Pt(x, y))
	}

	return montage
}

func generateSprite(encoder *ffmpeg.FFMpeg, videoFile *models.VideoFile, useHW bool) (image.Image, error) {
	if videoFile.FrameRate > 0 && videoFile.Duration*videoFile.FrameRate > 25 {
		logger.Infof("[generator] attempting single-process phash sprite generation for %s", videoFile.Path)
		img, err := generateSpriteSingleProcess(encoder, videoFile, useHW)
		if err == nil {
			return img, nil
		}
		logger.Warnf("[generator] single-process phash sprite generation failed, falling back to slow loop: %v", err)
	}

	return generateSpriteFallback(encoder, videoFile)
}

func generateSpriteSingleProcess(encoder *ffmpeg.FFMpeg, videoFile *models.VideoFile, useHW bool) (image.Image, error) {
	chunkCount := columns * rows
	offset := 0.05 * videoFile.Duration
	stepSize := (0.9 * videoFile.Duration) / float64(chunkCount)

	var frames []int
	for i := 0; i < chunkCount; i++ {
		timeVal := offset + (float64(i) * stepSize)
		frame := int(math.Round(timeVal * videoFile.FrameRate))
		frameCount := int(videoFile.Duration * videoFile.FrameRate)
		if frame >= frameCount {
			frame = frameCount - 1
		}
		if frame < 0 {
			frame = 0
		}
		frames = append(frames, frame)
	}

	selectParts := make([]string, len(frames))
	for idx, fNum := range frames {
		selectParts[idx] = fmt.Sprintf("eq(n\\,%d)", fNum)
	}
	selectExpr := strings.Join(selectParts, "+")

	var args ffmpeg.Args
	args = args.LogLevel(ffmpeg.LogLevelError)
	args = args.Overwrite()

	var hwCodec *ffmpeg.VideoCodec
	if useHW {
		hwCodec = encoder.HWCodecMP4Compatible()
	}

	useHWTranscode := false
	if hwCodec != nil {
		if encoder.HWCanFullHWTranscode(context.Background(), *hwCodec, videoFile, screenshotSize) {
			useHWTranscode = true
			args = encoder.HWDeviceInit(args, *hwCodec, true)
		}
	}

	args = args.Input(videoFile.Path)
	args = args.VideoFrames(1)

	var vf ffmpeg.VideoFilter
	if useHWTranscode {
		vf = ffmpeg.VideoFilter(fmt.Sprintf("select='%s'", selectExpr)).ScaleDimensions(screenshotSize, -2)
		vf = encoder.HWCodecFilter(vf, *hwCodec, videoFile, true)

		switch *hwCodec {
		case ffmpeg.VideoCodecN264, ffmpeg.VideoCodecN264H, ffmpeg.VideoCodecNAv1:
			vf = vf.Append("hwdownload,format=yuv420p")
		default:
			vf = vf.Append("hwdownload,format=nv12")
		}

		vf = vf.Append(fmt.Sprintf("tile=%dx%d", columns, rows))
	} else {
		vf = ffmpeg.VideoFilter(fmt.Sprintf("select='%s'", selectExpr)).ScaleDimensions(screenshotSize, -2)
		vf = vf.Append(fmt.Sprintf("tile=%dx%d", columns, rows))
	}

	args = args.VideoFilter(vf)
	args = args.AppendArgs(transcoder.ScreenshotOutputTypeBMP)
	args = args.Output("-")

	data, err := encoder.GenerateOutput(context.Background(), args, nil)
	if err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decoding image from tiled output: %w", err)
	}

	return img, nil
}

func generateSpriteFallback(encoder *ffmpeg.FFMpeg, videoFile *models.VideoFile) (image.Image, error) {
	logger.Infof("[generator] generating phash sprite for %s using fallback loop", videoFile.Path)

	chunkCount := columns * rows
	offset := 0.05 * videoFile.Duration
	stepSize := (0.9 * videoFile.Duration) / float64(chunkCount)
	var images []image.Image
	slowSeek := false

	for i := 0; i < chunkCount; i++ {
		time := offset + (float64(i) * stepSize)

		img, err := generateSpriteScreenshot(encoder, videoFile.Path, time, slowSeek)
		if err != nil && !slowSeek {
			logger.Warnf("[generator] fast phash screenshot seek failed for %s at %.3fs, retrying with accurate seek for remaining phash screenshots: %v", videoFile.Path, time, err)

			slowSeek = true
			img, err = generateSpriteScreenshot(encoder, videoFile.Path, time, slowSeek)
		}
		if err != nil {
			return nil, fmt.Errorf("generating sprite screenshot: %w", err)
		}

		images = append(images, img)
	}

	// Combine all of the thumbnails into a sprite image
	if len(images) == 0 {
		return nil, fmt.Errorf("images slice is empty, failed to generate phash sprite for %s", videoFile.Path)
	}

	return combineImages(images), nil
}
