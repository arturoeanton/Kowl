package main

import (
	"Kowl/js"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/jessevdk/go-flags"
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

var (
	opts struct {
		Filename       string `short:"f" required:"true" long:"filename" description:"filename that wants to be observed"`
		Script         string `short:"j" required:"true" long:"javascript" description:"Js that wants that executes the actions"`
		Millisecond    *int   `short:"m" default:"1000" long:"millisecond" description:"Millisecond change check defaul 1000"`
		FlagNotWatcher bool   `short:"w" long:"flagNotWatcher" description:"Watcher disable"`
	}
	errCh chan error
)

func observer(watcher *fsnotify.Watcher, script, filename string) func() {
	runjs(script, "EXIST", filename)
	return func() {
		var err error
		watcher, err = fsnotify.NewWatcher()
		if err != nil {
			log.Fatalln(err)
		}
		defer watcher.Close()
		watcher.Add(filename)
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
				case event.Op&fsnotify.Rename == fsnotify.Rename:
					runjs(script, "RENAME", event.Name)
				case event.Op&fsnotify.Chmod == fsnotify.Chmod:
					runjs(script, "CHMOD", event.Name)
				}
			case err := <-watcher.Errors:
				errCh <- err
			}
		}
	}
}

func main() {
	_, err := flags.ParseArgs(&opts, os.Args)
	if err != nil {
		os.Exit(-1)
	}
	if err != nil {
		os.Exit(-1)
	}
	filename := opts.Filename
	script := opts.Script
	millisecond := *opts.Millisecond
	flagNotWatcher := opts.FlagNotWatcher

	fmt.Println(filename)
	fmt.Println(script)
	fmt.Println(millisecond)
	fmt.Println(flagNotWatcher)
	errCh = make(chan error)

	var watcher fsnotify.Watcher
	var flagExecutedObserver bool

	if !flagNotWatcher {
		flagExecutedObserver = false
		ticker := time.NewTicker(time.Duration(1) * time.Second)
		go func() {
			for {
				select {
				case <-ticker.C:
					{
						if _, err := os.Stat(filename); os.IsNotExist(err) {
							flagExecutedObserver = false
						} else {
							if !flagNotWatcher {
								if !flagExecutedObserver {
									flagExecutedObserver = true
									go observer(&watcher, script, filename)()
								}
							}
						}
					}
				}
			}
		}()
	}

	if millisecond > 0 {
		ticker := time.NewTicker(time.Duration(millisecond) * time.Millisecond)
		go func() {
			for {
				select {
				case <-ticker.C:
					{
						if _, err := os.Stat(filename); os.IsNotExist(err) {
							runjs(script, "NOT_FOUND", filename)
						} else {
							if (*opts.Millisecond) > 0 {
								runjs(script, "TICKER", filename)
							}
						}
					}
				}
			}
		}()
	}
	log.Fatalln(<-errCh)

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

	vm.Set("kGetEnv", os.Getenv)
	vm.Set("kSetEnv", os.Setenv)
	vm.Set("kHostname", os.Hostname)
	vm.Set("kGetpid", os.Getpid)
	vm.Set("kGetppid", os.Getppid)
	vm.Set("kGetgid", os.Getgid)
	vm.Set("kGetuid", os.Getuid)
	vm.Set("kGetegid", os.Getegid)
	vm.Set("kArgs", os.Args)

	vm.Set("kNow", time.Now)

	vm.Run(code)

	vm.Call(strings.ToLower(op), nil, name, op, os.Args)

}
