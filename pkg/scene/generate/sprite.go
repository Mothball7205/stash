package generate

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/utils"
)

func (g Generator) SpriteScreenshot(ctx context.Context, input string, seconds float64, size int, isPortrait bool) (image.Image, error) {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	ssOptions := transcoder.ScreenshotOptions{
		OutputPath: "-",
		OutputType: transcoder.ScreenshotOutputTypeBMP,
	}

	if !isPortrait {
		ssOptions.Width = size
	} else {
		ssOptions.Height = size
	}

	args := transcoder.ScreenshotTime(input, seconds, ssOptions)
	img, err := g.generateImage(lockCtx, args)
	if err != nil {
		logger.Warnf("[generator] fast sprite screenshot seek failed for %s at %.3fs, retrying with accurate seek: %v", input, seconds, err)

		ssOptions.SlowSeek = true
		args = transcoder.ScreenshotTime(input, seconds, ssOptions)
		return g.generateImage(lockCtx, args)
	}

	return img, nil
}

func (g Generator) SpriteScreenshotSlow(ctx context.Context, input string, frame int, width int) (image.Image, error) {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	ssOptions := transcoder.ScreenshotOptions{
		OutputPath: "-",
		OutputType: transcoder.ScreenshotOutputTypeBMP,
		Width:      width,
	}

	args := transcoder.ScreenshotFrame(input, frame, ssOptions)

	return g.generateImage(lockCtx, args)
}

func (g Generator) generateImage(lockCtx *fsutil.LockContext, args ffmpeg.Args) (image.Image, error) {
	out, err := g.generateOutput(lockCtx, args)
	if err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("decoding image from ffmpeg: %w", err)
	}

	return img, nil
}

func (g Generator) CombineSpriteImages(images []image.Image) image.Image {
	// Combine all of the thumbnails into a sprite image
	width := images[0].Bounds().Size().X
	height := images[0].Bounds().Size().Y
	gridSize := GetSpriteGridSize(len(images))
	canvasWidth := width * gridSize
	canvasHeight := height * gridSize
	montage := imaging.New(canvasWidth, canvasHeight, color.NRGBA{})
	for index := 0; index < len(images); index++ {
		x := width * (index % gridSize)
		y := height * int(math.Floor(float64(index)/float64(gridSize)))
		img := images[index]
		montage = imaging.Paste(montage, img, image.Pt(x, y))
	}

	return montage
}

// GetSpriteGridSize return the required size of a grid, where the number of images in width
// equals the number of images in height, to hold 'imageCount' images
func GetSpriteGridSize(imageCount int) int {
	return int(math.Ceil(math.Sqrt(float64(imageCount))))
}

func (g Generator) SpriteVTT(ctx context.Context, output string, spritePath string, stepSize float64, spriteChunks int) error {
	lockCtx := g.LockManager.ReadLock(ctx, spritePath)
	defer lockCtx.Cancel()
	return g.generateFile(lockCtx, g.ScenePaths, vttPattern, output, g.spriteVTT(spritePath, stepSize, spriteChunks))
}

func (g Generator) spriteVTT(spritePath string, stepSize float64, spriteChunks int) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		spriteImage, err := os.Open(spritePath)
		if err != nil {
			return err
		}
		defer spriteImage.Close()
		spriteImageName := filepath.Base(spritePath)
		image, _, err := image.DecodeConfig(spriteImage)
		if err != nil {
			return err
		}

		gridSize := GetSpriteGridSize(spriteChunks)
		width := image.Width / gridSize
		height := image.Height / gridSize

		vttLines := []string{"WEBVTT", ""}
		for index := 0; index < spriteChunks; index++ {
			x := width * (index % gridSize)
			y := height * int(math.Floor(float64(index)/float64(gridSize)))
			startTime := utils.GetVTTTime(float64(index) * stepSize)
			endTime := utils.GetVTTTime(float64(index+1) * stepSize)
			vttLines = append(vttLines, startTime+" --> "+endTime)
			vttLines = append(vttLines, fmt.Sprintf("%s#xywh=%d,%d,%d,%d", spriteImageName, x, y, width, height))
			vttLines = append(vttLines, "")
		}
		vtt := strings.Join(vttLines, "\n")

		return os.WriteFile(tmpFn, []byte(vtt), 0644)
	}
}

