package js

import (
	"io/ioutil"
	"os"
)



func KAppendFile(value, filename string) int {
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

func KRemoveFile(filename string) int {
	var err = os.Remove(filename)
	if err != nil {
		return -1
	}
	return 0
}

func KStringToFile(value string, filename string) int {
	d1 := []byte(value)
	err := ioutil.WriteFile(filename, d1, 0644)
	if err != nil {
		return -1
	}
	return 0
}

func KFileToString(filename string) (string, int) {
	dat, err := ioutil.ReadFile(filename)
	if err != nil {
		return "Error: " + err.Error(), -1

	}
	return string(dat), 0
}