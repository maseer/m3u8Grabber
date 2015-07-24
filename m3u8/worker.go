package m3u8

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

var (
	TotalWorkers = 4
	DlChan       = make(chan *WJob)
	TmpFolder, _ = ioutil.TempDir("", "m3u8worker")
)

type WJobType int

const (
	_ WJobType = iota
	ListDL
	FileDL
)

// LaunchWorkers starts download workers
func LaunchWorkers(wg *sync.WaitGroup, stop <-chan bool) {
	for i := 0; i < TotalWorkers; i++ {
		w := &Worker{id: i, wg: wg}
		go w.Work()
	}

}

type WJob struct {
	Type     WJobType
	URL      string
	DestPath string
	Filename string
	Pos      int
	wg       *sync.WaitGroup
}

type Worker struct {
	id int
	wg *sync.WaitGroup
}

func (w *Worker) Work() {
	log.Printf("worker %d is ready for action\n", w.id)
	for msg := range DlChan {
		w.wg.Add(1)
		w.dispatch(msg)
	}

	log.Printf("worker %d is out", w.id)
}

func (w *Worker) dispatch(job *WJob) {
	defer w.wg.Done()
	switch job.Type {
	case ListDL:
		w.downloadM3u8List(job)
	case FileDL:
		w.downloadM3u8Segment(job)
	default:
		log.Println("format not supported")
		return
	}

}

func (w *Worker) downloadM3u8List(j *WJob) {
	m3f := &M3u8File{Url: j.URL}
	m3f.getSegments("", "")
	j.wg = &sync.WaitGroup{}
	for i, segURL := range m3f.Segments {
		j.wg.Add(1)
		DlChan <- &WJob{
			Type:     FileDL,
			URL:      segURL,
			Pos:      i,
			wg:       j.wg,
			DestPath: j.DestPath,
			Filename: j.Filename,
		}
	}
	j.wg.Wait()
	// put the segments together
	log.Printf("All segments (%d) downloaded!\n", len(m3f.Segments))
	// assemble
	tmpTsFile := j.DestPath + "/" + j.Filename + ".ts"
	if _, err := os.Stat(j.DestPath); err != nil {
		if os.IsNotExist(err) {
			// file does not exist
			if err := os.MkdirAll(j.DestPath, os.ModePerm); err != nil {
				log.Printf("Failed to create path to %s - %s\n", j.DestPath, err)
			}
		} else {
			log.Printf("Failed to create tmp ts file: %s - %s", tmpTsFile, err)
			return
		}
	}
	mp4Path := filepath.Join(j.DestPath, j.Filename) + ".mp4"
	out, err := os.Create(tmpTsFile) //OpenFile(outputFilePath, os.O_APPEND|os.O_WRONLY, os.ModePerm)
	if err != nil {
		log.Printf("Failed to create output ts file - %s - %s\n", tmpTsFile, err)
		return
	}
	defer out.Close()

	log.Printf("Preparing to convert to %s\n", mp4Path)

	for i := 0; i < len(m3f.Segments); i++ {
		file := segmentTmpPath(j.DestPath, j.Filename, i)
		in, err := os.OpenFile(file, os.O_RDONLY, 0666)
		if err != nil {
			log.Printf("Can't open %s because %s\n", file, err)
			return
		}
		_, err = io.Copy(out, in)
		in.Close()
		if err != nil {
			log.Println(err)
			return
		}
		out.Sync()
		err = os.Remove(file)
		if err != nil {
			log.Println(err)
			return
		}
	}
	if err := TsToMp4(tmpTsFile, mp4Path); err != nil {
		log.Println("ts to mp4 error", err)
		return
	}
	log.Printf("Episode available at %s\n", mp4Path)
}

// downloadM3u8Segment downloads one segment of a m3u8 file
func (w *Worker) downloadM3u8Segment(j *WJob) {
	defer func() {
		if j.wg != nil {
			j.wg.Done()
		}
	}()

	log.Printf("worker %d - (%#s)", w.id, filepath.Join(j.DestPath, j.Filename))
	log.Printf(" - segment file %d\n", j.Pos)
	client := &http.Client{}
	resp, err := client.Get(j.URL)
	if err != nil {
		log.Println("Failed to download ", j.URL)
		return
	}

	if resp.StatusCode != 200 {
		log.Println(resp)
		return
	}

	destination := segmentTmpPath(j.DestPath, j.Filename, j.Pos)
	if fileAlreadyExists(destination) {
		return
	}

	if err := os.MkdirAll(j.DestPath, os.ModePerm); err != nil {
		log.Printf("m3u8 download failed - %s\n", err)
		return
	}

	out, err := os.Create(destination)
	if err != nil {
		log.Println(err)
		return
	}
	defer out.Close()
	defer resp.Body.Close()
	_, err = io.Copy(out, resp.Body)
	log.Println("saved", destination)
}

func segmentTmpPath(path, filename string, pos int) string {
	return fmt.Sprintf("%s/%s._%d", TmpFolder, filename, pos)
}