func (g Generator) GenerateSpriteImage(ctx context.Context, videoFile *ffmpeg.VideoFile, chunkCount int, spriteSize int, isPortrait bool, slowSeek bool, outputPath string) error {
	if videoFile.FrameCount > int64(chunkCount) && !slowSeek {
		logger.Infof("[generator] attempting single-process sprite generation for %s", videoFile.Path)
		err := g.generateSpriteImageSingleProcess(ctx, videoFile, chunkCount, spriteSize, isPortrait, outputPath)
		if err == nil {
			return nil
		}
		logger.Warnf("[generator] single-process sprite generation failed, falling back to slow loop: %v", err)
	}

	return g.GenerateSpriteImageFallback(ctx, videoFile, chunkCount, spriteSize, isPortrait, slowSeek, outputPath)
}

func (g Generator) generateSpriteImageSingleProcess(ctx context.Context, videoFile *ffmpeg.VideoFile, chunkCount int, spriteSize int, isPortrait bool, outputPath string) error {
	var frames []int
	stepSize := videoFile.VideoStreamDuration / float64(chunkCount)
	for i := 0; i < chunkCount; i++ {
		timeVal := float64(i) * stepSize
		frame := int(math.Round(timeVal * videoFile.FrameRate))
		if frame >= int(videoFile.FrameCount) {
			frame = int(videoFile.FrameCount) - 1
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
	if g.FFMpegConfig.GetTranscodeHardwareAcceleration() {
		hwCodec = g.Encoder.HWCodecMP4Compatible()
	}

	var w, h int
	if !isPortrait {
		w = spriteSize
		h = -2
	} else {
		w = -2
		h = spriteSize
	}

	useHW := false
	if hwCodec != nil {
		mvf := &models.VideoFile{
			BaseFile: &models.BaseFile{
				Path:     videoFile.Path,
				Basename: filepath.Base(videoFile.Path),
			},
			Width:    videoFile.Width,
			Height:   videoFile.Height,
		}
		if g.Encoder.HWCanFullHWTranscode(ctx, *hwCodec, mvf, spriteSize) {
			useHW = true
			args = g.Encoder.HWDeviceInit(args, *hwCodec, true)
		}
	}

	args = args.Input(videoFile.Path)
	args = args.VideoFrames(1)

	gridSize := GetSpriteGridSize(chunkCount)
	var vf ffmpeg.VideoFilter
	if useHW {
		vf = ffmpeg.VideoFilter(fmt.Sprintf("select='%s'", selectExpr)).ScaleDimensions(w, h)
		mvf := &models.VideoFile{
			BaseFile: &models.BaseFile{
				Path:     videoFile.Path,
				Basename: filepath.Base(videoFile.Path),
			},
			Width:    videoFile.Width,
			Height:   videoFile.Height,
		}
		vf = g.Encoder.HWCodecFilter(vf, *hwCodec, mvf, true)
		
		switch *hwCodec {
		case ffmpeg.VideoCodecN264, ffmpeg.VideoCodecN264H, ffmpeg.VideoCodecNAv1:
			vf = vf.Append("hwdownload,format=yuv420p")
		default:
			vf = vf.Append("hwdownload,format=nv12")
		}
		
		vf = vf.Append(fmt.Sprintf("tile=%dx%d", gridSize, gridSize))
	} else {
		vf = ffmpeg.VideoFilter(fmt.Sprintf("select='%s'", selectExpr)).ScaleDimensions(w, h)
		vf = vf.Append(fmt.Sprintf("tile=%dx%d", gridSize, gridSize))
	}

	args = args.VideoFilter(vf)

	if strings.HasSuffix(strings.ToLower(outputPath), ".jpg") || strings.HasSuffix(strings.ToLower(outputPath), ".jpeg") {
		args = args.FixedQualityScaleVideo(2)
	}

	args = args.Output(outputPath)

	lockCtx := g.LockManager.ReadLock(ctx, videoFile.Path)
	defer lockCtx.Cancel()

	return g.generateFile(lockCtx, g.ScenePaths, jpgPattern, outputPath, func(lCtx *fsutil.LockContext, tmpFn string) error {
		runArgs := make(ffmpeg.Args, len(args))
		copy(runArgs, args)
		runArgs[len(runArgs)-1] = tmpFn
		
		return g.generate(lCtx, runArgs)
	})
}

func (g Generator) GenerateSpriteImageFallback(ctx context.Context, videoFile *ffmpeg.VideoFile, chunkCount int, spriteSize int, isPortrait bool, slowSeek bool, outputPath string) error {
	var images []image.Image
	if !slowSeek {
		stepSize := videoFile.VideoStreamDuration / float64(chunkCount)
		for i := 0; i < chunkCount; i++ {
			timeVal := float64(i) * stepSize
			img, err := g.SpriteScreenshot(ctx, videoFile.Path, timeVal, spriteSize, isPortrait)
			if err != nil {
				return err
			}
			images = append(images, img)
		}
	} else {
		stepFrame := float64(videoFile.FrameCount-1) / float64(chunkCount)
		for i := 0; i < chunkCount; i++ {
			frame := math.Round(float64(i) * stepFrame)
			img, err := g.SpriteScreenshotSlow(ctx, videoFile.Path, int(frame), spriteSize)
			if err != nil {
				return err
			}
			images = append(images, img)
		}
	}

	montage := g.CombineSpriteImages(images)
	return imaging.Save(montage, outputPath)
}

// TODO - move all sprite generation code here
// WIP
// func (g Generator) Sprite(ctx context.Context, videoFile *ffmpeg.VideoFile, hash string) error {
// 	input := videoFile.Path
// 	if err := g.generateSpriteImage(ctx, videoFile, hash); err != nil {
// 		return fmt.Errorf("generating sprite image for %s: %w", input, err)
// 	}

// 	output := g.ScenePaths.GetSpriteVttFilePath(hash)
// 	if !g.Overwrite {
// 		if exists, _ := fsutil.FileExists(output); exists {
// 			return nil
// 		}
// 	}

// 	if err := g.generateFile(ctx, g.ScenePaths, vttPattern, output, g.spriteVtt(input, screenshotOptions{
// 		Time:    at,
// 		Quality: screenshotQuality,
// 		// default Width is video width
// 	})); err != nil {
// 		return err
// 	}

// 	logger.Debug("created screenshot: ", output)

// 	return nil
// }

// func (g Generator) generateSpriteImage(ctx context.Context, videoFile *ffmpeg.VideoFile, hash string) error {
// 	output := g.ScenePaths.GetSpriteImageFilePath(hash)
// 	if !g.Overwrite {
// 		if exists, _ := fsutil.FileExists(output); exists {
// 			return nil
// 		}
// 	}

// 	var images []image.Image
// 	var err error
// 	if options.VideoDuration > 0 {
// 		images, err = g.generateSprites(ctx, input, options.VideoDuration)
// 	} else {
// 		images, err = g.generateSpritesSlow(ctx, input, options.FrameCount)
// 	}

// 	if len(images) == 0 {
// 		return errors.New("images slice is empty")
// 	}

// 	montage, err := g.combineSpriteImages(images)
// 	if err != nil {
// 		return err
// 	}

// 	if err := imaging.Save(montage, output); err != nil {
// 		return err
// 	}

// 	logger.Debug("created sprite image: ", output)

// 	return nil
// }

// func useSlowSeek(videoFile *ffmpeg.VideoFile) (bool, error) {
// 	// For files with small duration / low frame count  try to seek using frame number intead of seconds
// 	// some files can have FrameCount == 0, only use SlowSeek if duration < 5
// 	if videoFile.Duration < 5 || (videoFile.FrameCount > 0 && videoFile.FrameCount <= int64(spriteChunks)) {
// 		if videoFile.Duration <= 0 {
// 			return false, fmt.Errorf("duration(%.3f)/frame count(%d) invalid", videoFile.Duration, videoFile.FrameCount)
// 		}

// 		logger.Warnf("[generator] video %s too short (%.3fs, %d frames), using frame seeking", videoFile.Path, videoFile.Duration, videoFile.FrameCount)
// 		return true, nil
// 	}
// }

// func (g Generator) combineSpriteImages(images []image.Image) (image.Image, error) {
// 	// Combine all of the thumbnails into a sprite image
// 	width := images[0].Bounds().Size().X
// 	height := images[0].Bounds().Size().Y
// 	canvasWidth := width * spriteCols
// 	canvasHeight := height * spriteRows
// 	montage := imaging.New(canvasWidth, canvasHeight, color.NRGBA{})
// 	for index := 0; index < len(images); index++ {
// 		x := width * (index % spriteCols)
// 		y := height * int(math.Floor(float64(index)/float64(spriteRows)))
// 		img := images[index]
// 		montage = imaging.Paste(montage, img, image.Pt(x, y))
// 	}

// 	return montage, nil
// }

// func (g Generator) generateSprites(ctx context.Context, input string, videoDuration float64) ([]image.Image, error) {
// 	logger.Infof("[generator] generating sprite image for %s", input)
// 	// generate `ChunkCount` thumbnails
// 	stepSize := videoDuration / float64(spriteChunks)

// 	var images []image.Image
// 	for i := 0; i < spriteChunks; i++ {
// 		time := float64(i) * stepSize

// 		img, err := g.spriteScreenshot(ctx, input, time)
// 		if err != nil {
// 			return nil, err
// 		}
// 		images = append(images, img)
// 	}

// 	return images, nil
// }

// func (g Generator) generateSpritesSlow(ctx context.Context, input string, frameCount int) ([]image.Image, error) {
// 	logger.Infof("[generator] generating sprite image for %s (%d frames)", input, frameCount)

// 	stepFrame := float64(frameCount-1) / float64(spriteChunks)

// 	var images []image.Image
// 	for i := 0; i < spriteChunks; i++ {
// 		// generate exactly `ChunkCount` thumbnails, using duplicate frames if needed
// 		frame := math.Round(float64(i) * stepFrame)
// 		if frame >= math.MaxInt || frame <= math.MinInt {
// 			return nil, errors.New("invalid frame number conversion")
// 		}

// 		img, err := g.spriteScreenshotSlow(ctx, input, int(frame))
// 		if err != nil {
// 			return nil, err
// 		}
// 		images = append(images, img)
// 	}

// 	return images, nil
// }

// func (g Generator) spriteScreenshot(ctx context.Context, input string, seconds float64) (image.Image, error) {
// 	ssOptions := transcoder.ScreenshotOptions{
// 		OutputPath: "-",
// 		OutputType: transcoder.ScreenshotOutputTypeBMP,
// 		Width:      spriteScreenshotWidth,
// 	}

// 	args := transcoder.ScreenshotTime(input, seconds, ssOptions)

// 	return g.generateImage(ctx, args)
// }

// func (g Generator) spriteScreenshotSlow(ctx context.Context, input string, frame int) (image.Image, error) {
// 	ssOptions := transcoder.ScreenshotOptions{
// 		OutputPath: "-",
// 		OutputType: transcoder.ScreenshotOutputTypeBMP,
// 		Width:      spriteScreenshotWidth,
// 	}

// 	args := transcoder.ScreenshotFrame(input, frame, ssOptions)

// 	return g.generateImage(ctx, args)
// }

// func (g Generator) spriteVTT(videoFile ffmpeg.VideoFile, spriteImagePath string, slowSeek bool) generateFn {
// 	return func(ctx context.Context, tmpFn string) error {
// 		logger.Infof("[generator] generating sprite vtt for %s", input)

// 		spriteImage, err := os.Open(spriteImagePath)
// 		if err != nil {
// 			return err
// 		}
// 		defer spriteImage.Close()
// 		spriteImageName := filepath.Base(spriteImagePath)
// 		image, _, err := image.DecodeConfig(spriteImage)
// 		if err != nil {
// 			return err
// 		}
// 		width := image.Width / spriteCols
// 		height := image.Height / spriteRows

// 		var stepSize float64
// 		if !slowSeek {
// 			nthFrame = g.NumberOfFrames / g.ChunkCount
// 			stepSize = float64(g.Info.NthFrame) / g.Info.FrameRate
// 		} else {
// 			// for files with a low framecount (<ChunkCount) g.Info.NthFrame can be zero
// 			// so recalculate from scratch
// 			stepSize = float64(videoFile.FrameCount-1) / float64(spriteChunks)
// 			stepSize /= g.Info.FrameRate
// 		}

// 		vttLines := []string{"WEBVTT", ""}
// 		for index := 0; index < spriteChunks; index++ {
// 			x := width * (index % spriteCols)
// 			y := height * int(math.Floor(float64(index)/float64(spriteRows)))
// 			startTime := utils.GetVTTTime(float64(index) * stepSize)
// 			endTime := utils.GetVTTTime(float64(index+1) * stepSize)

// 			vttLines = append(vttLines, startTime+" --> "+endTime)
// 			vttLines = append(vttLines, fmt.Sprintf("%s#xywh=%d,%d,%d,%d", spriteImageName, x, y, width, height))
// 			vttLines = append(vttLines, "")
// 		}
// 		vtt := strings.Join(vttLines, "\n")

// 		return os.WriteFile(tmpFn, []byte(vtt), 0644)
// 	}
// }
