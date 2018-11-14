package main

import (
	"Kowl/js"
	"github.com/fsnotify/fsnotify"
	"github.com/robertkrimen/otto"
	"github.com/robertkrimen/otto/underscore"
	"gopkg.in/h2non/gentleman.v2"
	"gopkg.in/h2non/gentleman.v2/plugins/body"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"
)

func main() {

	if len(os.Args) != 3 {
		log.Fatalln("kowl [filename]  [script]")
	}

	filename := os.Args[1]
	script := os.Args[2]

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalln(err)
	}
	defer watcher.Close()

	waitAndAddWatcher(script, filename, watcher)

	renameCh := make(chan bool)
	removeCh := make(chan bool)
	errCh := make(chan error)

	go func() {
		for {
			select {
			case event := <-watcher.Events:
				switch {
				case event.Op&fsnotify.Write == fsnotify.Write:
					runjs(script, "WRITE", event.Name)
				case event.Op&fsnotify.Create == fsnotify.Create:
					runjs(script, "CREATE", event.Name)
				case event.Op&fsnotify.Remove == fsnotify.Remove:
					runjs(script, "REMOVE", event.Name)
					removeCh <- true
				case event.Op&fsnotify.Rename == fsnotify.Rename:
					runjs(script, "RENAME", event.Name)
					renameCh <- true
				case event.Op&fsnotify.Chmod == fsnotify.Chmod:
					runjs(script, "CHMOD", event.Name)
				}
			case err := <-watcher.Errors:
				errCh <- err
			}
		}
	}()

	go func() {
		for {
			select {
			case <-renameCh:
				waitAndAddWatcher(script, filename, watcher)
			case <-removeCh:
				waitAndAddWatcher(script, filename, watcher)
			}
		}
	}()

	log.Fatalln(<-errCh)
}

func waitUntilFind(script, filename string) error {
	b := false
	for {
		time.Sleep(1 * time.Second)
		_, err := os.Stat(filename)
		if err != nil {
			if os.IsNotExist(err) {
				b = true;
				runjs(script, "NOT_FOUND", filename)
				continue
			} else {
				return err
			}
		}
		if b {
			runjs(script, "EXIST", filename)
		}
		break
	}
	return nil
}

func waitAndAddWatcher(script, filename string, watcher *fsnotify.Watcher) {
	err := waitUntilFind(script, filename)
	if err != nil {
		log.Fatalln(err)
	}
	err = watcher.Add(filename)
	if err != nil {
		log.Fatalln(err)
	}
}

func runjs(script string, op string, name string) {
	vm := otto.New()
	underscore.Enable()
	cli := gentleman.New()

	b, err := ioutil.ReadFile(script) // just pass the file name
	if err != nil {
		log.Fatalln(err)
	}
	code := string(b)

	vm.Set("kExec", js.KExec)

	vm.Set("kFileToString", js.KFileToString)
	vm.Set("kStringToFile", js.KStringToFile)
	vm.Set("kAppendFile", js.KAppendFile)
	vm.Set("kRemoveFile", js.KRemoveFile)

	vm.Set("kEncrypt", js.KEncrypt)
	vm.Set("kDecrypt", js.KDecrypt)

	vm.Set("kCli", cli)
	vm.Set("kBodyJSON", body.JSON)
	vm.Set("kBodyXML", body.XML)
	vm.Set("kBodyString", body.String)

	vm.Set("kGetEnv",os.Getenv)
	vm.Set("kSetEnv",os.Setenv)
	vm.Set("kHostname",os.Hostname)
	vm.Set("kGetpid",os.Getpid)
	vm.Set("kGetppid",os.Getppid)
	vm.Set("kGetgid",os.Getgid)
	vm.Set("kGetuid",os.Getuid)
	vm.Set("kGetegid",os.Getegid)
	vm.Set("kArgs",os.Args)

	



	vm.Run(code)

	vm.Call(strings.ToLower(op), nil, name, op, os.Args)

}
