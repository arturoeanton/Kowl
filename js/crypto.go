package js

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)
func KEncrypt(skey , message string) ( string, int) {

	rest := len(skey) % aes.BlockSize

	if rest >  0 {
		for i := 0; i < (aes.BlockSize - rest); i++ {
			skey = skey + " "
		}
	}


	key := []byte(skey)
	plainText := []byte(message)

	block, err := aes.NewCipher(key)
	if err != nil {
		return err.Error(), -1
	}

	//IV needs to be unique, but doesn't have to be secure.
	//It's common to put it at the beginning of the ciphertext.
	cipherText := make([]byte, aes.BlockSize+len(plainText))
	iv := cipherText[:aes.BlockSize]
	if _, err = io.ReadFull(rand.Reader, iv); err != nil {
		return err.Error(), -2
	}

	stream := cipher.NewCFBEncrypter(block, iv)
	stream.XORKeyStream(cipherText[aes.BlockSize:], plainText)

	//returns to base64 encoded string
	encmess := base64.URLEncoding.EncodeToString(cipherText)
	return encmess, 0
}

func KDecrypt(skey, securemess string) ( string,  int) {
	rest := len(skey) % aes.BlockSize

	if rest >  0 {
		for i := 0; i < (aes.BlockSize - rest); i++ {
			skey = skey + " "
		}
	}

	key := []byte(skey)
	cipherText, err := base64.URLEncoding.DecodeString(securemess)
	if err != nil {
		return err.Error() , -1
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return err.Error(), -2
	}

	if len(cipherText) < aes.BlockSize {
		err = errors.New("Ciphertext block size is too short!")
		return "Ciphertext block size is too short!" , -3
	}

	//IV needs to be unique, but doesn't have to be secure.
	//It's common to put it at the beginning of the ciphertext.
	iv := cipherText[:aes.BlockSize]
	cipherText = cipherText[aes.BlockSize:]

	stream := cipher.NewCFBDecrypter(block, iv)
	// XORKeyStream can work in-place if the two arguments are the same.
	stream.XORKeyStream(cipherText, cipherText)

	decodedmess := string(cipherText)
	return decodedmess, 0
}