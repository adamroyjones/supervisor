package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pkg/errors"
)

// TODO: Add stdin, stdout, stderr pipes and let the supervisor manage them.
// TODO: Add signal handling.
type Supervisor struct {
	ServiceName, In, Out string

	Watcher *fsnotify.Watcher
	Process *os.Process

	ErrorChan      chan error
	BuildReadyChan chan bool
	KillReadyChan  chan bool
}

// TODO: Switch to using `ldflags` to pass the version in at build time.
const version string = "0.0.1"

func main() {
	// Startup: parse flags & print version information.
	{
		versionStr := fmt.Sprintf("supervisor: Version %s.", version)
		versionPtr := flag.Bool("version", false, "Print version information and exit.")
		flag.Parse()
		if *versionPtr {
			fmt.Println(versionStr)
			os.Exit(0)
		}
		log.Println(versionStr)
	}

	// This will block until `.mtime` is present in the source directory.
	s, err := NewSupervisor()
	if err != nil {
		s.Exit(err)
	}

	// The first build happens here.
	go s.Build()
	select {
	case err := <-s.ErrorChan:
		// TODO: Add in code to recover?
		s.Exit(err)
	case <-s.BuildReadyChan:
		// No-op: continue execution.
	}

	// The first run happens here.
	go s.Run()

	// This is a watch-compile-run loop.
	s.WatchLoop()
}

func NewSupervisor() (*Supervisor, error) {
	s := &Supervisor{}

	podName, isSet := os.LookupEnv("POD_NAME")
	if !isSet {
		return s, fmt.Errorf("Expected `POD_NAME` to be set as an environment variable.")
	}

	podNameComponents := strings.Split(podName, "-")
	n := len(podNameComponents)
	if n < 3 {
		return s, fmt.Errorf("Expected `POD_NAME` (value: %s) to have 3 or more dashes.", podName)
	}

	serviceName := strings.Join(podNameComponents[:n-2], "-") + "-service"
	s.ServiceName = serviceName
	s.In = filepath.Join("/usr/local/src", serviceName)
	s.Out = filepath.Join("/usr/local/bin", serviceName)
	err := os.MkdirAll(s.In, 0o755)
	if err != nil {
		return s, fmt.Errorf("Failed to create the directory '%s': %v", s.In, err)
	}

	// Wait until the required files exist.
	// TODO: Switch to github.com/fsnotify/fsnotify for this? Can a notification be set up for a file that doesn't yet exist?
	mtimeFile := filepath.Join(s.In, ".mtime")
	{
		log.Printf("Waiting until the file '%s' can be found.", mtimeFile)

		for {
			_, mtimeFileErr := os.Stat(mtimeFile)

			if mtimeFileErr == nil {
				log.Println("Files found. Continuing...")
				break
			}

			if mtimeFileErr != nil && !os.IsNotExist(mtimeFileErr) {
				return s, fmt.Errorf("Error examining file '%s': %v", mtimeFile, mtimeFileErr)
			}

			time.Sleep(time.Second)
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return s, errors.Wrap(err, "error creating a watcher")
	}
	watcher.Add(mtimeFile)

	s.Watcher = watcher
	s.ErrorChan = make(chan error)
	s.BuildReadyChan = make(chan bool)
	s.KillReadyChan = make(chan bool)

	return s, nil
}

func (s *Supervisor) Build() {
	log.Println("Building...")
	cmd := exec.Command("go", "build", "-o", s.Out, s.In)
	cmd.Dir = s.In
	if err := cmd.Run(); err != nil {
		s.ErrorChan <- errors.Wrap(err, "error building the binary")
		return
	}
	log.Println("Finished building.")
	s.BuildReadyChan <- true
}

// TODO: I think this can leak messages after the parent process exits. Ideally, the supervisor itself would manage the printing and logging of messages.
func (s *Supervisor) Run() {
	cmd := exec.Command(s.Out)
	cmd.Stdin = os.Stdin
	// TODO: Uncomment.
	// cmd.Stdout = os.Stdout
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		s.ErrorChan <- errors.Wrap(err, "error running program")
		return
	}

	s.Process = cmd.Process
	go func() { s.KillReadyChan <- true }()
	if err := cmd.Wait(); err != nil {
		s.ErrorChan <- errors.Wrap(err, "error waiting for the program to complete")
	}
}

func (s *Supervisor) RebuildAndRun() {
	log.Println("Change to source files detected.")
	go s.Build()
	// TODO: Add support for timeouts, given that this can block indefinitely?
	<-s.KillReadyChan

	// TODO: Kill the process more elegantly?
	log.Println("Killing the old process...")
	err := s.Process.Kill()
	if err != nil {
		s.ErrorChan <- errors.Wrap(err, fmt.Sprintf("error killing the process (PID %d)", s.Process.Pid))
		return
	}

	<-s.BuildReadyChan
	go s.Run()
}

func (s *Supervisor) WatchLoop() {
	for {
		select {
		case err := <-s.ErrorChan:
			// TODO: Add in code to recover?
			s.Exit(err)
		case event, ok := <-s.Watcher.Events:
			if !ok {
				s.ErrorChan <- fmt.Errorf("The events channel for the watcher has been unexpectedly closed.")
				continue
			}

			switch event.Op {
			case fsnotify.Chmod:
				go s.RebuildAndRun()
			default:
				s.ErrorChan <- fmt.Errorf("Unexpected event received: %v", event)
			}
		case err, ok := <-s.Watcher.Errors:
			if !ok {
				s.ErrorChan <- fmt.Errorf("The errors channel for the watcher has been unexpectedly closed.")
				continue
			}

			if err != nil {
				s.ErrorChan <- errors.Wrap(err, "error received on the watcher's errors channel")
			}
		}
	}
}

func (s *Supervisor) Exit(err error) {
	if s.Watcher != nil {
		if err := s.Watcher.Close(); err != nil {
			log.Printf("Error closing the file watcher: %v", err)
		}
	}

	log.Printf("Exiting as an error was encountered: %v", err)
	os.Exit(1)
}
