package main

import (
	"bytes"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/robertkrimen/otto"
	"github.com/robertkrimen/otto/underscore"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
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

	waitAndAddWatcher(script,filename,watcher)

	renameCh := make(chan bool)
	removeCh := make(chan bool)
	errCh 	 := make(chan error)

	go func() {
		for {
			select {
			case event := <-watcher.Events:
				switch {
				case event.Op&fsnotify.Write == fsnotify.Write:
					runjs(script,"WRITE", event.Name)
				case event.Op&fsnotify.Create == fsnotify.Create:
					runjs(script,"CREATE", event.Name)
				case event.Op&fsnotify.Remove == fsnotify.Remove:
					runjs(script,"REMOVE", event.Name)
					removeCh <- true
				case event.Op&fsnotify.Rename == fsnotify.Rename:
					runjs(script,"RENAME", event.Name)
					renameCh <- true
				case event.Op&fsnotify.Chmod == fsnotify.Chmod:
					runjs(script,"CHMOD", event.Name)
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
				waitAndAddWatcher(script,filename,watcher)
			case <-removeCh:
				waitAndAddWatcher(script, filename,watcher)
			}
		}
	}()

	log.Fatalln(<-errCh)
}










func waitUntilFind(script, filename string) error {
	b:=false
	for {
		time.Sleep(1 * time.Second)
		_, err := os.Stat(filename)
		if err != nil {
			if os.IsNotExist(err) {
				b=true;
				runjs(script,"NOT_FOUND",filename)
				continue
			} else {
				return err
			}
		}
		if b {
			runjs(script,"EXIST",filename)
		}
		break
	}
	return nil
}



func waitAndAddWatcher(script, filename string, watcher *fsnotify.Watcher){
	err := waitUntilFind(script, filename)
	if err != nil {
		log.Fatalln(err)
	}
	err = watcher.Add(filename)
	if err != nil {
		log.Fatalln(err)
	}
}

func runjs (script string, op string, name string){
	vm := otto.New()
	underscore.Enable()


	b, err := ioutil.ReadFile(script) // just pass the file name
	if err != nil {
		log.Fatalln(err)
	}
	code := string(b)

	vm.Set("kExec", kExec)
	vm.Set("kFileToString",kFileToString)
	vm.Set("kStringToFile",kStringToFile)
	vm.Set("kAppendFile",kAppendFile)
	vm.Set("kRemoveFile",kRemoveFile)


	vm.Run(code)

	vm.Call(strings.ToLower(op),nil, name, op, os.Args)

}


func  kExec(name string, arg ...string) (string,string, int){
	cmd := exec.Command(name, arg ...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "",fmt.Sprintf ("kExec failed with %s", err),-6

	}
	return string(stdout.Bytes()), string(stderr.Bytes()), 0
}


func kAppendFile(value, filename string) int{
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return -1
	}

	defer f.Close()

	if _, err = f.WriteString(value); err != nil {
		return -1
	}
	return 0
}

func kRemoveFile(filename string) int {
	var err = os.Remove(filename)
	if err != nil { return -1}
	return 0
}

func kStringToFile( value string, filename string ) int {
	d1 := []byte(value)
	err := ioutil.WriteFile(filename, d1, 0644)
	if err != nil {
		return  -1
	}
	return 0
}




func kFileToString(filename string) (string, int) {
	dat, err := ioutil.ReadFile(filename)
	if err != nil {
		return "Error: " + err.Error(), -1

	}
	return string(dat), 0
}