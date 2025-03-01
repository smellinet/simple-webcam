package raspivid

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sentry-picam/broker"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ricochet2200/go-disk-usage/du"
)

// Recorder writes the video stream to disk
type Recorder struct {
	RequestedRecord bool
	StopTime        time.Time
	HighlightTime   time.Time
	hasFfmpeg       bool
	MinFreeSpace    uint64
	IsFreeingSpace  sync.Mutex
}

func getFilename(lastName string) string {
	fileFormat := "%d-%02d-%02d-%02d%02d"
	now := time.Now()
	newFilename := fmt.Sprintf(fileFormat, now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute())
	if newFilename == lastName {
		return newFilename + fmt.Sprintf("_%02d", now.Second())
	}

	return fmt.Sprintf(fileFormat+"_%02d", now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second())
}

func (rec *Recorder) checkFfmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	if err == nil {
		rec.hasFfmpeg = true
	} else {
		log.Println("ffmpeg not found - no thumbnails will be created")
	}

	return rec.hasFfmpeg
}

func (rec *Recorder) Maintenance(folder string) {
	rec.IsFreeingSpace.Lock()
	defer rec.IsFreeingSpace.Unlock()
	usage := du.NewDiskUsage(folder)
	freeSpace := usage.Available()

	if freeSpace > rec.MinFreeSpace {
		return
	}

	for freeSpace < rec.MinFreeSpace {
		rec.deleteOldest(folder, freeSpace)
		usage = du.NewDiskUsage(folder)
		freeSpace = usage.Available()
	}
}

func (rec *Recorder) deleteOldest(folder string, freeSpace uint64) {
	folders, err := os.ReadDir(folder)
	if err != nil {
		return
	}

	for _, f := range folders {
		if f.IsDir() {
			files, err := os.ReadDir(folder + f.Name())
			if err != nil {
				log.Fatal(err)
			}

			if len(files) == 0 {
				os.Remove(folder + f.Name())
				return
			}

			for _, v := range files {
				extension := filepath.Ext(strings.ToLower(v.Name()))
				name := strings.TrimSuffix(filepath.Base(v.Name()), filepath.Ext(v.Name()))

				if extension == ".mp4" {
					os.Remove(folder + f.Name() + "/" + name + ".mp4")
					os.Remove(folder + f.Name() + "/" + name + ".jpg")
					log.Println("Low free space (" + strconv.FormatUint(freeSpace/1024, 10) + " KiB free). Deleted oldest recording: " + name)
					return
				}
			}
		}
	}
}

// Init initializes the raspivid recorder. folderpath must include the trailing slash
// When recording is triggered by (rec.StopTime > now), up to numHeaders Iframes will be
// saved before the trigger
func (rec *Recorder) Init(caster *broker.Broker, folderpath string, framerate int, triggerScript string) {
	os.MkdirAll(folderpath+"raw/", 0700)

	converter := Converter{}
	converter.Framerate = framerate
	converter.TriggerScript = triggerScript
	converter.Init(rec, folderpath)
	if rec.checkFfmpeg() {
		go converter.convertFolder(folderpath)
	}

	extension := ".h264"
	stream := caster.Subscribe()
	defer caster.Unsubscribe(stream)
	numHeaders := 0

	var f *os.File
	var fileName string
	var startTime time.Time

	buf := [][]byte{}
	i := 0
	frameOffset := 0
	startedFile := false
	for {
		x := <-stream

		if rec.RequestedRecord {
			if time.Now().Before(rec.StopTime) {
				if !startedFile {
					fileName = getFilename(fileName)
					f, _ = os.Create(folderpath + "raw/" + fileName + extension)
					startTime = time.Now()
					frameOffset = i
				}

				startedFile = true

				for _, v := range buf {
					_, err := f.Write(v)
					if err != nil {
						log.Println(err)
					}
				}
				buf = buf[:0]
				numHeaders = 0
				i = 0
			} else if startedFile {
				f.Close()
				go func(highlightTime time.Time, startTime time.Time, frameOffset int) {
					converter.CacheItem(fileName, highlightTime.Sub(startTime).Seconds()+float64(frameOffset)/float64(framerate)-.25)
					converter.convertFile(fileName)
				}(rec.HighlightTime, startTime, frameOffset)

				go rec.Maintenance(folderpath)

				startedFile = false
			}
		}

		if x.([]byte)[4] == 39 { // always start with SPS header
			if numHeaders == 2 {
				buf = buf[i:]
				numHeaders = 0
				i = 0
			}
			numHeaders++
		}

		buf = append(buf, x.([]byte))
		i++
	}
}
